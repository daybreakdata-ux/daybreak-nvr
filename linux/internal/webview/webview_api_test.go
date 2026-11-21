package webview

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"daybreaknvr/internal/engine"
)

// fakeAdmin backs the API tests: canned settings, one camera, one
// motion event. TestCamera always succeeds.
type fakeAdmin struct {
	settings SettingsDTO
	cameras  []CameraDTO
	events   []MotionEventDTO
	users    []UserDTO
	reloaded bool
}

func (f *fakeAdmin) Version() string { return "test-1.0" }

// CameraSnapshot returns a tiny valid JPEG for the zone-picker
// endpoint test (or an error for id "missing", exercising the
// unreachable-camera path).
func (f *fakeAdmin) CameraSnapshot(id string) ([]byte, error) {
	if id == "missing" {
		return nil, fmt.Errorf("no camera with id %q", id)
	}
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
func (f *fakeAdmin) GetSettings() SettingsDTO {
	return f.settings
}
func (f *fakeAdmin) UpdateSettings(d SettingsDTO) error {
	f.settings = d
	f.Reload()
	return nil
}
func (f *fakeAdmin) GetCameras() []CameraDTO { return f.cameras }
func (f *fakeAdmin) SaveCamera(d CameraDTO) (CameraDTO, error) {
	if d.ID == "" {
		d.ID = "cam-test"
	}
	for i, c := range f.cameras {
		if c.ID == d.ID {
			f.cameras[i] = d
			f.Reload()
			return d, nil
		}
	}
	f.cameras = append(f.cameras, d)
	f.Reload()
	return d, nil
}
func (f *fakeAdmin) DeleteCamera(id string) error {
	for i, c := range f.cameras {
		if c.ID == id {
			f.cameras = append(f.cameras[:i], f.cameras[i+1:]...)
			f.Reload()
			return nil
		}
	}
	return errNotFound
}
func (f *fakeAdmin) TestCamera(c CameraDTO) (bool, string) {
	return true, "Connected — the camera answered with video."
}
func (f *fakeAdmin) MotionEvents(cameraID string, limit int) []MotionEventDTO {
	out := f.events
	if cameraID != "" {
		out = nil
		for _, e := range f.events {
			if e.CameraID == cameraID {
				out = append(out, e)
			}
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
func (f *fakeAdmin) Status() StatusDTO {
	return StatusDTO{Version: "test-1.0", Cameras: len(f.cameras), Recording: 1,
		StoredBytes: 1234, StoredFiles: 2, RecordingsDir: "/tmp/rec", Remote: "Off"}
}
func (f *fakeAdmin) Reload() { f.reloaded = true }

func (f *fakeAdmin) ListUsers() []UserDTO { return append([]UserDTO{}, f.users...) }
func (f *fakeAdmin) SaveUser(in UserInput) error {
	for i, u := range f.users {
		if u.Username == in.Username {
			f.users[i].Role = in.Role
			return nil
		}
	}
	if in.Password == "" {
		return errNotFound
	}
	f.users = append(f.users, UserDTO{Username: in.Username, Role: in.Role})
	return nil
}
func (f *fakeAdmin) DeleteUser(username string) error {
	for i, u := range f.users {
		if u.Username == username {
			if u.BuiltIn {
				return errNotFound
			}
			f.users = append(f.users[:i], f.users[i+1:]...)
			return nil
		}
	}
	return errNotFound
}

var errNotFound = &apiError{"not found"}

type apiError struct{ msg string }

func (e *apiError) Error() string { return e.msg }

func startTestServer(t *testing.T, admin Admin) (base string, stop func()) {
	t.Helper()
	fp := &fakeProvider{
		clip: engine.Clip{
			CameraSlug: "front-door", CameraName: "Front Door",
			Path:  "/tmp/rec/front-door/2026-11-13/front-door_20261113_100000.mp4",
			Start: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC), Size: 42,
		},
	}
	s := New(fp)
	s.SetAuthenticator(testAuthenticator)
	if admin != nil {
		s.SetAdmin(admin)
	}
	if err := s.Start(0); err != nil {
		t.Fatal(err)
	}
	return "http://" + strings.Replace(s.Addr(), "[::]", "127.0.0.1", 1), s.Stop
}

func TestAPIEndpoints(t *testing.T) {
	admin := &fakeAdmin{
		settings: SettingsDTO{RecordingsDir: "/tmp/rec", SegmentMinutes: 10,
			RetentionDays: 7, PreviewFPS: 10, GridLayout: 4,
			PreBufferSeconds: 15, PostBufferSeconds: 20},
		cameras: []CameraDTO{{ID: "c1", Name: "Front Door", IP: "192.168.1.50",
			Enabled: true, Record: true, RecordMode: "continuous", Sensitivity: 50}},
		events: []MotionEventDTO{{CameraID: "c1", CameraName: "Front Door",
			Start: time.Now().Add(-time.Hour), End: time.Now().Add(-50 * time.Minute)}},
		users: []UserDTO{{Username: "admin", Role: "admin", BuiltIn: true}},
	}
	base, stop := startTestServer(t, admin)
	defer stop()
	plain := &http.Client{Timeout: 5 * time.Second}
	adminClient := loginClient(t, base, "daybreak", "secret1")
	viewerClient := loginClient(t, base, "viewer", "viewpass")
	do := func(client *http.Client, method, path string, body any) (int, []byte) {
		var rdr io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, base+path, rdr)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	auth := func(method, path string, body any) (int, []byte) {
		return do(adminClient, method, path, body)
	}
	viewer := func(method, path string, body any) (int, []byte) {
		return do(viewerClient, method, path, body)
	}

	// API endpoints require a session; Basic auth is retired.
	if code, _ := do(plain, "GET", "/api/status", nil); code != 401 {
		t.Fatalf("no-auth /api/status = %d, want 401", code)
	}
	basicReq, _ := http.NewRequest("GET", base+"/api/status", nil)
	basicReq.SetBasicAuth("daybreak", "secret1")
	basicResp, err := plain.Do(basicReq)
	if err != nil {
		t.Fatal(err)
	}
	basicResp.Body.Close()
	if basicResp.StatusCode != 401 {
		t.Fatalf("Basic-auth /api/status = %d, want 401", basicResp.StatusCode)
	}

	// Status.
	code, body := auth("GET", "/api/status", nil)
	if code != 200 || !strings.Contains(string(body), "test-1.0") {
		t.Fatalf("status code=%d body=%s", code, body)
	}
	// Viewers can read status but cannot mutate.
	if code, _ := viewer("GET", "/api/status", nil); code != 200 {
		t.Fatalf("viewer status = %d, want 200", code)
	}
	if code, _ := viewer("POST", "/api/settings", SettingsDTO{RecordingsDir: "/tmp/rec"}); code != 403 {
		t.Fatalf("viewer settings POST = %d, want 403", code)
	}
	if code, _ := viewer("POST", "/api/cameras", CameraDTO{Name: "X"}); code != 403 {
		t.Fatalf("viewer camera POST = %d, want 403", code)
	}
	if code, _ := viewer("DELETE", "/api/cameras?id=c1", nil); code != 403 {
		t.Fatalf("viewer camera DELETE = %d, want 403", code)
	}
	if code, _ := viewer("DELETE", "/api/clips?token=x", nil); code != 403 {
		t.Fatalf("viewer clip DELETE = %d, want 403", code)
	}
	if code, _ := viewer("GET", "/api/users", nil); code != 403 {
		t.Fatalf("viewer users GET = %d, want 403", code)
	}

	// Users: list, add, change role, delete; built-in admin cannot
	// be deleted.
	code, body = auth("GET", "/api/users", nil)
	if code != 200 || !strings.Contains(string(body), `"built_in":true`) {
		t.Fatalf("users code=%d body=%s", code, body)
	}
	code, _ = auth("POST", "/api/users", UserInput{Username: "viewer1", Password: "pass1234", Role: "viewer"})
	if code != 200 || len(admin.users) != 2 {
		t.Fatalf("add user code=%d users=%v", code, admin.users)
	}
	code, _ = auth("DELETE", "/api/users?username=admin", nil)
	if code != 400 || len(admin.users) != 2 {
		t.Fatalf("delete built-in admin code=%d users=%v", code, admin.users)
	}
	code, _ = auth("DELETE", "/api/users?username=viewer1", nil)
	if code != 200 || len(admin.users) != 1 {
		t.Fatalf("delete user code=%d users=%v", code, admin.users)
	}

	// Cameras list, add, delete.
	code, body = auth("GET", "/api/cameras", nil)
	if code != 200 || !strings.Contains(string(body), "Front Door") {
		t.Fatalf("cameras code=%d body=%s", code, body)
	}
	code, body = auth("POST", "/api/cameras", CameraDTO{Name: "Back Yard", IP: "192.168.1.51", Enabled: true, Record: true, RecordMode: "motion", Sensitivity: 75, ZoneGrid: strings.Repeat("1", 144), HumanDetect: true, HumanConfidence: 60})
	if code != 200 || !strings.Contains(string(body), "cam-test") {
		t.Fatalf("add camera code=%d body=%s", code, body)
	}
	if !admin.reloaded {
		t.Fatal("SaveCamera did not trigger Reload")
	}
	if len(admin.cameras) != 2 {
		t.Fatalf("cameras after add = %d, want 2", len(admin.cameras))
	}
	code, _ = auth("DELETE", "/api/cameras?id=cam-test", nil)
	if code != 200 || len(admin.cameras) != 1 {
		t.Fatalf("delete camera code=%d remaining=%d", code, len(admin.cameras))
	}

	// Settings round-trip.
	code, body = auth("GET", "/api/settings", nil)
	if code != 200 || !strings.Contains(string(body), "/tmp/rec") {
		t.Fatalf("settings GET code=%d body=%s", code, body)
	}
	admin.settings.RetentionDays = 7
	code, _ = auth("POST", "/api/settings", SettingsDTO{RecordingsDir: "/tmp/rec2", SegmentMinutes: 10, RetentionDays: 14, PreviewFPS: 10, GridLayout: 9, PreBufferSeconds: 10, PostBufferSeconds: 30})
	if code != 200 || admin.settings.RetentionDays != 14 || admin.settings.RecordingsDir != "/tmp/rec2" {
		t.Fatalf("settings POST code=%d settings=%+v", code, admin.settings)
	}

	// Test connection (fake admin always succeeds).
	code, body = auth("POST", "/api/test", CameraDTO{Name: "X", IP: "192.168.1.99"})
	if code != 200 || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("test code=%d body=%s", code, body)
	}

	// Clips: filter by date matches, wrong date empty.
	code, body = auth("GET", "/api/clips?date=2026-11-13", nil)
	if code != 200 || !strings.Contains(string(body), "front-door") {
		t.Fatalf("clips code=%d body=%s", code, body)
	}
	code, body = auth("GET", "/api/clips?date=2026-11-12", nil)
	if code != 200 || strings.Contains(string(body), "front-door") {
		t.Fatalf("clips wrong-date code=%d body=%s", code, body)
	}

	// Events, filtered and unfiltered.
	code, body = auth("GET", "/api/events", nil)
	if code != 200 || !strings.Contains(string(body), "Front Door") {
		t.Fatalf("events code=%d body=%s", code, body)
	}
	code, body = auth("GET", "/api/events?camera=other", nil)
	if code != 200 || strings.Contains(string(body), "Front Door") {
		t.Fatalf("events filtered code=%d body=%s", code, body)
	}

	// Full UI page, manifest, service worker, icon.
	code, body = auth("GET", "/ui/", nil)
	if code != 200 || !strings.Contains(string(body), "Daybreak NVR") || !strings.Contains(string(body), "manifest.webmanifest") {
		t.Fatalf("ui code=%d len=%d", code, len(body))
	}
	code, body = auth("GET", "/manifest.webmanifest", nil)
	if code != 200 || !strings.Contains(string(body), "Daybreak NVR") {
		t.Fatalf("manifest code=%d body=%s", code, body)
	}
	code, body = auth("GET", "/sw.js", nil)
	if code != 200 || !strings.Contains(string(body), "service worker") {
		t.Fatalf("sw code=%d", code)
	}
	code, body = auth("GET", "/ui/icon.png", nil)
	if code != 200 || len(body) < 100 || body[1] != 'P' || body[2] != 'N' || body[3] != 'G' {
		t.Fatalf("icon code=%d len=%d", code, len(body))
	}
}

// v1.6: the zone picker's snapshot endpoint returns a JPEG for a
// known camera, a clear error for an unknown one, and refuses
// viewers (admin role required — the picker lives on the
// admin-only Settings page).
func TestAPICameraSnapshot(t *testing.T) {
	admin := &fakeAdmin{
		cameras: []CameraDTO{{ID: "c1", Name: "Front Door", IP: "192.168.1.50",
			Enabled: true, Record: true, RecordMode: "motion", Sensitivity: 50}},
	}
	base, stop := startTestServer(t, admin)
	defer stop()
	adminClient := loginClient(t, base, "daybreak", "secret1")
	viewerClient := loginClient(t, base, "viewer", "viewpass")
	get := func(client *http.Client, path string) (int, []byte, string) {
		resp, err := client.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b, resp.Header.Get("Content-Type")
	}
	// Known camera: 200, a real JPEG.
	code, body, ctype := get(adminClient, "/api/camera-snapshot?id=c1")
	if code != 200 {
		t.Fatalf("snapshot c1 = %d, want 200 (body %s)", code, body)
	}
	if ctype != "image/jpeg" {
		t.Fatalf("snapshot content-type = %q, want image/jpeg", ctype)
	}
	if len(body) < 3 || body[0] != 0xFF || body[1] != 0xD8 || body[2] != 0xFF {
		t.Fatalf("snapshot returned %d bytes that are not a JPEG", len(body))
	}
	// Unknown camera: the fake admin errors, so the endpoint
	// answers 502 with a JSON error (the UI falls back to the
	// plain grid on this).
	code, body, _ = get(adminClient, "/api/camera-snapshot?id=missing")
	if code != 502 {
		t.Fatalf("snapshot missing = %d, want 502 (body %s)", code, body)
	}
	if !strings.Contains(string(body), "no camera") {
		t.Fatalf("snapshot missing body = %s, want a clear error", body)
	}
	// Viewers cannot read camera stills from this endpoint.
	if code, _, _ := get(viewerClient, "/api/camera-snapshot?id=c1"); code != 403 {
		t.Fatalf("viewer snapshot = %d, want 403", code)
	}
	// No session at all: 401.
	plain := &http.Client{Timeout: 5 * time.Second}
	if code, _, _ := get(plain, "/api/camera-snapshot?id=c1"); code != 401 {
		t.Fatalf("no-auth snapshot = %d, want 401", code)
	}
}

func TestAPINoAdmin(t *testing.T) {
	base, stop := startTestServer(t, nil)
	defer stop()
	client := loginClient(t, base, "daybreak", "secret1")
	req, _ := http.NewRequest("GET", base+"/api/status", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 501 {
		t.Fatalf("no-admin status = %d, want 501", resp.StatusCode)
	}
	// The read-only endpoints still work without an admin.
	req, _ = http.NewRequest("GET", base+"/api/clips", nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("no-admin clips = %d, want 200", resp.StatusCode)
	}
}
