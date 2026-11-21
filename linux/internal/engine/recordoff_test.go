package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- record modes (v1.5.2) ---

func TestRecordModeValues(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"motion", RecordMotion},
		{"continuous", RecordContinuous},
		{"off", RecordOff},
		{"", RecordContinuous},      // empty = historical default
		{"bogus", RecordContinuous}, // unknown = continuous, never Off
	}
	for _, c := range cases {
		cam := Camera{RecordMode: c.in}
		if got := cam.Mode(); got != c.want {
			t.Errorf("Mode() for %q = %q, want %q", c.in, got, c.want)
		}
	}
	if !ValidRecordMode(RecordContinuous) || !ValidRecordMode(RecordMotion) || !ValidRecordMode(RecordOff) {
		t.Error("the three known modes should be valid")
	}
	if ValidRecordMode("") || ValidRecordMode("bogus") {
		t.Error("empty/unknown modes should not be valid")
	}
}

// BuildCamera round-trips the Off mode through the editor mapping,
// and a directly-constructed camera with an unknown mode is
// rejected by Validate.
func TestBuildCameraOffRoundTrip(t *testing.T) {
	base := Camera{ID: "c1", Name: "Yard", IP: "192.168.1.80", Enabled: true, Record: true}
	got, err := BuildCamera(base, EditorInput{
		Name: "Yard", IP: "192.168.1.80", Enabled: true, Record: true,
		RecordMode: RecordOff, Sensitivity: 50,
	})
	if err != nil {
		t.Fatalf("BuildCamera: %v", err)
	}
	if got.Mode() != RecordOff {
		t.Fatalf("mode = %q, want off", got.Mode())
	}
	if got.RecordMode != "off" {
		t.Fatalf("stored record_mode = %q, want \"off\"", got.RecordMode)
	}
	in := EditorInputFromCamera(got)
	if in.RecordMode != RecordOff {
		t.Errorf("prefill mode = %q, want off", in.RecordMode)
	}
	// Switching back to Motion resumes the recording mode.
	got, err = BuildCamera(got, EditorInput{
		Name: "Yard", IP: "192.168.1.80", Enabled: true, Record: true,
		RecordMode: RecordMotion, Sensitivity: 50,
	})
	if err != nil {
		t.Fatalf("BuildCamera: %v", err)
	}
	if got.Mode() != RecordMotion {
		t.Errorf("mode after switching back = %q, want motion", got.Mode())
	}
	// An unknown mode is rejected, never stored.
	if _, err := BuildCamera(base, EditorInput{
		Name: "Yard", IP: "192.168.1.80", RecordMode: "bogus",
	}); err == nil {
		t.Error("unknown record mode should fail validation")
	}
	// A directly-constructed camera with a bogus mode fails
	// Validate too.
	bad := Camera{Name: "Yard", IP: "192.168.1.80", RecordMode: "bogus"}
	if err := bad.Validate(); err == nil {
		t.Error("Validate should reject an unknown record mode")
	}
}

// --- recorder: an Off camera never gets a recorder ---

// fakeFFmpeg writes a shell script that logs each invocation to
// logPath and exits 0, and returns its path.
func fakeFFmpeg(t *testing.T, logPath string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\necho invoked >> " + logPath + "\nexit 0\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func countInvocations(t *testing.T, logPath string) int {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) == "" {
		return 0
	}
	return len(strings.Split(strings.TrimSpace(string(b)), "\n"))
}

func countMP4s(t *testing.T, root string) int {
	t.Helper()
	n := 0
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".mp4") {
			n++
		}
		return nil
	})
	return n
}

// TestRecorderSkipsOffCamera is the v1.5.2 core behaviour: an Off
// camera writes no recording files and runs no recorder.
// v1.6: the segment recorder is Continuous-only (Motion and
// Person-only are the EventRecorder's job, and it only records
// while an event is open), so switching Off -> Motion must NOT
// start this recorder — switching Off -> Continuous must.
func TestRecorderSkipsOffCamera(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "ffmpeg.log")
	ff := fakeFFmpeg(t, logPath)
	rec := NewRecorder(ff)
	defer rec.StopAll()

	cam := Camera{ID: "off1", Name: "Gate", Enabled: true, Record: true,
		RecordMode: RecordOff, MainURL: "rtsp://192.168.1.99/stream1"}
	s := DefaultSettings()
	s.RecordingsDir = root
	s.Cameras = []Camera{cam}

	// Off: Sync must not invoke ffmpeg at all.
	rec.Sync(s)
	time.Sleep(1500 * time.Millisecond)
	if n := countInvocations(t, logPath); n != 0 {
		t.Fatalf("Off camera invoked ffmpeg %d times, want 0", n)
	}
	if n := countMP4s(t, root); n != 0 {
		t.Fatalf("Off camera wrote %d mp4 files, want 0", n)
	}
	if st := rec.Status(cam.ID); st.Recording || st.Restarts != 0 {
		t.Fatalf("Off camera recorder status = %+v, want idle", st)
	}

	// Switch the same camera to Motion and Sync again: the
	// segment recorder must stay idle (Motion recording is the
	// EventRecorder's job; with no event open nothing records).
	cam.RecordMode = RecordMotion
	s.Cameras = []Camera{cam}
	rec.Sync(s)
	time.Sleep(1500 * time.Millisecond)
	if n := countInvocations(t, logPath); n != 0 {
		t.Fatalf("switching Off -> Motion invoked ffmpeg %d times, want 0 (Motion is the EventRecorder's job)", n)
	}

	// Switch the same camera to Continuous and Sync again: the
	// segment recorder must start (recording resumes, no
	// backfill of the Off period — there is nothing to backfill).
	cam.RecordMode = RecordContinuous
	s.Cameras = []Camera{cam}
	rec.Sync(s)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if countInvocations(t, logPath) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if n := countInvocations(t, logPath); n == 0 {
		t.Fatal("switching Off -> Continuous did not start the recorder")
	}
}

// TestOffModePersistsAcrossSaveLoad: the Off record mode (and the
// other modes) survive a save/load round-trip, and an unknown
// stored mode still falls back to Continuous.
func TestOffModePersistsAcrossSaveLoad(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	cfgDir := filepath.Dir(ConfigPath())
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	orig := Settings{
		RecordingsDir: "/tmp/rec",
		Cameras: []Camera{
			{ID: "a", Name: "Gate", IP: "192.168.1.10", Enabled: true, Record: true, RecordMode: "off"},
			{ID: "b", Name: "Yard", IP: "192.168.1.11", Enabled: true, Record: true, RecordMode: "motion"},
			{ID: "c", Name: "Barn", IP: "192.168.1.12", Enabled: true, Record: true, RecordMode: "bogus"},
		},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath(), data, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, _ := LoadSettings()
	if loaded.Cameras[0].Mode() != RecordOff {
		t.Errorf("Off camera loaded as %q, want off", loaded.Cameras[0].Mode())
	}
	if loaded.Cameras[1].Mode() != RecordMotion {
		t.Errorf("Motion camera loaded as %q, want motion", loaded.Cameras[1].Mode())
	}
	if loaded.Cameras[2].Mode() != RecordContinuous {
		t.Errorf("unknown-mode camera loaded as %q, want continuous", loaded.Cameras[2].Mode())
	}
	// Persist and reload: the Off mode sticks.
	if err := loaded.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, _ := LoadSettings()
	if reloaded.Cameras[0].Mode() != RecordOff {
		t.Errorf("Off camera after save/load = %q, want off", reloaded.Cameras[0].Mode())
	}
	if reloaded.Cameras[0].RecordMode != "off" {
		t.Errorf("stored record_mode = %q, want \"off\"", reloaded.Cameras[0].RecordMode)
	}
}
