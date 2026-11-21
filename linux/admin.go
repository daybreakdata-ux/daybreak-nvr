// Admin surface for the full web UI (Linux v1.0).
//
// adminBackend implements webview.Admin for both the desktop GUI
// (via phoneProvider) and the headless runner: the settings and
// camera operations are the same, only the reload hook differs —
// the GUI must touch Fyne widgets on the UI thread, the headless
// runner does not.
package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"daybreaknvr/internal/engine"
	"daybreaknvr/internal/webview"
)

// adminBackend is the web UI's management surface.
type adminBackend struct {
	get      func() engine.Settings
	reload   func() // apply current settings to the running services; async-safe
	ffmpeg   string
	tracker  *engine.MotionTracker
	recorder *engine.Recorder
	// eventRec is the v1.6 direct event recorder (motion/person
	// cameras); Status counts it alongside the recorder.
	eventRec   *engine.EventRecorder
	stillCache *engine.StillCache // v1.6 web zone picker stills (5 s TTL)
	remote     func() string // one-line remote status ("" = off / unknown)
	// onUsersChanged is called after a user is added, changed or
	// deleted, so the app can push the new user list to the relay.
	onUsersChanged func()
}

func (a *adminBackend) Version() string { return version }

func settingsToDTO(s engine.Settings) webview.SettingsDTO {
	return webview.SettingsDTO{
		RecordingsDir:     s.RecordingsDir,
		SegmentMinutes:    s.SegmentMinutes,
		RetentionDays:     s.RetentionDays,
		MaxTotalGB:        s.MaxTotalGB,
		PreviewFPS:        s.PreviewFPS,
		GridLayout:        s.GridLayout,
		PreBufferSeconds:  s.PreBufferSeconds,
		PostBufferSeconds: s.PostBufferSeconds,
	}
}

// applySettingsDTO validates web-submitted settings onto a copy of
// the current settings (same bounds as the desktop Devices tab).
// Phone and remote viewing are not in the DTO any more (v1.3) —
// they keep their stored values untouched.
func applySettingsDTO(s engine.Settings, d webview.SettingsDTO) (engine.Settings, error) {
	if dir := strings.TrimSpace(d.RecordingsDir); dir != "" {
		s.RecordingsDir = dir
	}
	if d.SegmentMinutes >= 1 && d.SegmentMinutes <= 120 {
		s.SegmentMinutes = d.SegmentMinutes
	}
	if d.RetentionDays >= 1 && d.RetentionDays <= 365 {
		s.RetentionDays = d.RetentionDays
	}
	if d.MaxTotalGB >= 0 {
		s.MaxTotalGB = d.MaxTotalGB
	}
	if d.PreviewFPS >= 1 && d.PreviewFPS <= 30 {
		s.PreviewFPS = d.PreviewFPS
	}
	switch d.GridLayout {
	case 1, 4, 9, 16:
		s.GridLayout = d.GridLayout
	}
	if d.PreBufferSeconds >= 0 && d.PreBufferSeconds <= 120 {
		s.PreBufferSeconds = d.PreBufferSeconds
	}
	if d.PostBufferSeconds >= 0 && d.PostBufferSeconds <= 120 {
		s.PostBufferSeconds = d.PostBufferSeconds
	}
	return s, nil
}

func (a *adminBackend) GetSettings() webview.SettingsDTO {
	return settingsToDTO(a.get())
}

func (a *adminBackend) UpdateSettings(d webview.SettingsDTO) error {
	s, err := applySettingsDTO(a.get(), d)
	if err != nil {
		return err
	}
	if err := s.Save(); err != nil {
		return fmt.Errorf("could not save settings: %w", err)
	}
	a.reload()
	return nil
}

func cameraToDTO(c engine.Camera) webview.CameraDTO {
	return webview.CameraDTO{
		ID: c.ID, Name: c.Name, IP: c.IP, Username: c.Username,
		Password: c.Password, MainURL: c.MainURL, SubURL: c.SubURL,
		Enabled: c.Enabled, Record: c.Record,
		RecordMode: c.Mode(), Sensitivity: c.Sensitivity,
		ZoneGrid: c.ZoneGrid, HumanDetect: c.HumanDetect,
		HumanConfidence: c.HumanConfidence,
	}
}

func (a *adminBackend) GetCameras() []webview.CameraDTO {
	s := a.get()
	out := make([]webview.CameraDTO, 0, len(s.Cameras))
	for _, c := range s.Cameras {
		out = append(out, cameraToDTO(c))
	}
	return out
}

func (a *adminBackend) SaveCamera(d webview.CameraDTO) (webview.CameraDTO, error) {
	s := a.get()
	base := engine.Camera{Enabled: true, Record: true, RecordMode: engine.RecordMotion, Sensitivity: engine.SensitivityDefault}
	isNew := strings.TrimSpace(d.ID) == ""
	if !isNew {
		found := false
		for _, c := range s.Cameras {
			if c.ID == d.ID {
				base = c
				found = true
				break
			}
		}
		if !found {
			return webview.CameraDTO{}, fmt.Errorf("no camera with id %q", d.ID)
		}
	}
	if len(s.Cameras) >= engine.MaxCameras && isNew {
		return webview.CameraDTO{}, fmt.Errorf("limit reached — Daybreak NVR supports up to %d cameras", engine.MaxCameras)
	}
	saved, err := engine.BuildCamera(base, engine.EditorInput{
		Name: d.Name, IP: d.IP, Username: d.Username, Password: d.Password,
		MainURL: d.MainURL, SubURL: d.SubURL, Enabled: d.Enabled,
		Record: d.Record, RecordMode: d.RecordMode,
		Sensitivity: d.Sensitivity, ZoneGrid: d.ZoneGrid,
		HumanDetect: d.HumanDetect, HumanConfidence: d.HumanConfidence,
	})
	if err != nil {
		return webview.CameraDTO{}, err
	}
	if isNew {
		saved.ID = fmt.Sprintf("cam%d", time.Now().UnixNano())
		s.Cameras = append(s.Cameras, saved)
	} else {
		for i := range s.Cameras {
			if s.Cameras[i].ID == saved.ID {
				s.Cameras[i] = saved
			}
		}
	}
	if err := s.Save(); err != nil {
		return webview.CameraDTO{}, fmt.Errorf("could not save settings: %w", err)
	}
	a.reload()
	return cameraToDTO(saved), nil
}

func (a *adminBackend) DeleteCamera(id string) error {
	s := a.get()
	idx := -1
	for i, c := range s.Cameras {
		if c.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("no camera with id %q", id)
	}
	s.Cameras = append(s.Cameras[:idx], s.Cameras[idx+1:]...)
	if err := s.Save(); err != nil {
		return fmt.Errorf("could not save settings: %w", err)
	}
	a.reload()
	return nil
}

// testCameraStream tries a camera's sub-stream and reports whether
// video arrived. Shared by the web UI and the desktop editor's
// Test Connection button (same 6-second probe).
func testCameraStream(ffmpeg string, cam engine.Camera) (bool, string) {
	url := cam.SubStreamURL()
	if url == "" {
		return false, "Enter an IP address (or RTSP URL) first."
	}
	p := engine.StartPump(ffmpeg, engine.PumpOptions{Input: url, Width: 320, FPS: 5})
	ok := false
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if p.Frames() > 0 {
			ok = true
			break
		}
		if p.Stopped() {
			break
		}
		time.Sleep(120 * time.Millisecond)
	}
	p.Stop()
	if ok {
		return true, "Connected — the camera answered with video."
	}
	return false, "No video. Check the IP, the Camera Account username/password, and that the camera is on the same network."
}

func (a *adminBackend) TestCamera(d webview.CameraDTO) (bool, string) {
	cam, err := engine.BuildCamera(engine.Camera{}, engine.EditorInput{
		Name: d.Name, IP: d.IP, Username: d.Username, Password: d.Password,
		MainURL: d.MainURL, SubURL: d.SubURL, Enabled: true, Record: true,
		RecordMode: engine.RecordContinuous, Sensitivity: engine.SensitivityDefault,
	})
	if err != nil {
		return false, err.Error()
	}
	return testCameraStream(a.ffmpeg, cam)
}

func (a *adminBackend) MotionEvents(cameraID string, limit int) []webview.MotionEventDTO {
	s := a.get()
	names := map[string]string{}
	for _, c := range s.Cameras {
		names[c.ID] = c.Name
	}
	out := []webview.MotionEventDTO{}
	collect := func(id string) {
		if a.tracker == nil {
			return
		}
		evs := a.tracker.Events(id)
		for i, e := range evs {
			// The newest event is open while the tracker still
			// has the camera in its open set.
			open := i == len(evs)-1 && a.tracker.Active(id)
			out = append(out, webview.MotionEventDTO{
				CameraID: id, CameraName: names[id],
				Start: e.Start, End: e.End, Open: open,
				Kind: e.Kind, TrackID: e.TrackID, Confidence: e.Confidence,
			})
		}
	}
	if cameraID != "" {
		collect(cameraID)
	} else {
		for _, c := range s.Cameras {
			collect(c.ID)
		}
	}
	// Newest first.
	sort.Slice(out, func(i, j int) bool { return out[i].Start.After(out[j].Start) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (a *adminBackend) Status() webview.StatusDTO {
	s := a.get()
	rec := 0
	if a.recorder != nil {
		for _, c := range s.Cameras {
			if a.recorder.Status(c.ID).Recording {
				rec++
			}
		}
	}
	if a.eventRec != nil {
		rec += a.eventRec.RecordingCount()
	}
	total, files := engine.DiskUsage(s.RecordingsDir)
	remote := ""
	if a.remote != nil {
		remote = a.remote()
	}
	return webview.StatusDTO{
		Version:       version,
		Cameras:       len(s.Cameras),
		Recording:     rec,
		StoredBytes:   total,
		StoredFiles:   files,
		RecordingsDir: s.RecordingsDir,
		Remote:        remote,
	}
}

func (a *adminBackend) Reload() { a.reload() }

// CameraSnapshot grabs one fresh JPEG still from a camera's
// sub-stream, for the web UI's zone picker (the browser has no
// live pump to read a frame from). Stills are cached for 5 s so a
// burst of refreshes does not open a new RTSP connection each
// time. It returns a clear error when the camera cannot be
// reached, so the UI can fall back to the plain zone grid.
func (a *adminBackend) CameraSnapshot(id string) ([]byte, error) {
	s := a.get()
	var cam engine.Camera
	found := false
	for _, c := range s.Cameras {
		if c.ID == id {
			cam = c
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("no camera with id %q", id)
	}
	if a.stillCache == nil {
		a.stillCache = engine.NewStillCache(5*time.Second, nil)
	}
	return a.stillCache.Get(a.ffmpeg, id, cam.SubStreamURL())
}

func (a *adminBackend) usersChanged() {
	if a.onUsersChanged != nil {
		a.onUsersChanged()
	}
}

// ListUsers returns the sign-in accounts (built-in admin
// included); password hashes are never exposed.
func (a *adminBackend) ListUsers() []webview.UserDTO {
	out := []webview.UserDTO{}
	for _, u := range engine.EffectiveUsers() {
		out = append(out, webview.UserDTO{
			Username: u.Username, Role: u.Role, BuiltIn: u.BuiltIn,
		})
	}
	return out
}

func (a *adminBackend) SaveUser(in webview.UserInput) error {
	if _, err := engine.SetUser(in.Username, in.Password, in.Role); err != nil {
		return err
	}
	a.usersChanged()
	return nil
}

func (a *adminBackend) DeleteUser(username string) error {
	if err := engine.DeleteUser(username); err != nil {
		return err
	}
	a.usersChanged()
	return nil
}
