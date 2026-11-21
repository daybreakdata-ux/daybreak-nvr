package main

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"daybreaknvr/internal/engine"
	"daybreaknvr/internal/webview"
)

func TestParseHeadlessFlag(t *testing.T) {
	if parseHeadlessFlag(nil) {
		t.Fatal("no args should not be headless")
	}
	if parseHeadlessFlag([]string{"--headless"}) != true {
		t.Fatal("--headless should parse true")
	}
	if parseHeadlessFlag([]string{"--headless=false"}) {
		t.Fatal("--headless=false should parse false")
	}
	if parseHeadlessFlag([]string{"--bogus"}) {
		t.Fatal("unknown flags must not turn on headless")
	}
}

func TestApplySettingsDTO(t *testing.T) {
	base := engine.DefaultSettings()
	dto := webview.SettingsDTO{
		RecordingsDir: "/tmp/nvr-test", SegmentMinutes: 5, RetentionDays: 30,
		MaxTotalGB: 500, PreviewFPS: 15, GridLayout: 9,
		PreBufferSeconds: 10, PostBufferSeconds: 30,
	}
	out, err := applySettingsDTO(base, dto)
	if err != nil {
		t.Fatal(err)
	}
	if out.RecordingsDir != "/tmp/nvr-test" || out.SegmentMinutes != 5 ||
		out.RetentionDays != 30 || out.MaxTotalGB != 500 || out.PreviewFPS != 15 {
		t.Fatalf("storage settings not applied: %+v", out)
	}
	if out.GridLayout != 9 || out.PreBufferSeconds != 10 || out.PostBufferSeconds != 30 {
		t.Fatalf("general/buffer settings not applied: %+v", out)
	}
	// Phone and remote viewing are not in the DTO any more
	// (v1.3): applying settings must leave them exactly as they
	// were, so removing their UI can never silently disable them.
	if out.PhoneEnabled != base.PhoneEnabled || out.PhonePort != base.PhonePort {
		t.Fatalf("phone settings changed by the DTO: %+v", out)
	}
	if out.RemoteEnabled != base.RemoteEnabled || out.RemoteMode != base.RemoteMode {
		t.Fatalf("remote settings changed by the DTO: %+v", out)
	}

	// Out-of-range values are ignored, not fatal.
	bad := webview.SettingsDTO{SegmentMinutes: 9999, RetentionDays: 0, PreviewFPS: 99, GridLayout: 7, PreBufferSeconds: 999}
	out2, err := applySettingsDTO(base, bad)
	if err != nil {
		t.Fatal(err)
	}
	if out2.SegmentMinutes != base.SegmentMinutes || out2.PreviewFPS != base.PreviewFPS ||
		out2.GridLayout != base.GridLayout || out2.PreBufferSeconds != base.PreBufferSeconds {
		t.Fatalf("out-of-range values leaked through: %+v", out2)
	}
}

func TestAdminBackendCameraCRUD(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	reloaded := 0
	// Start from a clean settings file in the temp HOME.
	s, _ := engine.LoadSettings()
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	backend := &adminBackend{
		get: func() engine.Settings {
			loaded, _ := engine.LoadSettings()
			return loaded
		},
		reload:   func() { reloaded++ },
		ffmpeg:   "",
		tracker:  engine.NewMotionTracker(t.TempDir() + "/motion.json"),
		recorder: engine.NewRecorder(""),
		remote:   func() string { return "Off" },
	}

	// Add a camera via the Tapo-style fields.
	saved, err := backend.SaveCamera(webview.CameraDTO{
		Name: "Front Door", IP: "192.168.1.50", Username: "tapo",
		Password: "pw123", Enabled: true, Record: true,
		RecordMode: "motion", Sensitivity: 75,
		ZoneGrid: engine.DefaultZoneGrid(), HumanDetect: true, HumanConfidence: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" || saved.RecordMode != "motion" || saved.Sensitivity != 75 {
		t.Fatalf("saved camera wrong: %+v", saved)
	}
	if !saved.HumanDetect || saved.HumanConfidence != 60 || saved.ZoneGrid != engine.DefaultZoneGrid() {
		t.Fatalf("zone/AI fields not saved: %+v", saved)
	}
	if reloaded != 1 {
		t.Fatalf("reload count = %d, want 1", reloaded)
	}
	// The stored camera builds the Tapo URLs.
	stored := backend.GetCameras()
	if len(stored) != 1 {
		t.Fatalf("cameras = %d, want 1", len(stored))
	}
	cam := stored[0]
	if cam.Name != "Front Door" {
		t.Fatalf("camera name = %q", cam.Name)
	}
	// Edit it: switch to continuous.
	cam.RecordMode = "continuous"
	if _, err := backend.SaveCamera(cam); err != nil {
		t.Fatal(err)
	}
	if got := backend.GetCameras()[0]; got.RecordMode != "continuous" {
		t.Fatalf("edit did not stick: %+v", got)
	}
	// Validation: no name → error, nothing saved.
	if _, err := backend.SaveCamera(webview.CameraDTO{IP: "192.168.1.51"}); err == nil {
		t.Fatal("nameless camera saved without error")
	}
	if len(backend.GetCameras()) != 1 {
		t.Fatal("failed save changed the camera list")
	}
	// Delete.
	if err := backend.DeleteCamera(saved.ID); err != nil {
		t.Fatal(err)
	}
	if len(backend.GetCameras()) != 0 {
		t.Fatal("camera not deleted")
	}
	if err := backend.DeleteCamera("nope"); err == nil {
		t.Fatal("deleting unknown camera did not error")
	}
}

// v1.5.2: an Off ("Do not record") camera round-trips through
// the web UI backend and still appears in the camera list —
// live view only, but listed like any other camera.
func TestAdminBackendOffCamera(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	s, _ := engine.LoadSettings()
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	backend := &adminBackend{
		get: func() engine.Settings {
			loaded, _ := engine.LoadSettings()
			return loaded
		},
		reload:   func() {},
		ffmpeg:   "",
		tracker:  engine.NewMotionTracker(t.TempDir() + "/motion.json"),
		recorder: engine.NewRecorder(""),
		remote:   func() string { return "Off" },
	}

	saved, err := backend.SaveCamera(webview.CameraDTO{
		Name: "Gate", IP: "192.168.1.52", Enabled: true, Record: true,
		RecordMode: "off", Sensitivity: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.RecordMode != "off" {
		t.Fatalf("saved record_mode = %q, want off", saved.RecordMode)
	}
	// It still appears in the camera list (live view only).
	stored := backend.GetCameras()
	if len(stored) != 1 || stored[0].RecordMode != "off" {
		t.Fatalf("camera list = %+v, want one Off camera", stored)
	}
	// Switching it back to Motion sticks (recording resumes).
	stored[0].RecordMode = "motion"
	if _, err := backend.SaveCamera(stored[0]); err != nil {
		t.Fatal(err)
	}
	if got := backend.GetCameras()[0]; got.RecordMode != "motion" {
		t.Fatalf("switching back did not stick: %+v", got)
	}
	// A bogus mode is rejected, not stored.
	stored[0].RecordMode = "bogus"
	if _, err := backend.SaveCamera(stored[0]); err == nil {
		t.Fatal("bogus record mode saved without error")
	}
	if got := backend.GetCameras()[0]; got.RecordMode != "motion" {
		t.Fatalf("failed save changed the mode: %+v", got)
	}
}

// v1.5.2: an Off ("Do not record") camera still gets a live
// pump when it is visible on the wall (live view works), but no
// pump is kept running for it when it is not visible — it records
// nothing, so the motion analyzer has no reason to drink from it.
// v1.6: a Person-only camera round-trips through the web UI
// backend, and saving it forces human detection on (Person-only
// recording cannot work without it) — even when the request says
// human_detect: false.
func TestAdminBackendPersonCamera(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	s, _ := engine.LoadSettings()
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	backend := &adminBackend{
		get: func() engine.Settings {
			loaded, _ := engine.LoadSettings()
			return loaded
		},
		reload:   func() {},
		ffmpeg:   "",
		tracker:  engine.NewMotionTracker(t.TempDir() + "/motion.json"),
		recorder: engine.NewRecorder(""),
		remote:   func() string { return "Off" },
	}

	saved, err := backend.SaveCamera(webview.CameraDTO{
		Name: "Driveway", IP: "192.168.1.53", Enabled: true, Record: true,
		RecordMode: "person", Sensitivity: 50, HumanDetect: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.RecordMode != "person" {
		t.Fatalf("saved record_mode = %q, want person", saved.RecordMode)
	}
	if !saved.HumanDetect {
		t.Fatal("saving a Person-only camera did not force human detection on")
	}
	// It persists across a reload (the stored file is re-read by
	// the backend's get()).
	stored := backend.GetCameras()
	if len(stored) != 1 || stored[0].RecordMode != "person" || !stored[0].HumanDetect {
		t.Fatalf("camera list = %+v, want one Person-only camera with human detection on", stored)
	}
	// Switching it to Motion keeps human detection on (it was
	// turned on, not just forced at save time).
	stored[0].RecordMode = "motion"
	if _, err := backend.SaveCamera(stored[0]); err != nil {
		t.Fatal(err)
	}
	if got := backend.GetCameras()[0]; got.RecordMode != "motion" || !got.HumanDetect {
		t.Fatalf("switching to Motion lost the mode/human detection: %+v", got)
	}
}

// v1.6: the desktop editor also forces human detection on for a
// Person-only camera (BuildCamera is the single place every save
// path goes through).
func TestBuildCameraPersonForcesHumanDetect(t *testing.T) {
	in := engine.EditorInput{
		Name: "Driveway", IP: "192.168.1.53", Enabled: true, Record: true,
		RecordMode: engine.RecordPerson, Sensitivity: 50, HumanDetect: false,
	}
	cam, err := engine.BuildCamera(engine.Camera{}, in)
	if err != nil {
		t.Fatal(err)
	}
	if cam.Mode() != engine.RecordPerson {
		t.Fatalf("mode = %q, want person", cam.Mode())
	}
	if !cam.HumanDetect {
		t.Fatal("BuildCamera did not force human detection on for Person-only mode")
	}
	// And the mode survives a save/load round-trip.
	s := engine.DefaultSettings()
	s.Cameras = []engine.Camera{cam}
	if got := s.Cameras[0].Mode(); got != engine.RecordPerson {
		t.Fatalf("round-tripped mode = %q, want person", got)
	}
}

func TestNeededLockedOffCamera(t *testing.T) {
	offCam := engine.Camera{ID: "off1", Name: "Gate", Enabled: true, Record: true, RecordMode: engine.RecordOff}
	motCam := engine.Camera{ID: "mot1", Name: "Yard", Enabled: true, Record: true, RecordMode: engine.RecordMotion}
	// v1.6: a Person-only camera needs a pump too (its analyzer
	// feeds the person trigger).
	perCam := engine.Camera{ID: "per1", Name: "Drive", Enabled: true, Record: true, RecordMode: engine.RecordPerson}
	n := &nvrApp{
		visible:    map[string]bool{},
		watchUntil: map[string]time.Time{},
		settings:   engine.Settings{Cameras: []engine.Camera{offCam, motCam, perCam}},
	}
	// Nobody watching anything: the motion and person cameras
	// need a pump (for their analyzers); the Off camera does not.
	if need := n.neededLocked(); need["off1"] {
		t.Error("Off camera should not need a pump when not visible")
	} else if !need["mot1"] {
		t.Error("Motion camera should need a pump for its analyzer")
	} else if !need["per1"] {
		t.Error("Person-only camera should need a pump for its analyzer")
	}
	// Off camera visible on the wall: it gets a pump (live view).
	n.visible["off1"] = true
	if need := n.neededLocked(); !need["off1"] {
		t.Error("visible Off camera should need a pump for live view")
	}
}

func TestAdminBackendStatus(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	backend := &adminBackend{
		get:      func() engine.Settings { return engine.DefaultSettings() },
		reload:   func() {},
		recorder: engine.NewRecorder(""),
		remote:   func() string { return "Off" },
	}
	st := backend.Status()
	if st.Version != version {
		t.Fatalf("version = %q, want %q", st.Version, version)
	}
	if st.Remote != "Off" {
		t.Fatalf("remote = %q", st.Remote)
	}
}

// TestCappedListHeight pins the v1.3.1 settings-list fix: a
// widget.List in a VBox is only one row high (its MinSize), so the
// Cameras/Users pages showed a single entry at a time. The capped
// list must be exactly rows*rowHeight + (rows-1)*separator tall for
// up to maxVisibleListRows rows, and cap there — measured against
// the real Fyne List and theme, not hand-computed constants.
func TestCappedListHeight(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()

	mkBox := func(n int) (*widget.List, *cappedList, *int) {
		count := n
		l := widget.NewList(
			func() int { return count },
			func() fyne.CanvasObject { return widget.NewLabel("row") },
			func(id widget.ListItemID, obj fyne.CanvasObject) {
				obj.(*widget.Label).SetText(fmt.Sprintf("row %d", id))
			},
		)
		box := newCappedList(l, func() int { return count })
		return l, box, &count
	}

	// One row is the list's own MinSize height.
	_, box1, _ := mkBox(1)
	probe, _, _ := mkBox(1)
	rowH := probe.MinSize().Height
	if rowH <= 0 {
		t.Fatalf("list MinSize height = %v, want one positive row", rowH)
	}
	sep := probe.Theme().Size(theme.SizeNamePadding)
	want := func(rows int) float32 {
		if rows < 1 {
			rows = 1
		}
		if rows > maxVisibleListRows {
			rows = maxVisibleListRows
		}
		return float32(rows)*rowH + float32(rows-1)*sep
	}
	if got := box1.MinSize().Height; got != want(1) {
		t.Fatalf("1 row: height = %v, want %v", got, want(1))
	}

	for _, n := range []int{0, 2, 4, 10, 11, 12} {
		_, box, _ := mkBox(n)
		if got := box.MinSize().Height; got != want(n) {
			t.Fatalf("%d items: height = %v, want %v (row %v, sep %v)", n, got, want(n), rowH, sep)
		}
	}

	// 12 items cap at the 10-row height; 4 items show all 4 rows.
	_, box4, _ := mkBox(4)
	_, box12, _ := mkBox(12)
	if box12.MinSize().Height != box4.MinSize().Height+6*rowH+6*sep {
		t.Fatalf("12 items should cap at 10 rows: %v vs 4-row %v", box12.MinSize().Height, box4.MinSize().Height)
	}

	// Refresh recomputes the height when the count changes: grow
	// from 4 to 12 items and the box must jump to the 10-row cap.
	l2, box2, count2 := mkBox(4)
	if got := box2.MinSize().Height; got != want(4) {
		t.Fatalf("4 items before refresh: height = %v, want %v", got, want(4))
	}
	*count2 = 12
	l2.Refresh()
	box2.Refresh()
	if got := box2.MinSize().Height; got != want(10) {
		t.Fatalf("12 items after refresh: height = %v, want %v (10-row cap)", got, want(10))
	}

	// The box lays out at its MinSize height in a real window: with
	// 4 items it must get at least 4 rows of height, not one.
	w := test.NewWindow(box4)
	defer w.Close()
	w.Resize(fyne.NewSize(400, 800))
	if got := box4.Size().Height; got < want(4) {
		t.Fatalf("4-item box laid out at height %v, want at least %v", got, want(4))
	}
}

// writeTestMP4 writes a minimal finalized MP4 (ftyp + mdat + moov)
// for one camera/day, so ScanLibrary lists it.
func writeTestMP4(t *testing.T, root, slug string, start time.Time) string {
	t.Helper()
	dir := filepath.Join(root, slug, start.Format("2006-01-02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, slug+"_"+start.Format("20060102_150405")+".mp4")
	var b []byte
	writeBox := func(typ string, payload []byte) {
		var hdr [8]byte
		binary.BigEndian.PutUint32(hdr[0:4], uint32(8+len(payload)))
		copy(hdr[4:8], typ)
		b = append(b, hdr[:]...)
		b = append(b, payload...)
	}
	writeBox("ftyp", []byte("isom\x00\x00\x00\x00isomiso2"))
	writeBox("mdat", []byte{0x00, 0x00, 0x00, 0x01})
	writeBox("moov", []byte{0x00, 0x00, 0x00, 0x00})
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestPlaybackMultiSelectDelete pins the v1.4 playback delete
// controls: per-row selection, delete-selected, delete-all
// (two-tap inline confirm, no dialogs), and viewers seeing no
// delete controls at all.
func TestPlaybackMultiSelectDelete(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	root := t.TempDir()
	start := time.Date(2026, 11, 13, 15, 4, 5, 0, time.Local)
	p1 := writeTestMP4(t, root, "driveway", start)
	p2 := writeTestMP4(t, root, "garage", start.Add(time.Hour))
	// A snapshot JPEG for the driveway clip: the thumbnail loader
	// must decode it.
	img := image.NewRGBA(image.Rect(0, 0, 64, 36))
	for y := 0; y < 36; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{200, 30, 30, 255})
		}
	}
	snap := filepath.Join(root, "driveway", start.Format("2006-01-02"),
		"driveway_"+start.Add(3*time.Second).Format("20060102_150405")+".jpg")
	f, err := os.Create(snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	f.Close()

	settings := engine.DefaultSettings()
	settings.RecordingsDir = root
	settings.Cameras = []engine.Camera{{Name: "Driveway"}, {Name: "Garage"}}
	n := &nvrApp{app: a, settings: settings}
	v := newPlaybackView(n)
	if len(v.all) != 2 {
		t.Fatalf("clips = %d, want 2", len(v.all))
	}
	// Thumbnail decodes from the snapshot.
	var driveClip engine.Clip
	for _, c := range v.all {
		if c.CameraSlug == "driveway" {
			driveClip = c
		}
	}
	if driveClip.SnapshotPath == "" {
		t.Fatal("driveway clip should carry a snapshot path")
	}
	if v.thumbnail(driveClip) == nil {
		t.Fatal("thumbnail should decode the snapshot JPEG")
	}
	// Row template is a clip card (thumbnail + checkbox + labels),
	// not the old single label.
	if _, ok := v.list.CreateItem().(*clipRowWidget); !ok {
		t.Fatalf("list template = %T, want *clipRowWidget", v.list.CreateItem())
	}

	// Select one clip and delete it via deleteTokens.
	tok1 := engine.ClipToken(driveClip)
	v.selected[tok1] = true
	if got := v.deleteTokens([]string{tok1}); got != 1 {
		t.Fatalf("deleteTokens = %d, want 1", got)
	}
	if _, err := os.Stat(p1); !os.IsNotExist(err) {
		t.Fatal("selected clip still on disk")
	}
	if _, err := os.Stat(snap); !os.IsNotExist(err) {
		t.Fatal("selected clip's snapshot still on disk")
	}
	if _, err := os.Stat(p2); err != nil {
		t.Fatal("unselected clip must survive")
	}
	if len(v.all) != 1 {
		t.Fatalf("clips after delete = %d, want 1", len(v.all))
	}

	// Delete all uses the two-tap pattern: first tap arms, second
	// tap deletes.
	v.deleteAllClicked()
	if len(v.all) != 1 {
		t.Fatal("first delete-all tap must only arm, not delete")
	}
	if v.delAllBtn.Text != "Tap again to delete all" {
		t.Fatalf("armed delete-all button = %q", v.delAllBtn.Text)
	}
	v.deleteAllClicked()
	if len(v.all) != 0 {
		t.Fatalf("clips after delete-all = %d, want 0", len(v.all))
	}
	if _, err := os.Stat(p2); !os.IsNotExist(err) {
		t.Fatal("remaining clip still on disk after delete-all")
	}

	// Viewers see no delete controls; admins see them again.
	v.applyRole(engine.RoleViewer)
	if v.delBtn.Visible() || v.delSelBtn.Visible() || v.delAllBtn.Visible() {
		t.Fatal("viewer must not see any delete buttons")
	}
	v.applyRole(engine.RoleAdmin)
	if !v.delBtn.Visible() || !v.delSelBtn.Visible() || !v.delAllBtn.Visible() {
		t.Fatal("admin must see all delete buttons")
	}
}
