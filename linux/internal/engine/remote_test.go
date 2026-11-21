package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testCam(name, ip string, enabled bool) Camera {
	return Camera{ID: "id-" + name, Name: name, IP: ip, Enabled: enabled}
}

func TestDecidePushersModes(t *testing.T) {
	cams := []Camera{
		testCam("Front Door", "192.168.1.10", true),
		testCam("Garage", "192.168.1.11", true),
		testCam("Off Cam", "192.168.1.12", false),
	}
	now := time.Now()

	if got := DecidePushers(RemoteModeOff, cams, map[string]bool{"front-door": true}, nil, now, 15*time.Second); len(got) != 0 {
		t.Fatalf("off mode: want none, got %v", got)
	}

	got := DecidePushers(RemoteModeAlways, cams, nil, nil, now, 15*time.Second)
	if len(got) != 2 || !got["front-door"] || !got["garage"] || got["off-cam"] {
		t.Fatalf("always mode: want front-door+garage, got %v", got)
	}

	got = DecidePushers(RemoteModeOnDemand, cams, map[string]bool{"garage": true}, nil, now, 15*time.Second)
	if len(got) != 1 || !got["garage"] {
		t.Fatalf("ondemand: want garage only, got %v", got)
	}
}

func TestDecidePushersGrace(t *testing.T) {
	cams := []Camera{testCam("Front Door", "192.168.1.10", true)}
	now := time.Now()
	last := map[string]time.Time{"front-door": now.Add(-10 * time.Second)}
	got := DecidePushers(RemoteModeOnDemand, cams, map[string]bool{}, last, now, 15*time.Second)
	if !got["front-door"] {
		t.Fatalf("within grace: pusher should stay up, got %v", got)
	}
	last["front-door"] = now.Add(-20 * time.Second)
	got = DecidePushers(RemoteModeOnDemand, cams, map[string]bool{}, last, now, 15*time.Second)
	if got["front-door"] {
		t.Fatalf("past grace: pusher should stop, got %v", got)
	}
	// Disabled camera never pushes even when demanded.
	cams[0].Enabled = false
	got = DecidePushers(RemoteModeOnDemand, cams, map[string]bool{"front-door": true}, nil, now, 15*time.Second)
	if len(got) != 0 {
		t.Fatalf("disabled camera: want none, got %v", got)
	}
}

// --- manager lifecycle with fakes ---

type fakeSource struct {
	mu           sync.Mutex
	demand       []string
	posted       [][]RelayCamera
	demandErr    error
	clipPosts    [][]RelayClip
	clipRequests []ClipRequest
	uploads      []clipUploadRec
	uploadErr    error
	snapUploads  []clipUploadRec
	postedUsers  [][]User
	clipDeletes  []ClipDelete
	deleteDone   []string
}

type clipUploadRec struct {
	token string
	size  int64
}

func (f *fakeSource) PostCameras(_ context.Context, cams []RelayCamera) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posted = append(f.posted, append([]RelayCamera{}, cams...))
	return nil
}

func (f *fakeSource) GetDemand(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.demandErr != nil {
		return nil, f.demandErr
	}
	return append([]string{}, f.demand...), nil
}

func (f *fakeSource) PostClips(_ context.Context, clips []RelayClip) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clipPosts = append(f.clipPosts, append([]RelayClip{}, clips...))
	return nil
}

func (f *fakeSource) GetClipRequests(_ context.Context) ([]ClipRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ClipRequest{}, f.clipRequests...), nil
}

func (f *fakeSource) UploadClip(_ context.Context, token string, data io.Reader, size int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.uploadErr != nil {
		return f.uploadErr
	}
	b, _ := io.ReadAll(data)
	f.uploads = append(f.uploads, clipUploadRec{token: token, size: int64(len(b))})
	return nil
}

func (f *fakeSource) UploadSnapshot(_ context.Context, token string, data io.Reader, size int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.uploadErr != nil {
		return f.uploadErr
	}
	b, _ := io.ReadAll(data)
	f.snapUploads = append(f.snapUploads, clipUploadRec{token: token, size: int64(len(b))})
	return nil
}

func (f *fakeSource) snapUploadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.snapUploads)
}

func (f *fakeSource) PostUsers(_ context.Context, users []User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.postedUsers = append(f.postedUsers, append([]User{}, users...))
	return nil
}

func (f *fakeSource) GetClipDeletes(_ context.Context) ([]ClipDelete, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ClipDelete{}, f.clipDeletes...), nil
}

func (f *fakeSource) PostClipDeleteDone(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteDone = append(f.deleteDone, id)
	return nil
}

func (f *fakeSource) setClipDeletes(d ...ClipDelete) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clipDeletes = d
}

func (f *fakeSource) setClipRequests(reqs ...ClipRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clipRequests = reqs
}

func (f *fakeSource) uploadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.uploads)
}

func (f *fakeSource) setDemand(slugs ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.demand = slugs
}

type fakePushes struct {
	mu      sync.Mutex
	running map[string]bool
	starts  map[string]int
}

func (f *fakePushes) run(ctx context.Context, cam Camera, _ Settings) error {
	f.mu.Lock()
	f.running[cam.Slug()] = true
	f.starts[cam.Slug()]++
	f.mu.Unlock()
	<-ctx.Done()
	f.mu.Lock()
	f.running[cam.Slug()] = false
	f.mu.Unlock()
	return nil
}

func (f *fakePushes) isRunning(slug string) bool {
	// supervise goroutines update asynchronously; give them a beat
	for i := 0; i < 50; i++ {
		f.mu.Lock()
		v := f.running[slug]
		f.mu.Unlock()
		if v {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func (f *fakePushes) waitStopped(slug string) bool {
	for i := 0; i < 50; i++ {
		f.mu.Lock()
		v := f.running[slug]
		f.mu.Unlock()
		if !v {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func testSettings(mode string) Settings {
	return Settings{
		RemoteEnabled: mode != RemoteModeOff,
		RemoteMode:    mode,
		Cameras: []Camera{
			{ID: "1", Name: "Front Door", IP: "192.168.1.10", Enabled: true},
			{ID: "2", Name: "Garage", IP: "192.168.1.11", Enabled: true},
		},
	}
}

func newTestManager(mode string, src *fakeSource, fp *fakePushes) *RemoteManager {
	settings := testSettings(mode)
	m := NewRemoteManager("ffmpeg", func() Settings { return settings })
	m.newSource = func(Settings) demandSource { return src }
	m.runPush = fp.run
	m.pollInterval = time.Hour // tests drive cycle() by hand
	m.usersFunc = func() []User {
		return []User{{Username: "admin", PasswordHash: "hash1", Role: RoleAdmin}}
	}
	return m
}

func TestManagerOnDemandLifecycle(t *testing.T) {
	src := &fakeSource{}
	fp := &fakePushes{running: map[string]bool{}, starts: map[string]int{}}
	m := newTestManager(RemoteModeOnDemand, src, fp)

	// No demand: nothing pushes, but the camera list is posted.
	m.cycle()
	if len(src.posted) == 0 || len(src.posted[0]) != 2 {
		t.Fatalf("camera list not posted: %v", src.posted)
	}
	if st := m.Status(); len(st.Pushing) != 0 || !st.Connected {
		t.Fatalf("no demand: want no pushers + connected, got %+v", st)
	}

	// Demand arrives: pusher starts for that slug only.
	src.setDemand("front-door")
	m.cycle()
	if !fp.isRunning("front-door") {
		t.Fatal("demand: front-door pusher did not start")
	}
	if fp.starts["garage"] != 0 {
		t.Fatal("demand: garage pusher should not start")
	}
	if st := m.Status(); len(st.Pushing) != 1 || st.Pushing[0] != "front-door" {
		t.Fatalf("status pushing wrong: %+v", st)
	}

	// Demand lapses but grace (default 15s) keeps it alive for one
	// quick cycle, then backdate lastDemand past grace -> stops.
	src.setDemand()
	m.cycle()
	if fp.starts["front-door"] == 0 || !fp.isRunning("front-door") {
		t.Fatal("grace: pusher stopped too early")
	}
	m.mu.Lock()
	m.lastDemand["front-door"] = time.Now().Add(-time.Minute)
	m.mu.Unlock()
	m.cycle()
	if !fp.waitStopped("front-door") {
		t.Fatal("after grace: pusher did not stop")
	}
	m.Stop()
}

func TestManagerAlwaysAndOff(t *testing.T) {
	src := &fakeSource{}
	fp := &fakePushes{running: map[string]bool{}, starts: map[string]int{}}
	m := newTestManager(RemoteModeAlways, src, fp)
	m.cycle()
	if !fp.isRunning("front-door") || !fp.isRunning("garage") {
		t.Fatalf("always: both pushers should run, starts=%v", fp.starts)
	}
	m.Stop()
	if !fp.waitStopped("front-door") {
		t.Fatal("always: Stop() did not stop pushers")
	}

	src2 := &fakeSource{}
	fp2 := &fakePushes{running: map[string]bool{}, starts: map[string]int{}}
	m2 := newTestManager(RemoteModeOff, src2, fp2)
	m2.cycle()
	if len(fp2.starts) != 0 || len(src2.posted) != 0 {
		t.Fatalf("off: nothing should run or post, starts=%v posted=%v", fp2.starts, src2.posted)
	}
	m2.Stop()
}

// --- relay client against a local server ---

func TestRelayClientRoundTrip(t *testing.T) {
	var gotAuth string
	var posted []RelayCamera
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/api/nvr/cameras":
			var body struct {
				Cameras []RelayCamera `json:"cameras"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			posted = body.Cameras
			_, _ = w.Write([]byte(`{"ok":true,"count":1}`))
		case "/api/nvr/demand":
			_, _ = w.Write([]byte(`{"demand":["front-door"]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	c := NewRelayClient(Settings{RemotePublishUser: "pub", RemotePublishPass: "pw"})
	c.Base = srv.URL
	ctx := context.Background()
	if err := c.PostCameras(ctx, []RelayCamera{{Slug: "front-door", Name: "Front Door"}}); err != nil {
		t.Fatal(err)
	}
	if len(posted) != 1 || posted[0].Slug != "front-door" {
		t.Fatalf("posted wrong: %v", posted)
	}
	if gotAuth == "" {
		t.Fatal("no Authorization header sent")
	}
	d, err := c.GetDemand(ctx)
	if err != nil || len(d) != 1 || d[0] != "front-door" {
		t.Fatalf("demand wrong: %v err=%v", d, err)
	}
}

func TestSettingsRemoteMigrationDefaults(t *testing.T) {
	s := DefaultSettings()
	if s.RemoteModeEffective() != RemoteModeOnDemand {
		t.Fatalf("default mode: want ondemand, got %q", s.RemoteModeEffective())
	}
	s.RemoteEnabled = false
	if s.RemoteModeEffective() != RemoteModeOff {
		t.Fatal("disabled remote must be off")
	}
	if u := s.PublishURL(Camera{Name: "Front Door"}); u == "" ||
		!strings.Contains(u, ":8554/nvr-front-door") {
		t.Fatalf("publish URL wrong: %s", u)
	}
}

// --- remote clip playback ---

func TestClipTokenRoundTrip(t *testing.T) {
	c := Clip{CameraSlug: "front-door", Start: time.Unix(1794612000, 0)}
	tok := ClipToken(c)
	if tok != "front-door/1794612000" {
		t.Fatalf("token wrong: %q", tok)
	}
	if got, ok := FindClip([]Clip{c}, tok); !ok || got.CameraSlug != "front-door" {
		t.Fatalf("FindClip(%q) = %+v, %v", tok, got, ok)
	}
	if _, ok := FindClip([]Clip{c}, "front-door/1794612001"); ok {
		t.Fatal("FindClip matched a different timestamp")
	}
	if _, ok := FindClip([]Clip{c}, "garage/1794612000"); ok {
		t.Fatal("FindClip matched a different slug")
	}
	if _, ok := FindClip([]Clip{c}, "not-a-token"); ok {
		t.Fatal("FindClip accepted a malformed token")
	}
}

func writeTestClip(t *testing.T, root, slug string, start time.Time) string {
	t.Helper()
	dir := filepath.Join(root, slug, start.Format("2006-01-02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := slug + "_" + start.Format("20060102_150405") + ".mp4"
	p := filepath.Join(dir, name)
	// A minimal finalized MP4 (ftyp + mdat + moov): ScanLibrary
	// and the upload path skip files with no moov atom, so test
	// clips must carry one to exercise the happy path.
	if err := os.WriteFile(p, minimalMP4(true), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func waitUploads(t *testing.T, src *fakeSource, want int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if src.uploadCount() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("uploads = %d, want >= %d", src.uploadCount(), want)
}

func TestManagerClipIndexAndUpload(t *testing.T) {
	root := t.TempDir()
	start := time.Date(2026, 11, 13, 15, 4, 5, 0, time.Local)
	writeTestClip(t, root, "front-door", start)
	writeTestClip(t, root, "garage", start.Add(time.Hour))

	settings := testSettings(RemoteModeOnDemand)
	settings.RecordingsDir = root
	src := &fakeSource{}
	fp := &fakePushes{running: map[string]bool{}, starts: map[string]int{}}
	m := NewRemoteManager("ffmpeg", func() Settings { return settings })
	m.newSource = func(Settings) demandSource { return src }
	m.runPush = fp.run
	m.pollInterval = time.Hour

	// No clip requests yet: the clip index is posted, nothing uploads.
	m.cycle()
	if len(src.clipPosts) == 0 || len(src.clipPosts[0]) != 2 {
		t.Fatalf("clip index not posted: %v", src.clipPosts)
	}
	if st := m.Status(); st.ClipsPosted != 2 {
		t.Fatalf("ClipsPosted = %d, want 2", st.ClipsPosted)
	}
	if src.uploadCount() != 0 {
		t.Fatalf("uploads = %d, want 0", src.uploadCount())
	}

	// A viewer asks for the front-door clip: exactly one upload,
	// of the right token, with the file's bytes.
	tok := fmt.Sprintf("front-door/%d", start.Unix())
	src.setClipRequests(ClipRequest{Token: tok, Slug: "front-door", Start: start.Unix()})
	m.cycle()
	waitUploads(t, src, 1)
	src.mu.Lock()
	rec := src.uploads[0]
	src.mu.Unlock()
	wantSize := int64(len(minimalMP4(true)))
	if rec.token != tok || rec.size != wantSize {
		t.Fatalf("upload wrong: %+v (want size %d)", rec, wantSize)
	}
	if st := m.Status(); st.ClipUploads != 1 {
		t.Fatalf("ClipUploads = %d, want 1", st.ClipUploads)
	}

	// Same request on the next cycle: no second upload (dedup).
	m.cycle()
	time.Sleep(100 * time.Millisecond)
	if src.uploadCount() != 1 {
		t.Fatalf("uploads = %d, want 1 (dedup)", src.uploadCount())
	}

	// Unknown token: ignored, no upload, no crash.
	src.setClipRequests(ClipRequest{Token: "nope/1", Slug: "nope", Start: 1})
	m.cycle()
	time.Sleep(100 * time.Millisecond)
	if src.uploadCount() != 1 {
		t.Fatalf("uploads = %d, want 1 (unknown token)", src.uploadCount())
	}
	m.Stop()
}

func TestManagerClipsOffInOffMode(t *testing.T) {
	root := t.TempDir()
	writeTestClip(t, root, "front-door", time.Now())
	settings := testSettings(RemoteModeOff)
	settings.RecordingsDir = root
	src := &fakeSource{}
	fp := &fakePushes{running: map[string]bool{}, starts: map[string]int{}}
	m := NewRemoteManager("ffmpeg", func() Settings { return settings })
	m.newSource = func(Settings) demandSource { return src }
	m.runPush = fp.run
	m.pollInterval = time.Hour
	m.cycle()
	if len(src.clipPosts) != 0 {
		t.Fatalf("off mode posted clips: %v", src.clipPosts)
	}
	m.Stop()
}

// --- user sync + clip deletes (v1.2) ---

func TestManagerUsersSync(t *testing.T) {
	src := &fakeSource{}
	fp := &fakePushes{running: map[string]bool{}, starts: map[string]int{}}
	m := newTestManager(RemoteModeOnDemand, src, fp)

	m.cycle()
	if len(src.postedUsers) != 1 || len(src.postedUsers[0]) != 1 || src.postedUsers[0][0].Username != "admin" {
		t.Fatalf("users not posted on startup: %v", src.postedUsers)
	}
	// Unchanged users: no re-post on the next cycle.
	m.cycle()
	if len(src.postedUsers) != 1 {
		t.Fatalf("unchanged users re-posted: %d posts", len(src.postedUsers))
	}
	// SyncUsersNow forces a re-post even with no change.
	m.SyncUsersNow()
	if len(src.postedUsers) != 2 {
		t.Fatalf("SyncUsersNow did not post: %d posts", len(src.postedUsers))
	}
	// A changed user list posts on the next cycle.
	users := []User{{Username: "admin", PasswordHash: "hash1", Role: RoleAdmin},
		{Username: "viewer1", PasswordHash: "hash2", Role: RoleViewer}}
	m.usersFunc = func() []User { return users }
	m.cycle()
	if len(src.postedUsers) != 3 || len(src.postedUsers[2]) != 2 {
		t.Fatalf("changed users not posted: %v", src.postedUsers)
	}
	// Older than 5 minutes: re-post even with no change.
	m.mu.Lock()
	m.lastUserPost = time.Now().Add(-6 * time.Minute)
	m.mu.Unlock()
	m.cycle()
	if len(src.postedUsers) != 4 {
		t.Fatalf("stale users not re-posted: %d posts", len(src.postedUsers))
	}
	m.Stop()
}

func TestManagerClipDeletes(t *testing.T) {
	root := t.TempDir()
	start := time.Date(2026, 11, 13, 15, 4, 5, 0, time.Local)
	p := writeTestClip(t, root, "front-door", start)

	settings := testSettings(RemoteModeOnDemand)
	settings.RecordingsDir = root
	src := &fakeSource{}
	fp := &fakePushes{running: map[string]bool{}, starts: map[string]int{}}
	m := NewRemoteManager("ffmpeg", func() Settings { return settings })
	m.newSource = func(Settings) demandSource { return src }
	m.runPush = fp.run
	m.pollInterval = time.Hour
	m.usersFunc = func() []User {
		return []User{{Username: "admin", PasswordHash: "hash1", Role: RoleAdmin}}
	}

	m.cycle()
	if len(src.clipPosts) == 0 || len(src.clipPosts[0]) != 1 {
		t.Fatalf("clip index not posted: %v", src.clipPosts)
	}
	tok := fmt.Sprintf("front-door/%d", start.Unix())
	src.setClipDeletes(ClipDelete{ID: "del-1", Token: tok})
	m.cycle()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("clip file still on disk after delete: %v", err)
	}
	if len(src.deleteDone) != 1 || src.deleteDone[0] != "del-1" {
		t.Fatalf("delete not acked: %v", src.deleteDone)
	}
	// The re-posted index no longer lists the deleted clip.
	last := src.clipPosts[len(src.clipPosts)-1]
	if len(last) != 0 {
		t.Fatalf("re-posted index still lists the clip: %v", last)
	}
	m.Stop()
}
