package engine

// End-to-end proof for remote viewing, run only on demand:
//
//	DAYBREAK_NVR_E2E=1 go test ./internal/engine/ -run TestRemoteE2E -v -timeout 8m
//
// It drives the real RemoteManager against the real relay on
// Machine 1: a local MediaMTX + lavfi source stands in for a Tapo
// sub-stream; a viewer-side demand request must wake the pusher,
// real HLS must come back through the relay's HTTPS front door,
// and the pusher must stop after demand expires (+ grace).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func viewerDo(t *testing.T, s Settings, method, url string, body any) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	user := s.RemoteViewerUser
	if user == "" {
		user = DefaultViewerUser
	}
	pass := s.RemoteViewerPass
	if pass == "" {
		pass = DefaultViewerPass
	}
	req.SetBasicAuth(user, pass)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	cli := &http.Client{Timeout: 15 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, data
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			t.Logf("ok: %s", what)
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func TestRemoteE2E(t *testing.T) {
	if os.Getenv("DAYBREAK_NVR_E2E") != "1" {
		t.Skip("set DAYBREAK_NVR_E2E=1 to run the live-relay proof")
	}
	ff := ffmpegOrSkip(t)
	mtx := "/home/hatch/workspace/daybreak-cctv/tools/mediamtx"
	if _, err := os.Stat(mtx); err != nil {
		t.Skip("mediamtx test server not available")
	}
	// Local source: MediaMTX on :8555 + lavfi publisher.
	cfgPath := filepath.Join(t.TempDir(), "mtx.yml")
	_ = os.WriteFile(cfgPath, []byte(
		"logLevel: warn\nrtspAddress: :8555\npaths:\n  all_others:\n    source: publisher\n"), 0o644)
	srv := exec.Command(mtx, cfgPath)
	hideWindow(srv)
	if err := srv.Start(); err != nil {
		t.Fatalf("start mediamtx: %v", err)
	}
	defer srv.Process.Kill()
	pub := exec.Command(ff, "-hide_banner", "-loglevel", "error",
		"-re", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=15",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-g", "30",
		"-f", "rtsp", "-rtsp_transport", "tcp", "rtsp://127.0.0.1:8555/e2e")
	hideWindow(pub)
	if err := pub.Start(); err != nil {
		t.Fatalf("start publisher: %v", err)
	}
	defer pub.Process.Kill()
	time.Sleep(2 * time.Second)

	s := DefaultSettings()
	s.RemoteEnabled = true
	s.RemoteMode = RemoteModeOnDemand
	s.Cameras = []Camera{{
		ID: "e2e", Name: "E2E Test", Enabled: true,
		SubURL: "rtsp://127.0.0.1:8555/e2e",
	}}
	relayBase := "https://" + s.RemoteRelayHost

	m := NewRemoteManager(ff, func() Settings { return s })
	m.pollInterval = 500 * time.Millisecond
	defer func() {
		// Remove the test camera from the relay state.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = NewRelayClient(s).PostCameras(ctx, []RelayCamera{})
		m.Stop()
	}()
	m.Start()

	// 1. Camera list reaches the relay; no demand -> no pusher.
	waitFor(t, "camera list on relay", 20*time.Second, func() bool {
		code, data := viewerDo(t, s, "GET", relayBase+"/api/nvr/cameras", nil)
		return code == 200 && strings.Contains(string(data), `"e2e-test"`)
	})
	time.Sleep(2 * time.Second)
	if st := m.Status(); len(st.Pushing) != 0 {
		t.Fatalf("pusher running without demand: %+v", st)
	}
	t.Log("ok: no pusher without demand")

	// 2. Viewer demand wakes the pusher.
	code, _ := viewerDo(t, s, "POST", relayBase+"/api/nvr/request",
		map[string]string{"slug": "e2e-test"})
	if code != 200 {
		t.Fatalf("demand request: HTTP %d", code)
	}
	waitFor(t, "pusher started after demand", 20*time.Second, func() bool {
		for _, p := range m.Status().Pushing {
			if p == "e2e-test" {
				return true
			}
		}
		return false
	})

	// 3. Real HLS comes back through the relay's HTTPS door.
	var segURL string
	waitFor(t, "HLS playlist + segment via relay", 45*time.Second, func() bool {
		code, data := viewerDo(t, s, "GET", relayBase+"/nvr-e2e-test/index.m3u8", nil)
		if code != 200 {
			return false
		}
		var sub string
		for _, l := range strings.Split(string(data), "\n") {
			if l != "" && !strings.HasPrefix(l, "#") {
				sub = l
				break
			}
		}
		if sub == "" {
			return false
		}
		code, data = viewerDo(t, s, "GET", relayBase+"/nvr-e2e-test/"+sub, nil)
		if code != 200 {
			return false
		}
		for _, l := range strings.Split(string(data), "\n") {
			if l != "" && !strings.HasPrefix(l, "#") {
				segURL = relayBase + "/nvr-e2e-test/" + l
				return true
			}
		}
		return false
	})
	code, seg := viewerDo(t, s, "GET", segURL, nil)
	if code != 200 || len(seg) < 10000 || seg[0] != 0x47 {
		t.Fatalf("segment fetch: HTTP %d, %d bytes", code, len(seg))
	}
	t.Logf("ok: TS segment through relay: %d bytes", len(seg))

	// 4. Demand expires (45s TTL) + grace (15s): pusher stops.
	fmt.Println("waiting for demand expiry + grace (~70s)…")
	waitFor(t, "pusher stopped after demand expiry", 110*time.Second, func() bool {
		return len(m.Status().Pushing) == 0
	})
}
