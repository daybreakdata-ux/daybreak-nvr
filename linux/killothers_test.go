package main

// Tests for the v1.7 launch changes:
//   (a) kill-others — the matcher, the /proc scan, and a live kill
//       of a real spawned NVR-named process (with an unrelated
//       process proving it is left alone);
//   (b) no sign-in gate — the desktop app starts as full local
//       admin and builds the main UI with no sign-in interaction.

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"daybreaknvr/internal/engine"
)

// TestMain: when the test binary is re-executed as a helper child
// (NVR_TEST_HELPER=sleep), just sleep instead of running tests.
// The live-kill test copies the test binary to a path named
// "daybreak-nvr-linux" and runs it this way, so the kill logic
// has a real matching process to terminate.
func TestMain(m *testing.M) {
	if os.Getenv("NVR_TEST_HELPER") == "sleep" {
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestMatchNVRProcess(t *testing.T) {
	cases := []struct {
		name     string
		exe      string
		argv     []string
		wantNVR  bool
		wantHead bool
	}{
		{"installed GUI binary", "/usr/bin/daybreak-nvr-linux", []string{"/usr/bin/daybreak-nvr-linux"}, true, false},
		{"original variant binary", "/usr/bin/daybreak-nvr", []string{"/usr/bin/daybreak-nvr"}, true, false},
		{"resolved lib path", "/usr/lib/daybreak-nvr-linux/daybreak-nvr-linux", []string{"/usr/bin/daybreak-nvr-linux"}, true, false},
		{"original lib path", "/usr/lib/daybreak-nvr/daybreak-nvr", []string{"/usr/bin/daybreak-nvr"}, true, false},
		{"bundled ffmpeg (renamed)", "/usr/lib/daybreak-nvr-linux/ffmpeg", []string{"/usr/lib/daybreak-nvr-linux/ffmpeg", "-i", "rtsp://x/stream1", "-f", "segment", "-segment_time", "10"}, true, false},
		{"bundled ffmpeg (original)", "/usr/lib/daybreak-nvr/ffmpeg", []string{"/usr/lib/daybreak-nvr/ffmpeg"}, true, false},
		{"headless service", "/usr/bin/daybreak-nvr-linux", []string{"/usr/bin/daybreak-nvr-linux", "--headless"}, true, true},
		{"headless via argv0 only", "/bin/daybreak-nvr-linux", []string{"daybreak-nvr-linux", "--headless"}, true, true},
		{"argv0 basename match", "/tmp/build/daybreak-nvr", []string{"daybreak-nvr"}, true, false},
		// Must NOT match:
		{"system ffmpeg", "/usr/bin/ffmpeg", []string{"/usr/bin/ffmpeg", "-i", "in.mp4"}, false, false},
		{"local ffmpeg", "/usr/local/bin/ffmpeg", []string{"/usr/local/bin/ffmpeg"}, false, false},
		{"other binary", "/usr/bin/python3", []string{"/usr/bin/python3", "x.py"}, false, false},
		{"sleep", "/bin/sleep", []string{"sleep", "30"}, false, false},
		{"empty", "", nil, false, false},
		{"name as argument only", "/usr/bin/python3", []string{"/usr/bin/python3", "daybreak-nvr-linux"}, false, false},
		{"test binary suffix", "/tmp/go-build/daybreak-nvr-linux.test", []string{"/tmp/go-build/daybreak-nvr-linux.test"}, false, false},
	}
	for _, c := range cases {
		gotNVR, gotHead := matchNVRProcess(c.exe, c.argv)
		if gotNVR != c.wantNVR || gotHead != c.wantHead {
			t.Errorf("%s: matchNVRProcess(%q, %v) = (%v, %v), want (%v, %v)",
				c.name, c.exe, c.argv, gotNVR, gotHead, c.wantNVR, c.wantHead)
		}
	}
}

func TestFindNVRProcessesIn(t *testing.T) {
	procRoot := t.TempDir()
	mk := func(pid, exe string, cmdline string) {
		dir := filepath.Join(procRoot, pid)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if exe != "" {
			if err := os.Symlink(exe, filepath.Join(dir, "exe")); err != nil {
				t.Fatal(err)
			}
		}
		if cmdline != "" {
			if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Headless GUI instance.
	mk("123", "/usr/bin/daybreak-nvr-linux", "daybreak-nvr-linux\x00--headless\x00")
	// Orphaned bundled-ffmpeg recorder (the 2026-11-14 incident).
	mk("456", "/usr/lib/daybreak-nvr-linux/ffmpeg", "/usr/lib/daybreak-nvr-linux/ffmpeg\x00-hide_banner\x00-i\x00rtsp://x/stream1\x00-f\x00segment\x00-segment_time\x0010\x00")
	// Unrelated processes: must never be reported.
	mk("789", "/bin/sleep", "sleep\x0030\x00")
	mk("999", "", "")                                                  // kernel-thread-like: no exe, no cmdline
	mk("abc", "/usr/bin/daybreak-nvr-linux", "daybreak-nvr-linux\x00") // non-numeric dir

	found := findNVRProcessesIn(procRoot, 9999)
	if len(found) != 2 {
		t.Fatalf("found %d NVR processes, want 2: %+v", len(found), found)
	}
	byPID := map[int]nvrProc{}
	for _, p := range found {
		byPID[p.pid] = p
	}
	if p, ok := byPID[123]; !ok || !p.headless {
		t.Errorf("pid 123 (headless GUI) missing or not headless: %+v", byPID)
	}
	if p, ok := byPID[456]; !ok || p.headless {
		t.Errorf("pid 456 (ffmpeg recorder) missing or wrongly headless: %+v", byPID)
	}
	if _, ok := byPID[789]; ok {
		t.Errorf("pid 789 (sleep) must not be reported as an NVR process")
	}

	// Self is always excluded.
	foundSelf := findNVRProcessesIn(procRoot, 123)
	if len(foundSelf) != 1 || foundSelf[0].pid != 456 {
		t.Fatalf("excluding self (123) should leave only 456, got %+v", foundSelf)
	}
}

func TestKillOtherNVRInstances(t *testing.T) {
	// Safety: this test really kills matching processes on the
	// test machine. Never run it where a real NVR is already up.
	if pre := findNVRProcessesIn("/proc", os.Getpid()); len(pre) > 0 {
		t.Skipf("an NVR instance is already running on this machine (%+v); skipping live-kill test", pre)
	}

	// Helper child: a copy of the test binary named like the real
	// NVR binary, run in "sleep" mode via the env var (see
	// TestMain). It must be terminated by the kill.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helperPath := filepath.Join(t.TempDir(), "daybreak-nvr-linux")
	src, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(helperPath, os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		t.Fatal(err)
	}
	dst.Close()

	// Decoy: an unrelated process that must survive the kill.
	decoy := exec.Command("sleep", "30")
	if err := decoy.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = decoy.Process.Kill() }()

	child := exec.Command(helperPath)
	child.Env = append(os.Environ(), "NVR_TEST_HELPER=sleep")
	if err := child.Start(); err != nil {
		t.Fatalf("starting helper child: %v", err)
	}
	childDone := make(chan error, 1)
	go func() { childDone <- child.Wait() }()

	// Wait until the child is visible to the matcher.
	deadline := time.Now().Add(5 * time.Second)
	seen := false
	for time.Now().Before(deadline) {
		for _, p := range findNVRProcessesIn("/proc", os.Getpid()) {
			if p.pid == child.Process.Pid {
				seen = true
			}
		}
		if seen {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !seen {
		_ = child.Process.Kill()
		t.Fatal("helper child never appeared as an NVR process")
	}

	killOtherNVRInstances()

	select {
	case err := <-childDone:
		if err != nil {
			// Killed by a signal: Wait returns an ExitError
			// ("signal: terminated") — that IS the expected
			// outcome here.
			if !strings.Contains(err.Error(), "signal:") {
				t.Fatalf("helper child exited with unexpected error: %v", err)
			}
		}
	case <-time.After(5 * time.Second):
		_ = child.Process.Kill()
		t.Fatal("helper child still running after killOtherNVRInstances")
	}

	// The decoy and this test process must be untouched.
	if err := decoy.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("decoy sleep was killed by killOtherNVRInstances: %v", err)
	}
	if err := syscall.Kill(os.Getpid(), 0); err != nil {
		t.Fatalf("killOtherNVRInstances killed itself: %v", err)
	}
}

func TestNoGateStartup(t *testing.T) {
	// v1.7: no sign-in gate. The desktop app is built as full
	// local admin and enterMainUI runs with no Authenticate call
	// and no login screen anywhere in the path.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	a := test.NewApp()
	defer a.Quit()

	n := newNVRApp(a)
	if n.role != engine.RoleAdmin {
		t.Fatalf("new app role = %q, want %q (desktop is full local admin, no sign-in gate)",
			n.role, engine.RoleAdmin)
	}

	n.settings, _ = engine.LoadSettings()
	n.settings.PhoneEnabled = false  // hermetic: no listener
	n.settings.RemoteEnabled = false // hermetic: no relay polling
	n.ffmpeg = engine.FindFFmpeg()   // "" here; recorders no-op
	n.recorder = engine.NewRecorder(n.ffmpeg)
	n.tracker = engine.NewMotionTracker(filepath.Join(n.settings.RecordingsDir, ".motion-events.json"))
	n.snapKeeper = engine.NewSnapshotKeeper(n.settings.RecordingsDir, n.tracker)
	n.eventRec = engine.NewEventRecorder(n.ffmpeg, func() engine.Settings { return n.settings }, n.tracker)
	n.win = a.NewWindow("Daybreak NVR")
	defer func() {
		if n.remote != nil {
			n.remote.Stop()
		}
		if n.phone != nil {
			n.phone.Stop()
		}
		if n.eventRec != nil {
			n.eventRec.Stop()
		}
	}()

	// This is the whole v1.7 startup: main UI + services, with no
	// sign-in step before it.
	n.enterMainUI()

	if n.stack == nil || len(n.stack.Items) != 3 {
		t.Fatalf("main UI not built: %d tabs, want 3 (Live Preview, Playback, Devices & Settings)", len(n.stack.Items))
	}
	wantTabs := []string{"Live Preview", "Playback", "Devices & Settings"}
	for i, want := range wantTabs {
		if got := n.stack.Items[i].Text; got != want {
			t.Fatalf("tab %d = %q, want %q", i, got, want)
		}
	}
	if n.role != engine.RoleAdmin {
		t.Fatalf("role after enterMainUI = %q, want admin", n.role)
	}
}
