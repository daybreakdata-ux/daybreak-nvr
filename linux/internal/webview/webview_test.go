package webview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"daybreaknvr/internal/engine"
)

type fakeProvider struct {
	frame image.Image
	clip  engine.Clip
	path  string
}

func (f *fakeProvider) Cameras() []CameraInfo {
	return []CameraInfo{{ID: "c1", Name: "Front Door"}}
}
func (f *fakeProvider) Watch(string) func() { return func() {} }
func (f *fakeProvider) LatestFrame(string) image.Image {
	return f.frame
}
func (f *fakeProvider) Clips() []engine.Clip { return []engine.Clip{f.clip} }
func (f *fakeProvider) ClipByToken(tok string) (string, bool) {
	if tok == ClipToken(f.clip) {
		return f.path, true
	}
	return "", false
}

func (f *fakeProvider) DeleteClip(tok string) bool {
	if tok == ClipToken(f.clip) {
		return os.Remove(f.path) == nil
	}
	return false
}

// testAuthenticator accepts daybreak/secret1 as admin and
// viewer/viewpass as viewer — the same accounts the API tests use.
func testAuthenticator(username, password string) (string, bool) {
	switch {
	case username == "daybreak" && password == "secret1":
		return "admin", true
	case username == "viewer" && password == "viewpass":
		return "viewer", true
	}
	return "", false
}

// loginClient returns a client holding a live session cookie for
// the given user (via the JSON login endpoint).
func loginClient(t *testing.T, base, username, password string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: 5 * time.Second, Jar: jar}
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req, _ := http.NewRequest("POST", base+"/ui/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login %s = %d, want 200", username, resp.StatusCode)
	}
	return client
}

func TestServerEndToEnd(t *testing.T) {
	dir := t.TempDir()
	clipPath := filepath.Join(dir, "clip.mp4")
	clipBytes := []byte("fake-mp4-content-for-tests")
	if err := os.WriteFile(clipPath, clipBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 64, 36))
	for y := 0; y < 36; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{200, 40, 40, 255})
		}
	}
	fp := &fakeProvider{
		frame: img,
		path:  clipPath,
		clip: engine.Clip{
			CameraSlug: "front-door", CameraName: "Front Door",
			Path: clipPath, Start: time.Now().Add(-time.Hour), Size: int64(len(clipBytes)),
		},
	}
	s := New(fp)
	s.SetAuthenticator(testAuthenticator)
	if err := s.Start(0); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	base := "http://" + strings.Replace(s.Addr(), "[::]", "127.0.0.1", 1)

	// The login page itself is public; a bad login is a 401.
	plain := &http.Client{Timeout: 5 * time.Second}
	resp, err := plain.Get(base + "/ui/login")
	if err != nil {
		t.Fatal(err)
	}
	lb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(lb), "Sign in") {
		t.Fatalf("login page status=%d", resp.StatusCode)
	}
	badBody, _ := json.Marshal(map[string]string{"username": "daybreak", "password": "wrong"})
	badReq, _ := http.NewRequest("POST", base+"/ui/login", bytes.NewReader(badBody))
	badReq.Header.Set("Content-Type", "application/json")
	badResp, err := plain.Do(badReq)
	if err != nil {
		t.Fatal(err)
	}
	badResp.Body.Close()
	if badResp.StatusCode != 401 {
		t.Fatalf("wrong-password login = %d, want 401", badResp.StatusCode)
	}
	// No session → 401 on API, redirect to login on pages.
	noAuth := &http.Client{Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := noAuth.Get(base + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 401 {
		t.Fatalf("no-auth /api/status = %d, want 401", r.StatusCode)
	}
	r, err = noAuth.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 302 || r.Header.Get("Location") != "/ui/login" {
		t.Fatalf("no-auth / = %d loc=%q, want 302 to /ui/login", r.StatusCode, r.Header.Get("Location"))
	}
	// Old Basic auth no longer works at all.
	basicReq, _ := http.NewRequest("GET", base+"/", nil)
	basicReq.SetBasicAuth("daybreak", "secret1")
	br, err := noAuth.Do(basicReq)
	if err != nil {
		t.Fatal(err)
	}
	br.Body.Close()
	if br.StatusCode != 302 {
		t.Fatalf("Basic-auth / = %d, want redirect to login (Basic auth retired)", br.StatusCode)
	}

	client := loginClient(t, base, "daybreak", "secret1")
	get := func(path string) *http.Response {
		req, _ := http.NewRequest("GET", base+path, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		return resp
	}

	// Home page lists the camera.
	r = get("/")
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || !strings.Contains(string(body), "Front Door") {
		t.Fatalf("home page status=%d body missing camera", r.StatusCode)
	}
	// Snapshot is a JPEG.
	r = get("/snapshot?id=c1")
	sb, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if len(sb) < 2 || sb[0] != 0xFF || sb[1] != 0xD8 {
		t.Fatalf("snapshot not JPEG (%d bytes)", len(sb))
	}
	// MJPEG streams at least one frame then we hang up.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/mjpeg?id=c1", nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	resp.Body.Close()
	if !strings.Contains(string(buf[:n]), "--frame") ||
		!strings.Contains(resp.Header.Get("Content-Type"), "multipart") {
		t.Fatalf("mjpeg stream malformed: %q", buf[:n])
	}
	// Clip serves exact bytes; bogus token 404s.
	r = get(fmt.Sprintf("/clip?f=%s", ClipToken(fp.clip)))
	cb, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if string(cb) != string(clipBytes) {
		t.Fatalf("clip bytes mismatch: %q", cb)
	}
	if r := get("/clip?f=nope/1"); r.StatusCode != 404 {
		t.Fatalf("bad token status = %d", r.StatusCode)
	}
	// Camera page renders.
	r = get("/cam?id=c1")
	body, _ = io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(string(body), "Recordings") {
		t.Fatal("cam page missing recordings section")
	}
	// A viewer session can read too.
	viewer := loginClient(t, base, "viewer", "viewpass")
	vreq, _ := http.NewRequest("GET", base+"/", nil)
	vresp, err := viewer.Do(vreq)
	if err != nil {
		t.Fatal(err)
	}
	vb, _ := io.ReadAll(vresp.Body)
	vresp.Body.Close()
	if vresp.StatusCode != 200 || !strings.Contains(string(vb), "Front Door") {
		t.Fatalf("viewer home page status=%d", vresp.StatusCode)
	}
	// /api/me reports the signed-in role.
	mreq, _ := http.NewRequest("GET", base+"/api/me", nil)
	mresp, err := viewer.Do(mreq)
	if err != nil {
		t.Fatal(err)
	}
	mb, _ := io.ReadAll(mresp.Body)
	mresp.Body.Close()
	if mresp.StatusCode != 200 || !strings.Contains(string(mb), `"role":"viewer"`) {
		t.Fatalf("/api/me viewer = %d %s", mresp.StatusCode, mb)
	}
}
