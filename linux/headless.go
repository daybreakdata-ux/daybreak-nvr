// Headless mode for Daybreak NVR (Linux v1.0): `daybreak-nvr
// --headless` runs the recorder, motion detector, web UI and
// remote-viewing manager with no GUI window. This is what the
// systemd user service runs, and what lets the app be tested on a
// headless machine. It mirrors the desktop app's service startup
// (main.go runApp) minus anything that touches Fyne.
package main

import (
	"context"
	"fmt"
	"image"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"daybreaknvr/internal/engine"
	"daybreaknvr/internal/startlog"
	"daybreaknvr/internal/webview"
)

// headlessService owns the running engine pieces in headless mode.
type headlessService struct {
	mu       sync.Mutex
	settings engine.Settings
	ffmpeg   string
	recorder *engine.Recorder
	tracker  *engine.MotionTracker
	phone    *webview.Server
	remote   *engine.RemoteManager

	pumps        map[string]*engine.Pump
	watchUntil   map[string]time.Time
	analyzers    map[string]*engine.Analyzer
	analyzerSens map[string]int
	analyzerZone map[string]string
	lastAnalyzed map[string]int64
	person       *personService
	snapKeeper   *engine.SnapshotKeeper // v1.4 best-frame snapshots
	eventRec     *engine.EventRecorder  // v1.6 direct event recording

	stopCh chan struct{}
	wg     sync.WaitGroup
}

func newHeadlessService(settings engine.Settings, ffmpeg string) *headlessService {
	return &headlessService{
		settings:     settings,
		ffmpeg:       ffmpeg,
		pumps:        map[string]*engine.Pump{},
		watchUntil:   map[string]time.Time{},
		analyzers:    map[string]*engine.Analyzer{},
		analyzerSens: map[string]int{},
		analyzerZone: map[string]string{},
		lastAnalyzed: map[string]int64{},
		stopCh:       make(chan struct{}),
	}
}

func (h *headlessService) getSettings() engine.Settings {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.settings
}

func (h *headlessService) setSettings(s engine.Settings) {
	h.mu.Lock()
	h.settings = s
	h.mu.Unlock()
}

// start brings up recorder, pumps, web UI and remote manager.
func (h *headlessService) start() {
	h.recorder = engine.NewRecorder(h.ffmpeg)
	h.tracker = engine.NewMotionTracker(
		filepath.Join(h.getSettings().RecordingsDir, ".motion-events.json"))
	h.snapKeeper = engine.NewSnapshotKeeper(h.getSettings().RecordingsDir, h.tracker)
	h.eventRec = engine.NewEventRecorder(h.ffmpeg, h.getSettings, h.tracker)
	h.recorder.Sync(h.getSettings())
	h.eventRec.Start()
	h.syncPumps()
	h.startPhoneServer()
	h.remote = engine.NewRemoteManager(h.ffmpeg, h.getSettings)
	h.remote.Start()
	h.person = newPersonService()
	h.person.onPersonFrame = func(camID string, cx, cy float32, img image.Image, now time.Time) {
		for _, cam := range h.getSettings().Cameras {
			if cam.ID == camID {
				h.snapKeeper.NotePersonFrame(camID, cam.Slug(), img, cx, cy, now)
				break
			}
		}
	}
	h.person.Start(h.getSettings, func(id string) *engine.Pump {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.pumps[id]
	}, h.tracker)

	h.wg.Add(3)
	go h.tickerLoop()
	go h.retentionLoop()
	go h.statusLoop()
}

// stop shuts everything down cleanly.
func (h *headlessService) stop() {
	close(h.stopCh)
	if h.person != nil {
		h.person.Stop()
	}
	if h.remote != nil {
		h.remote.Stop()
	}
	if h.phone != nil {
		h.phone.Stop()
	}
	h.recorder.StopAll()
	if h.eventRec != nil {
		h.eventRec.Stop()
	}
	h.stopAllPumps()
	if h.tracker != nil {
		h.tracker.Save()
	}
	h.wg.Wait()
}

// reload applies already-saved settings to the running services.
// Called by the web UI's Admin surface; the phone server restart
// is delayed so the API response that triggered the reload gets
// out first (a port change restarts this very server). Settings
// are re-read from disk first: the Admin surface saves the file,
// it does not mutate the in-memory copy.
func (h *headlessService) reload() {
	s, _ := engine.LoadSettings()
	h.setSettings(s)
	h.recorder.Sync(h.getSettings())
	h.syncPumps()
	go func() {
		time.Sleep(300 * time.Millisecond)
		h.startPhoneServer()
	}()
}

// startPhoneServer (re)starts the web UI server from settings.
func (h *headlessService) startPhoneServer() {
	h.mu.Lock()
	if h.phone != nil {
		h.phone.Stop()
		h.phone = nil
	}
	enabled := h.settings.PhoneEnabled
	port := h.settings.PhonePort
	h.mu.Unlock()
	if !enabled {
		startlog.Stage("headless: web UI disabled in settings")
		return
	}
	srv := webview.New(headlessProvider{h})
	srv.SetAuthenticator(func(username, password string) (string, bool) {
		u, ok := engine.Authenticate(username, password)
		if !ok {
			return "", false
		}
		return u.Role, true
	})
	srv.SetAdmin(h.admin())
	if err := srv.Start(port); err != nil {
		startlog.Stage("headless: web UI failed to start on port %d: %v", port, err)
		fmt.Printf("Daybreak NVR: web UI failed to start on port %d: %v\n", port, err)
		return
	}
	h.mu.Lock()
	h.phone = srv
	h.mu.Unlock()
	startlog.Stage("headless: web UI listening on port %d", port)
	fmt.Printf("Daybreak NVR: web UI at http://%s:%d/ui/login\n", lanIP(), port)
}

// admin builds the web UI management surface for this service.
func (h *headlessService) admin() *adminBackend {
	return &adminBackend{
		get:      h.getSettings,
		reload:   h.reload,
		ffmpeg:   h.ffmpeg,
		tracker:  h.tracker,
		recorder: h.recorder,
		eventRec: h.eventRec,
		remote:   h.remoteText,
		onUsersChanged: func() {
			if h.remote != nil {
				go h.remote.SyncUsersNow()
			}
		},
	}
}

func (h *headlessService) remoteText() string {
	if h.remote == nil {
		return ""
	}
	st := h.remote.Status()
	switch st.Mode {
	case engine.RemoteModeOff, "":
		return "Off"
	}
	if st.Connected {
		return "Connected"
	}
	if st.LastError != "" {
		return "Relay unreachable"
	}
	return "Connecting…"
}

// ---------- pumps (same policy as the desktop app) ----------

// camByIDLocked reads h.settings directly: every caller holds h.mu
// (syncPumps, watchHold). It must NOT call getSettings(), which
// locks h.mu — Go mutexes are not re-entrant, so that deadlocked
// headless startup before the web UI ever started (v1.0 bug, found
// by smoke-testing --headless: log stopped after "ffmpeg found").
func (h *headlessService) camByIDLocked(id string) (engine.Camera, bool) {
	for _, c := range h.settings.Cameras {
		if c.ID == id {
			return c, true
		}
	}
	return engine.Camera{}, false
}

func (h *headlessService) ensurePumpLocked(cam engine.Camera) *engine.Pump {
	if p, ok := h.pumps[cam.ID]; ok && p != nil && !p.Stopped() {
		return p
	}
	url := cam.SubStreamURL()
	if url == "" {
		return nil
	}
	p := engine.StartPump(h.ffmpeg, engine.PumpOptions{
		Input: url,
		Width: 640,
		FPS:   h.settings.PreviewFPS, // direct: caller holds h.mu, see camByIDLocked
	})
	h.pumps[cam.ID] = p
	return p
}

func (h *headlessService) neededLocked() map[string]bool {
	need := map[string]bool{}
	now := time.Now()
	for id, until := range h.watchUntil {
		if until.After(now) {
			need[id] = true
		}
	}
	for _, c := range h.settings.Cameras { // direct: caller holds h.mu, see camByIDLocked
		if c.Enabled && c.Record &&
			(c.Mode() == engine.RecordMotion || c.Mode() == engine.RecordPerson) {
			need[c.ID] = true
		}
	}
	return need
}

func (h *headlessService) syncPumps() {
	h.mu.Lock()
	defer h.mu.Unlock()
	need := h.neededLocked()
	for id, p := range h.pumps {
		if !need[id] {
			p.Stop()
			delete(h.pumps, id)
		}
	}
	for id := range need {
		if cam, ok := h.camByIDLocked(id); ok && cam.Enabled {
			// v1.8 watchdog: a pump whose ffmpeg is alive but
			// has produced no frame for 30 s (stalled RTSP
			// connection) is killed here so ensurePumpLocked
			// starts a fresh one — a stalled process used to
			// sit dead forever because it never exited.
			if p := h.pumps[id]; p != nil && p.Stalled(engine.PumpStallAfter) {
				startlog.Stage("detector pump for %s stalled (no frames for %s) — restarting",
					cam.Slug(), engine.PumpStallAfter)
				p.Stop()
				delete(h.pumps, id)
			}
			h.ensurePumpLocked(cam)
		}
	}
}

func (h *headlessService) stopAllPumps() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, p := range h.pumps {
		p.Stop()
		delete(h.pumps, id)
	}
}

func (h *headlessService) watchHold(id string) func() {
	h.mu.Lock()
	h.watchUntil[id] = time.Now().Add(45 * time.Second)
	if cam, ok := h.camByIDLocked(id); ok && cam.Enabled {
		h.ensurePumpLocked(cam)
	}
	h.mu.Unlock()
	return func() {
		h.mu.Lock()
		h.watchUntil[id] = time.Now().Add(5 * time.Second)
		h.mu.Unlock()
	}
}

// ---------- motion ----------

func (h *headlessService) motionTick() {
	now := time.Now()
	for _, cam := range h.getSettings().Cameras {
		if !cam.Enabled || !cam.Record ||
			(cam.Mode() != engine.RecordMotion && cam.Mode() != engine.RecordPerson) {
			continue
		}
		h.mu.Lock()
		p := h.pumps[cam.ID]
		h.mu.Unlock()
		if p == nil {
			continue
		}
		fr := p.Frames()
		if fr == 0 || fr == h.lastAnalyzed[cam.ID] {
			continue
		}
		h.lastAnalyzed[cam.ID] = fr
		az, ok := h.analyzers[cam.ID]
		if !ok || h.analyzerSens[cam.ID] != cam.Sensitivity || h.analyzerZone[cam.ID] != cam.ZoneGrid {
			az = engine.NewAnalyzer(cam.Sensitivity, cam.ZoneGrid)
			h.analyzers[cam.ID] = az
			h.analyzerSens[cam.ID] = cam.Sensitivity
			h.analyzerZone[cam.ID] = cam.ZoneGrid
		}
		if img := p.Latest(); img != nil {
			if hot, cx, cy := az.ObserveCentroid(h.tracker, cam.ID, img, now); hot {
				h.snapKeeper.NoteFrame(cam.ID, cam.Slug(), img, cx, cy, now)
			}
		}
	}
	if closed := h.tracker.CloseStale(now, 5*time.Second); len(closed) > 0 {
		// v1.6: in Person-only mode a motion-only event (no
		// person) never records, so it gets no snapshot either.
		kept := closed[:0]
		for _, c := range closed {
			skip := false
			for _, cam := range h.getSettings().Cameras {
				if cam.ID == c.CamID && cam.Mode() == engine.RecordPerson && c.Event.Kind != "person" {
					skip = true
					break
				}
			}
			if !skip {
				kept = append(kept, c)
			}
		}
		h.snapKeeper.WriteClosed(kept)
	}
}

// ---------- loops ----------

func (h *headlessService) tickerLoop() {
	defer h.wg.Done()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-h.stopCh:
			return
		case <-t.C:
			startlog.Guard("headless tick", func() {
				h.motionTick()
				h.syncPumps()
				if time.Now().Second()%15 == 0 {
					h.tracker.Save()
				}
			})
		}
	}
}

func (h *headlessService) retentionLoop() {
	defer h.wg.Done()
	for {
		select {
		case <-h.stopCh:
			return
		case <-time.After(15 * time.Minute):
		}
		startlog.Guard("headless retention", func() {
			deleted, freed := engine.RunRetention(h.getSettings())
			h.tracker.Prune(time.Now().Add(-48 * time.Hour))
			h.tracker.Save()
			if deleted > 0 {
				startlog.Stage("headless: housekeeping removed %d clips, freed %s",
					deleted, humanBytes(freed))
			}
		})
	}
}

func (h *headlessService) statusLoop() {
	defer h.wg.Done()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-h.stopCh:
			return
		case <-t.C:
			s := h.getSettings()
			rec := 0
			for _, c := range s.Cameras {
				if h.recorder.Status(c.ID).Recording {
					rec++
				}
			}
			if h.eventRec != nil {
				rec += h.eventRec.RecordingCount()
			}
			total, files := engine.DiskUsage(s.RecordingsDir)
			fmt.Printf("Daybreak NVR: recording %d of %d cameras · %s in %d clips\n",
				rec, len(s.Cameras), humanBytes(total), files)
		}
	}
}

// ---------- webview provider ----------

type headlessProvider struct{ h *headlessService }

func (p headlessProvider) Cameras() []webview.CameraInfo {
	var out []webview.CameraInfo
	for _, c := range p.h.getSettings().Cameras {
		if c.Enabled {
			out = append(out, webview.CameraInfo{ID: c.ID, Name: c.Name})
		}
	}
	return out
}

func (p headlessProvider) Watch(id string) func() { return p.h.watchHold(id) }

func (p headlessProvider) LatestFrame(id string) image.Image {
	p.h.mu.Lock()
	pump := p.h.pumps[id]
	p.h.mu.Unlock()
	if pump == nil {
		return nil
	}
	return pump.Latest()
}

func (p headlessProvider) Clips() []engine.Clip {
	s := p.h.getSettings()
	return engine.ScanLibrary(s.RecordingsDir, s.Cameras)
}

func (p headlessProvider) ClipByToken(token string) (string, bool) {
	root := p.h.getSettings().RecordingsDir
	for _, c := range p.Clips() {
		if webview.ClipToken(c) == token {
			if strings.HasPrefix(c.Path, root) {
				return c.Path, true
			}
			return "", false
		}
	}
	return "", false
}

func (p headlessProvider) DeleteClip(token string) bool {
	s := p.h.getSettings()
	return engine.DeleteClipFile(s.RecordingsDir, s.Cameras, token)
}

// ---------- entry point ----------

// runHeadless starts the service and blocks until SIGINT/SIGTERM.
func runHeadless(logPath string) {
	startlog.Stage("headless mode — engine + web UI, no GUI window")
	// v1.7: same takeover as the GUI — kill other NVR instances
	// first (including a running GUI), then take the lock.
	if !takeOverSingleInstance() {
		startlog.Stage("headless: another copy is already running")
		fmt.Println("Daybreak NVR is already running.")
		return
	}
	settings, migrated := engine.LoadSettings()
	startlog.Stage("settings loaded: %d camera(s), recordings in %s",
		len(settings.Cameras), settings.RecordingsDir)
	if migrated {
		if err := settings.Save(); err != nil {
			startlog.Stage("settings migration save failed (continuing): %v", err)
		}
	}
	ffmpeg := engine.FindFFmpeg()
	if ffmpeg == "" {
		startlog.Stage("WARNING: ffmpeg not found — recording/preview unavailable until ffmpeg is installed or bundled next to the binary")
		fmt.Println("Daybreak NVR: WARNING — ffmpeg not found; recording unavailable.")
	} else {
		startlog.Stage("ffmpeg found: %s", ffmpeg)
	}

	svc := newHeadlessService(settings, ffmpeg)
	svc.start()
	fmt.Printf("Daybreak NVR v%s (Linux) running headless — Ctrl+C to stop.\n", version)
	startlog.Stage("headless: services started")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	startlog.Stage("headless: shutdown signal received")
	fmt.Println("Daybreak NVR: stopping…")
	svc.stop()
	startlog.Stage("headless: clean shutdown")
	fmt.Println("Daybreak NVR: stopped.")
}
