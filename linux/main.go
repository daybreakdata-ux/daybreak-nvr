// Daybreak NVR — a Linux desktop NVR for TP-Link Tapo (and any
// RTSP) cameras, in the spirit of SmartPSS Lite's layout with
// Blue-Iris-style always-on segment recording underneath.
//
// Recording and decoding are done by the bundled ffmpeg; this app is
// the supervisor, the video wall, the motion detector, the phone
// viewing server and the playback desk.
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"daybreaknvr/internal/engine"
	"daybreaknvr/internal/startlog"
	"daybreaknvr/internal/webview"
)

const version = "1.8" // Linux v1.8 — timescale fix (event body + pre-roll share 15360, so the finalize concat no longer inflates durations ~5.86x and drops every clip), worker liveness watchdog (a stalled-but-alive pre-buffer or detector ffmpeg is killed and restarted after 30 s with no output), and the pre-buffer pauses while an event records (two RTSP connections during an event, not three). v1.7 kept: no desktop sign-in gate (full local admin, services start immediately) and kill-others on launch.

//go:embed icon.png
var iconBytes []byte

// uiReady flips true just before the event loop starts. Before that,
// widget setup runs on the constructing goroutine and must not go
// through fyne.Do (which queues onto a loop that isn't running yet).
var uiReady atomic.Bool

func doUI(f func()) {
	if uiReady.Load() {
		fyne.Do(f)
	} else {
		f()
	}
}

type nvrApp struct {
	app      fyne.App
	win      fyne.Window
	settings engine.Settings
	ffmpeg   string
	recorder *engine.Recorder

	// role is always admin on the desktop (v1.7: the launch
	// sign-in gate is gone — this is his own desktop, trusted).
	// The role still gates the Devices/Playback viewer
	// restrictions, which simply never trigger here; the web UI
	// and the relay keep their own sign-in.
	role string

	live     *liveView
	playback *playbackView
	devices  *devicesView

	stack     *container.AppTabs
	diskLabel *widget.Label
	clockLbl  *widget.Label

	// Shared sub-stream pumps: one per camera, used by the live
	// wall, the motion analyzers and phone viewing alike.
	pumpsMu    sync.Mutex
	pumps      map[string]*engine.Pump
	visible    map[string]bool
	watchUntil map[string]time.Time

	// Motion.
	tracker      *engine.MotionTracker
	analyzers    map[string]*engine.Analyzer
	analyzerSens map[string]int
	analyzerZone map[string]string
	lastAnalyzed map[string]int64
	// Snapshot keeper (v1.4): best frame per open motion event.
	snapKeeper *engine.SnapshotKeeper
	// Event recorder (v1.6): one clip per motion/person event,
	// recorded directly (no chunks, no finalizer).
	eventRec *engine.EventRecorder

	// On-device AI person detection (v1.3).
	person *personService

	// Phone viewing.
	phone *webview.Server

	// Remote viewing (on-demand push to the relay).
	remote *engine.RemoteManager
}

// parseHeadlessFlag reports whether --headless was passed. Kept
// separate from main() so it can be unit-tested.
func parseHeadlessFlag(args []string) bool {
	fs := flag.NewFlagSet("daybreak-nvr", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	headless := fs.Bool("headless", false, "run the engine and web UI with no GUI window")
	_ = fs.Parse(args)
	return *headless
}

func main() {
	// --headless runs the engine + web UI with no GUI window:
	// for Ubuntu Server / systemd use, and for testing on a
	// headless machine. The desktop GUI is the default.
	headless := parseHeadlessFlag(os.Args[1:])

	// FIRST thing: open the startup log. This binary has no
	// console on Windows, so without this file any startup
	// failure is completely invisible (v1.2 and earlier simply
	// "never opened" with no trace of why).
	logPath := startlog.Init()
	startlog.Stage("=== Daybreak NVR v%s starting (log: %s) ===", version, logPath)

	if headless {
		runHeadless(logPath)
		return
	}

	// Mesa driver selection (Windows only — v1.6): force Mesa's
	// safe software renderer BEFORE any Fyne/GL call. The bundled
	// Mesa WGL gallium driver reads GALLIUM_DRIVER when it
	// creates its screen — which happens at first GL context
	// creation, after main() starts — so setting it here reliably
	// switches llvmpipe → softpipe before any GL call. v1.5's log
	// from a real PC proved llvmpipe dies natively inside
	// win.Show() on that machine (window flashes, process gone,
	// no Go panic); softpipe is a pure CPU interpreter — no LLVM
	// JIT — so it runs on anything, at some drawing speed. An
	// externally set GALLIUM_DRIVER is honored (manual llvmpipe
	// experiments stay possible).
	//
	// Linux: native OpenGL is the default. Ubuntu desktop has a
	// real GL driver, and forcing softpipe would throw away GPU
	// acceleration for no reason. If a Linux machine ever has no
	// working GL, run with GALLIUM_DRIVER=softpipe
	// LIBGL_ALWAYS_SOFTWARE=1 daybreak-nvr — the launcher does
	// not set them.
	if runtime.GOOS == "windows" {
		if existing, ok := os.LookupEnv("GALLIUM_DRIVER"); ok && existing != "" {
			startlog.Stage("mesa driver: GALLIUM_DRIVER=%s (from environment: %s)", existing, existing)
		} else {
			_ = os.Setenv("GALLIUM_DRIVER", "softpipe")
			startlog.Stage("mesa driver: GALLIUM_DRIVER=softpipe (set by app)")
		}
		// Harmless on WGL; helps if any component consults it.
		if existing, ok := os.LookupEnv("LIBGL_ALWAYS_SOFTWARE"); ok && existing != "" {
			startlog.Stage("mesa driver: LIBGL_ALWAYS_SOFTWARE=%s (from environment: %s)", existing, existing)
		} else {
			_ = os.Setenv("LIBGL_ALWAYS_SOFTWARE", "1")
			startlog.Stage("mesa driver: LIBGL_ALWAYS_SOFTWARE=1 (set by app)")
		}
	}

	// Panic capture: log the panic + stack and tell the user
	// where the details are instead of exiting silently.
	defer func() {
		if r := recover(); r != nil {
			summary := startlog.LogPanic("main", r)
			showErrorBox("Daybreak NVR — startup problem",
				"Daybreak NVR hit a problem while starting and has to close.\n\n"+
					summary+"\n\nDetails are in the startup log:\n"+logPath+
					"\n\nSend that file to get it fixed.")
		}
	}()

	// Single instance (v1.7): kill every other NVR instance first
	// — the main binary and its ffmpeg workers, including
	// orphaned recorders whose parent is dead — then drop any
	// stale lock file and take the lock. A fresh open leaves
	// exactly one NVR running; the old "already running"
	// dead-end box is gone. If the lock still cannot be taken,
	// another copy genuinely started in between.
	if !takeOverSingleInstance() {
		startlog.Stage("second instance blocked — another Daybreak NVR started in between")
		showErrorBox("Daybreak NVR",
			"Daybreak NVR is already running — check your taskbar or press Alt+Tab to find its window.")
		return
	}
	startlog.Stage("single instance confirmed")

	runApp()
}

// newNVRApp builds the app state. v1.7: the desktop app is full
// local admin from construction — there is no sign-in gate, so the
// role is admin here and the viewer restrictions never trigger.
func newNVRApp(a fyne.App) *nvrApp {
	return &nvrApp{
		app:          a,
		role:         engine.RoleAdmin,
		pumps:        map[string]*engine.Pump{},
		visible:      map[string]bool{},
		watchUntil:   map[string]time.Time{},
		analyzers:    map[string]*engine.Analyzer{},
		analyzerSens: map[string]int{},
		analyzerZone: map[string]string{},
		lastAnalyzed: map[string]int64{},
	}
}

// runApp builds the app and window, SHOWS the window first, and
// only then builds the main UI and starts the recorder / pumps /
// phone server / remote manager — nothing network- or disk-bound
// may sit on the path to a visible window ever again. v1.7: there
// is no sign-in gate; the desktop app is full local admin and
// services start immediately on launch.
func runApp() {
	startlog.Stage("creating Fyne app")
	a := app.NewWithID("com.daybreak.nvr")
	a.SetIcon(fyne.NewStaticResource("icon.png", iconBytes))
	a.Settings().SetTheme(nvrTheme{})

	n := newNVRApp(a)

	startlog.Stage("loading settings from %s", engine.ConfigPath())
	settings, migrated := engine.LoadSettings()
	n.settings = settings
	startlog.Stage("settings loaded: %d camera(s), recordings in %s",
		len(settings.Cameras), settings.RecordingsDir)
	if migrated {
		if err := n.settings.Save(); err != nil {
			startlog.Stage("settings migration save failed (continuing): %v", err)
		} else {
			startlog.Stage("settings migrated and saved")
		}
	}

	startlog.Stage("locating ffmpeg")
	n.ffmpeg = engine.FindFFmpeg()
	if n.ffmpeg == "" {
		startlog.Stage("WARNING: ffmpeg not found — app will open with an in-app warning; recording/preview unavailable until ffmpeg is installed (sudo apt install ffmpeg) or the bundled copy is present")
	} else {
		startlog.Stage("ffmpeg found: %s", n.ffmpeg)
	}
	n.recorder = engine.NewRecorder(n.ffmpeg)
	n.tracker = engine.NewMotionTracker(
		filepath.Join(n.settings.RecordingsDir, ".motion-events.json"))
	n.snapKeeper = engine.NewSnapshotKeeper(n.settings.RecordingsDir, n.tracker)
	n.eventRec = engine.NewEventRecorder(n.ffmpeg, func() engine.Settings { return n.settings }, n.tracker)
	startlog.Stage("recorder, event recorder and motion tracker created")

	startlog.Stage("creating window")
	n.win = a.NewWindow("Daybreak NVR")
	n.win.Resize(fyne.NewSize(1320, 840))
	n.win.SetIcon(fyne.NewStaticResource("icon.png", iconBytes))
	// Center on the primary screen: a window that opens (or is
	// restored) on a disconnected or secondary display is the
	// classic "the app runs but I can't see it" failure — and
	// exactly what a real startup log showed on the user's PC
	// (clean launches, no visible window). Centering on the
	// primary display guarantees the window opens where the user
	// is looking.
	n.win.CenterOnScreen()
	startlog.Stage("window centered")

	// v1.7: no sign-in gate — the desktop app runs as full local
	// admin and the main UI is built immediately. The user system
	// itself is untouched (users still sync to the relay, the web
	// UI still requires sign-in); only the desktop gate is gone.
	// The window is still shown FIRST, as before, so a driver/GL
	// failure surfaces before any service work; enterMainUI then
	// replaces this placeholder with the main UI and starts the
	// services, and the event loop runs.
	n.win.SetContent(widget.NewLabel("Starting Daybreak NVR…"))
	n.win.SetCloseIntercept(func() { a.Quit() })

	// Show the window FIRST. Driver/GL problems surface here,
	// with the log proving exactly how far startup got.
	uiReady.Store(true)
	startlog.Stage("showing window (local admin — no sign-in)")
	n.win.Show()
	// Bracket: reaching this line proves Show() itself returned —
	// if a startup log ever ends at "showing window" with nothing
	// after it, the failure is inside native window/GL creation
	// (GLFW/driver), not in app code. The console build
	// (DaybreakNVR-Console.exe) exists to surface that error text.
	startlog.Stage("window show returned")
	n.win.RequestFocus()
	startlog.Stage("window focus requested")

	startlog.Stage("local admin (no sign-in) — building main UI and starting services")
	n.enterMainUI()

	startlog.Stage("entering event loop")
	a.Run()
	startlog.Stage("event loop exited — clean shutdown")
}

// enterMainUI builds the main tabbed UI (v1.7: immediately on
// launch, as full local admin — no sign-in gate) and starts the
// services (recorder, pumps, phone server, remote manager).
func (n *nvrApp) enterMainUI() {
	a := n.app
	startlog.Stage("building live view")
	n.live = newLiveView(n)
	startlog.Stage("building playback view")
	n.playback = newPlaybackView(n)
	startlog.Stage("building devices view")
	n.devices = newDevicesView(n)
	startlog.Stage("views built")

	n.stack = container.NewAppTabs(
		container.NewTabItem("Live Preview", n.live.root),
		container.NewTabItem("Playback", n.playback.root),
		container.NewTabItem("Devices & Settings", n.devices.root),
	)
	n.stack.SetTabLocation(container.TabLocationTop)
	if n.role == engine.RoleViewer {
		// Viewers keep live view + playback; the Devices tab's
		// controls are disabled by devicesView.applyRole.
		n.stack.SelectTab(n.stack.Items[0])
	}

	n.diskLabel = widget.NewLabel("")
	n.clockLbl = widget.NewLabel("")

	// No bottom bar (v1.3.1): the footer status line is gone; the
	// top bar (title, disk usage, clock) is the only chrome.
	content := container.NewBorder(
		container.NewBorder(nil, nil,
			widget.NewLabelWithStyle("DAYBREAK  NVR · v"+version, fyne.TextAlignLeading,
				fyne.TextStyle{Bold: true}),
			container.NewHBox(n.diskLabel, widget.NewSeparator(), n.clockLbl),
		),
		nil, nil, nil, n.stack)
	n.win.SetContent(content)

	n.win.SetCloseIntercept(func() {
		startlog.Stage("close requested — shutting down")
		n.stopAllPumps()
		n.playback.stopPump()
		n.recorder.StopAll()
		if n.eventRec != nil {
			n.eventRec.Stop()
		}
		if n.remote != nil {
			n.remote.Stop()
		}
		if n.phone != nil {
			n.phone.Stop()
		}
		n.tracker.Save()
		a.Quit()
	})

	// Services start after the window exists, each guarded so a
	// panic in any of them is logged instead of taking the whole
	// process down invisibly.
	startlog.Go("service startup", func() {
		startlog.Stage("starting services (recorder, pumps, phone server, remote manager)")
		if startlog.Guard("recorder sync", func() { n.recorder.Sync(n.settings) }) {
			startlog.Stage("recorder sync panicked — continuing without it this run")
		}
		startlog.Guard("event recorder start", func() {
			if n.eventRec != nil {
				n.eventRec.Start()
			}
		})
		startlog.Guard("pump sync", func() { n.syncPumps() })
		startlog.Guard("phone server start", func() { n.restartPhoneServer() })
		startlog.Guard("remote start", func() { n.startRemote() })
		startlog.Guard("person detection start", func() {
			n.person = newPersonService()
			// v1.4: person tracks feed the snapshot keeper with
			// their box centres (zone-gated in the service).
			n.person.onPersonFrame = func(camID string, cx, cy float32, img image.Image, now time.Time) {
				if cam, ok := n.camByID(camID); ok {
					n.snapKeeper.NotePersonFrame(camID, cam.Slug(), img, cx, cy, now)
				}
			}
			n.person.Start(func() engine.Settings { return n.settings }, n.pumpFor, n.tracker)
		})
		startlog.Go("retention loop", n.retentionLoop)
		startlog.Go("ticker", n.ticker)
		startlog.Stage("services started")
		n.refreshStatus()
	})
}

// ---------- shared pump manager ----------

func (n *nvrApp) camByID(id string) (engine.Camera, bool) {
	for _, c := range n.settings.Cameras {
		if c.ID == id {
			return c, true
		}
	}
	return engine.Camera{}, false
}

func (n *nvrApp) ensurePumpLocked(cam engine.Camera) *engine.Pump {
	if p, ok := n.pumps[cam.ID]; ok && p != nil && !p.Stopped() {
		return p
	}
	url := cam.SubStreamURL()
	if url == "" {
		return nil
	}
	p := engine.StartPump(n.ffmpeg, engine.PumpOptions{
		Input: url,
		Width: 640,
		FPS:   n.settings.PreviewFPS,
	})
	n.pumps[cam.ID] = p
	return p
}

// pumpFor returns the running pump for a camera, or nil.
func (n *nvrApp) pumpFor(id string) *engine.Pump {
	n.pumpsMu.Lock()
	defer n.pumpsMu.Unlock()
	return n.pumps[id]
}

// setVisible records which cameras are on the live wall.
func (n *nvrApp) setVisible(ids []string) {
	n.pumpsMu.Lock()
	defer n.pumpsMu.Unlock()
	n.visible = map[string]bool{}
	for _, id := range ids {
		n.visible[id] = true
	}
}

// watchHold implements webview.Provider.Watch: it keeps the camera's
// stream up while a phone is viewing, plus a grace window.
func (n *nvrApp) watchHold(id string) func() {
	n.pumpsMu.Lock()
	n.watchUntil[id] = time.Now().Add(45 * time.Second)
	if cam, ok := n.camByID(id); ok && cam.Enabled {
		n.ensurePumpLocked(cam)
	}
	n.pumpsMu.Unlock()
	return func() {
		n.pumpsMu.Lock()
		n.watchUntil[id] = time.Now().Add(5 * time.Second)
		n.pumpsMu.Unlock()
	}
}

// neededLocked computes which cameras should have a live pump now:
// visible on the wall, watched from a phone, or motion-recording
// (their analyzer drinks from the same pump).
func (n *nvrApp) neededLocked() map[string]bool {
	need := map[string]bool{}
	for id := range n.visible {
		need[id] = true
	}
	now := time.Now()
	for id, until := range n.watchUntil {
		if until.After(now) {
			need[id] = true
		}
	}
	for _, c := range n.settings.Cameras {
		if c.Enabled && c.Record &&
			(c.Mode() == engine.RecordMotion || c.Mode() == engine.RecordPerson) {
			need[c.ID] = true
		}
	}
	return need
}

// syncPumps starts pumps for needed cameras and stops the rest.
// Called on startup, on settings changes and every heartbeat.
func (n *nvrApp) syncPumps() {
	n.pumpsMu.Lock()
	defer n.pumpsMu.Unlock()
	need := n.neededLocked()
	for id, p := range n.pumps {
		if !need[id] {
			p.Stop()
			delete(n.pumps, id)
		}
	}
	for id := range need {
		if cam, ok := n.camByID(id); ok && cam.Enabled {
			// v1.8 watchdog: a pump whose ffmpeg is alive but
			// has produced no frame for 30 s (stalled RTSP
			// connection) is killed here so ensurePumpLocked
			// starts a fresh one — a stalled process used to
			// sit dead forever because it never exited.
			if p := n.pumps[id]; p != nil && p.Stalled(engine.PumpStallAfter) {
				startlog.Stage("detector pump for %s stalled (no frames for %s) — restarting",
					cam.Slug(), engine.PumpStallAfter)
				p.Stop()
				delete(n.pumps, id)
			}
			n.ensurePumpLocked(cam)
		}
	}
}

func (n *nvrApp) stopAllPumps() {
	n.pumpsMu.Lock()
	defer n.pumpsMu.Unlock()
	for id, p := range n.pumps {
		p.Stop()
		delete(n.pumps, id)
	}
}

// ---------- motion service ----------

// motionTick analyzes one fresh frame per motion/person camera
// (the frame pump runs at preview rate; one analysis per second
// is plenty) and maintains event open/close bookkeeping. Person
// mode cameras are analyzed exactly like Motion mode: the
// detector feeds the AI (which is motion-gated), and a person
// sighting opens a person event — the EventRecorder decides
// whether that event records.
func (n *nvrApp) motionTick() {
	now := time.Now()
	for _, cam := range n.settings.Cameras {
		if !cam.Enabled || !cam.Record ||
			(cam.Mode() != engine.RecordMotion && cam.Mode() != engine.RecordPerson) {
			continue
		}
		p := n.pumpFor(cam.ID)
		if p == nil {
			continue
		}
		fr := p.Frames()
		if fr == 0 || fr == n.lastAnalyzed[cam.ID] {
			continue
		}
		n.lastAnalyzed[cam.ID] = fr
		az, ok := n.analyzers[cam.ID]
		if !ok || n.analyzerSens[cam.ID] != cam.Sensitivity || n.analyzerZone[cam.ID] != cam.ZoneGrid {
			az = engine.NewAnalyzer(cam.Sensitivity, cam.ZoneGrid)
			n.analyzers[cam.ID] = az
			n.analyzerSens[cam.ID] = cam.Sensitivity
			n.analyzerZone[cam.ID] = cam.ZoneGrid
		}
		if img := p.Latest(); img != nil {
			// v1.4: a hot frame feeds the snapshot keeper with
			// its motion centroid; the keeper keeps the frame
			// nearest the centre while the event is open.
			if hot, cx, cy := az.ObserveCentroid(n.tracker, cam.ID, img, now); hot {
				n.snapKeeper.NoteFrame(cam.ID, cam.Slug(), img, cx, cy, now)
			}
		}
	}
	// v1.4: events that just closed write out their best frame.
	// v1.6: in Person-only mode a motion-only event (no person)
	// never records, so it gets no snapshot either — otherwise
	// the snapshot would be orphaned with no clip to attach to.
	if closed := n.tracker.CloseStale(now, 5*time.Second); len(closed) > 0 {
		kept := closed[:0]
		for _, c := range closed {
			if cam, ok := n.camByID(c.CamID); ok && cam.Mode() == engine.RecordPerson && c.Event.Kind != "person" {
				continue
			}
			kept = append(kept, c)
		}
		n.snapKeeper.WriteClosed(kept)
	}
}

// ---------- housekeeping ----------

// syncUsersToRelay pushes the current user list to the relay
// right now (called after a user is added, changed or deleted).
// A nil remote manager (not started yet) is a no-op.
func (n *nvrApp) syncUsersToRelay() {
	if n.remote != nil {
		go n.remote.SyncUsersNow()
	}
}

// reloadSettings is called after the devices view saves changes.
func (n *nvrApp) reloadSettings() {
	s, _ := engine.LoadSettings()
	n.settings = s
	n.recorder.Sync(n.settings)
	n.live.refreshDeviceList()
	n.syncPumps()
	n.playback.refreshClips()
	n.refreshStatus()
}

// refreshStatus recomputes the recording count and disk usage and
// updates the top bar's disk label. (v1.3.1: the bottom-bar status
// line is gone, so the recording count and clip total are computed
// but no longer displayed anywhere.)
func (n *nvrApp) refreshStatus() {
	rec := 0
	for _, c := range n.settings.Cameras {
		if n.recorder.Status(c.ID).Recording {
			rec++
		}
	}
	if n.eventRec != nil {
		rec += n.eventRec.RecordingCount()
	}
	total, files := engine.DiskUsage(n.settings.RecordingsDir)
	_ = rec   // footer removed in v1.3.1 — count kept for future use
	_ = files // footer removed in v1.3.1 — clip total kept for future use
	fyne.Do(func() {
		if n.diskLabel != nil {
			n.diskLabel.SetText(humanBytes(total))
		}
	})
}

func (n *nvrApp) ticker() {
	t := time.NewTicker(time.Second)
	for range t.C {
		// One bad tick must never kill the heartbeat.
		startlog.Guard("ticker tick", func() {
			fyne.Do(func() {
				if n.clockLbl != nil {
					n.clockLbl.SetText(time.Now().Format("Mon Jan 2 · 3:04:05 PM"))
				}
			})
			n.motionTick()
			n.syncPumps()
			n.live.tick()
			n.playback.tick()
			if time.Now().Second()%10 == 0 {
				n.refreshStatus()
				n.live.refreshDeviceList()
			}
			if time.Now().Second()%15 == 0 {
				n.tracker.Save()
			}
		})
	}
}

func (n *nvrApp) retentionLoop() {
	for {
		var deleted int
		var freed int64
		startlog.Guard("retention pass", func() {
			deleted, freed = engine.RunRetention(n.settings)
		})
		n.tracker.Prune(time.Now().Add(-48 * time.Hour))
		n.tracker.Save()
		if deleted > 0 {
			// v1.3.1: the desktop footer is gone, so the
			// housekeeping note goes to the startup log instead.
			startlog.Stage("Housekeeping: removed %d clips past retention, freed %s",
				deleted, humanBytes(freed))
		}
		time.Sleep(15 * time.Minute)
	}
}

func humanBytes(b int64) string {
	const u = 1024
	if b < u {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(u), 0
	for n := b / u; n >= u; n /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
