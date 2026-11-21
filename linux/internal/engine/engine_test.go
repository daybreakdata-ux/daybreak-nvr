package engine

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func ffmpegOrSkip(t *testing.T) string {
	t.Helper()
	ff := FindFFmpeg()
	if ff == "" {
		t.Skip("ffmpeg not available")
	}
	return ff
}

func TestTapoURLAndSlug(t *testing.T) {
	c := Camera{Name: "Front Door", IP: "192.168.1.50", Username: "nvr user", Password: "p@ss"}
	if got, want := c.MainStreamURL(), "rtsp://nvr+user:p%40ss@192.168.1.50:554/stream1"; got != want {
		t.Fatalf("main url = %q, want %q", got, want)
	}
	if got, want := c.SubStreamURL(), "rtsp://nvr+user:p%40ss@192.168.1.50:554/stream2"; got != want {
		t.Fatalf("sub url = %q, want %q", got, want)
	}
	if got, want := c.Slug(), "front-door"; got != want {
		t.Fatalf("slug = %q, want %q", got, want)
	}
	c2 := Camera{Name: "Garage", MainURL: "rtsp://example/x"}
	if c2.MainStreamURL() != "rtsp://example/x" {
		t.Fatal("explicit URL override not honoured")
	}
}

func TestPumpFromFile(t *testing.T) {
	ff := ffmpegOrSkip(t)
	dir := t.TempDir()
	video := filepath.Join(dir, "src.mp4")
	cmd := exec.Command(ff, "-y", "-f", "lavfi", "-i",
		"testsrc2=size=320x240:rate=15", "-t", "3", "-c:v", "libx264", "-pix_fmt", "yuv420p", video)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate test video: %v\n%s", err, out)
	}
	p := StartPump(ff, PumpOptions{Input: video, IsFile: true, Width: 320, FPS: 10})
	defer p.Stop()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if p.Latest() != nil && p.Frames() >= 3 {
			return // frames decoded: pump works
		}
		if p.Stopped() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("pump produced no frames (frames=%d stopped=%v err=%q)", p.Frames(), p.Stopped(), p.Err())
}

func waitForRTSP(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("mediamtx not listening on %s after %s", addr, timeout)
}

// waitForStream polls ffprobe until the RTSP path serves a stream.
// probe is the ffprobe binary (next to ffmpeg, or "ffprobe" on PATH).
func waitForStream(t *testing.T, probe, url string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd := exec.Command(probe, "-v", "error", "-rtsp_transport", "tcp",
			"-i", url, "-show_entries", "stream=codec_name", "-of", "csv")
		if out, err := cmd.CombinedOutput(); err == nil && len(out) > 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no stream on %s after %s", url, timeout)
}

// ffprobePath returns the ffprobe next to the given ffmpeg, or "ffprobe".
func ffprobePath(ffmpegBin string) string {
	cand := filepath.Join(filepath.Dir(ffmpegBin), "ffprobe")
	if _, err := os.Stat(cand); err == nil {
		return cand
	}
	return "ffprobe"
}

func TestRecorderWritesSegments(t *testing.T) {
	ff := ffmpegOrSkip(t)
	// Local RTSP server: MediaMTX (from the Daybreak CCTV project),
	// with ffmpeg publishing a test pattern into it.
	mtx := "/home/hatch/workspace/daybreak-cctv/tools/mediamtx"
	if _, err := os.Stat(mtx); err != nil {
		t.Skip("mediamtx test server not available")
	}
	cfgPath := filepath.Join(t.TempDir(), "mtx.yml")
	_ = os.WriteFile(cfgPath, []byte(
		"logLevel: warn\npaths:\n  all_others:\n    source: publisher\n"), 0o644)
	srv := exec.Command(mtx, cfgPath)
	hideWindow(srv)
	if err := srv.Start(); err != nil {
		t.Fatalf("start mediamtx: %v", err)
	}
	defer srv.Process.Kill()
	// Wait until MediaMTX is actually listening before the publisher
	// connects: on a loaded machine the server can take a moment to
	// bind, and a publisher that connects too early exits at once,
	// leaving the recorder staring at a 404 for the whole test.
	waitForRTSP(t, "127.0.0.1:8554", 10*time.Second)
	pub := exec.Command(ff, "-hide_banner", "-loglevel", "error",
		"-re", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=15",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-g", "30",
		"-f", "rtsp", "-rtsp_transport", "tcp", "rtsp://127.0.0.1:8554/testcam")
	hideWindow(pub)
	if err := pub.Start(); err != nil {
		t.Fatalf("start publisher: %v", err)
	}
	defer pub.Process.Kill()
	// Wait until the test pattern is actually published before the
	// recorder starts, otherwise the recorder's first connects 404.
	waitForStream(t, ffprobePath(ff), "rtsp://127.0.0.1:8554/testcam", 10*time.Second)
	if pub.ProcessState != nil && pub.ProcessState.Exited() {
		t.Fatal("publisher exited before the recorder started")
	}

	dir := t.TempDir()
	s := DefaultSettings()
	s.RecordingsDir = dir
	s.SegmentMinutes = 1 // 60s segments: the first segment finalizes at rollover.
	cam := Camera{ID: "t1", Name: "Test Cam", Enabled: true, Record: true,
		MainURL: "rtsp://127.0.0.1:8554/testcam"}
	s.Cameras = []Camera{cam}

	rec := NewRecorder(ff)
	rec.Sync(s)
	defer rec.StopAll()

	deadline := time.Now().Add(15 * time.Second)
	var found string
	for time.Now().Before(deadline) {
		st := rec.Status("t1")
		dayDir := filepath.Join(dir, "test-cam", time.Now().Format("2006-01-02"))
		entries, _ := os.ReadDir(dayDir)
		for _, e := range entries {
			if fi, err := e.Info(); err == nil && fi.Size() > 20000 {
				found = e.Name()
				_ = st
				break
			}
		}
		if found != "" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if st := rec.Status("t1"); !st.Recording && found == "" {
		t.Fatalf("recorder not recording (status=%+v)", st)
	}
	if found == "" {
		t.Fatal("no growing segment file written by recorder")
	}
	t.Logf("segment being written: %s", found)

	// The segment still being written has no moov atom yet, so the
	// library must not list it (an unfinalized segment cannot be
	// played). It appears once ffmpeg finalizes it at the 60s
	// segment rollover.
	if clips := ScanLibrary(dir, s.Cameras); len(clips) != 0 {
		t.Fatalf("library listed %d clips while the only segment is still recording", len(clips))
	}
	deadline = time.Now().Add(120 * time.Second)
	var clips []Clip
	for time.Now().Before(deadline) {
		clips = ScanLibrary(dir, s.Cameras)
		if len(clips) > 0 {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if len(clips) == 0 {
		t.Fatal("library scan found no clips after the first segment should have finalized")
	}
	if clips[0].CameraName != "Test Cam" {
		t.Fatalf("clip camera name = %q", clips[0].CameraName)
	}
}

func TestRetention(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "cam-a", "2026-01-01")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	oldFile := filepath.Join(old, "cam-a_20260101_000000.mp4")
	if err := os.WriteFile(oldFile, make([]byte, 5000), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-10 * 24 * time.Hour)
	_ = os.Chtimes(oldFile, past, past)
	newFile := filepath.Join(dir, "cam-a", "new.mp4")
	_ = os.WriteFile(newFile, make([]byte, 3000), 0o644)

	s := DefaultSettings()
	s.RecordingsDir = dir
	s.RetentionDays = 7
	s.MaxTotalGB = 0
	deleted, _ := RunRetention(s)
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatal("old file still present")
	}
	if _, err := os.Stat(newFile); err != nil {
		t.Fatal("new file wrongly deleted")
	}
}
