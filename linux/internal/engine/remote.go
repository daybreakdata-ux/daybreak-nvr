package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"daybreaknvr/internal/startlog"
)

// DecidePushers is the pure heart of remote viewing: given the
// mode, the cameras, the set of slugs viewers are demanding right
// now, and when each slug was last demanded, it returns the set of
// slugs that should have a live pusher to the relay.
//
//   - off: nothing.
//   - always: every enabled camera with a sub-stream.
//   - ondemand: demanded cameras, plus any camera whose demand
//     lapsed within the grace window (so a viewer refreshing the
//     page doesn't flap the stream off and on).
func DecidePushers(mode string, cams []Camera, demanded map[string]bool,
	lastDemand map[string]time.Time, now time.Time, grace time.Duration) map[string]bool {
	want := map[string]bool{}
	switch mode {
	case RemoteModeAlways:
		for _, c := range cams {
			if c.Enabled && c.SubStreamURL() != "" {
				want[c.Slug()] = true
			}
		}
	case RemoteModeOnDemand:
		for _, c := range cams {
			if !c.Enabled || c.SubStreamURL() == "" {
				continue
			}
			slug := c.Slug()
			if demanded[slug] {
				want[slug] = true
				continue
			}
			if t, ok := lastDemand[slug]; ok && now.Sub(t) < grace {
				want[slug] = true
			}
		}
	}
	return want
}

// RemoteStatus is a snapshot of the remote-viewing machinery for
// the settings UI.
type RemoteStatus struct {
	Mode      string
	Connected bool
	LastPoll  time.Time
	LastError string
	Pushing   []string // slugs currently being pushed to the relay
	// Playback: how many clips the relay knows about, how many the
	// NVR has uploaded for viewers, and what is uploading now.
	ClipsPosted int
	ClipUploads int
	Uploading   []string // clip tokens currently uploading
}

// clipUpload records a finished clip upload so the same file is not
// sent twice. uploadedAt bounds the dedup: the relay drops clip
// files after about a day, so anything older is sent again.
type clipUpload struct {
	size       int64
	modUnix    int64
	uploadedAt time.Time
}

// pushRunner performs ONE push attempt for a camera and returns
// when the attempt ends. The manager supervises retries. Tests
// substitute a fake.
type pushRunner func(ctx context.Context, cam Camera, s Settings) error

// demandSource abstracts the relay signaling API (tests fake it).
type demandSource interface {
	PostCameras(ctx context.Context, cams []RelayCamera) error
	GetDemand(ctx context.Context) ([]string, error)
	PostClips(ctx context.Context, clips []RelayClip) error
	GetClipRequests(ctx context.Context) ([]ClipRequest, error)
	UploadClip(ctx context.Context, token string, data io.Reader, size int64) error
	UploadSnapshot(ctx context.Context, token string, data io.Reader, size int64) error
	PostUsers(ctx context.Context, users []User) error
	GetClipDeletes(ctx context.Context) ([]ClipDelete, error)
	PostClipDeleteDone(ctx context.Context, id string) error
}

// RemoteManager keeps the relay's camera list fresh, polls viewer
// demand, and supervises one ffmpeg pusher per camera that should
// be streaming to the relay. It never touches recording, motion or
// the local preview pumps.
type RemoteManager struct {
	ffmpeg      string
	getSettings func() Settings

	// Injectable for tests.
	newSource    func(s Settings) demandSource
	runPush      pushRunner
	pollInterval time.Duration
	grace        time.Duration

	mu          sync.Mutex
	cancels     map[string]context.CancelFunc // by slug
	sigs        map[string]string             // by slug
	lastDemand  map[string]time.Time          // by slug
	status      RemoteStatus
	lastPost    time.Time
	lastPostSig string
	// User sync (v1.2): the relay signs remote viewers in against
	// these accounts, so they are posted on startup, on change
	// (SyncUsersNow) and at least every 5 minutes.
	usersFunc    func() []User
	lastUserPost time.Time
	lastUserSig  string
	// Clip playback (remote): the recordings index the relay has,
	// and which clip files have already been uploaded for viewers.
	clipCache    []Clip
	clipCacheAt  time.Time
	lastClipPost time.Time
	lastClipSig  string
	uploaded     map[string]clipUpload // by clip token
	uploading    map[string]bool       // by clip token
	// Snapshot uploads (v1.4, eager): snapshots are tiny, so they
	// go up as soon as they exist instead of waiting for a viewer
	// to ask for their clip. Same dedup shape as clip uploads.
	snapUploaded  map[string]clipUpload // by clip token
	snapUploading map[string]bool       // by clip token
	snapUploads   int
	// notFinalizedLogged tracks clip tokens already logged as
	// unfinalized, so a pending request for a segment with no
	// moov atom logs once instead of every poll cycle.
	notFinalizedLogged map[string]bool // by clip token
	clipUploads        int

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
	started  bool
}

// NewRemoteManager creates the manager. getSettings is polled each
// cycle, so settings edits apply without a restart.
func NewRemoteManager(ffmpeg string, getSettings func() Settings) *RemoteManager {
	m := &RemoteManager{
		ffmpeg:       ffmpeg,
		getSettings:  getSettings,
		pollInterval: 3 * time.Second,
		grace:        15 * time.Second,
		cancels:      map[string]context.CancelFunc{},
		sigs:         map[string]string{},
		lastDemand:   map[string]time.Time{},
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
	m.newSource = func(s Settings) demandSource { return NewRelayClient(s) }
	m.runPush = m.ffmpegPush
	m.usersFunc = EffectiveUsers
	m.uploaded = map[string]clipUpload{}
	m.uploading = map[string]bool{}
	m.snapUploaded = map[string]clipUpload{}
	m.snapUploading = map[string]bool{}
	m.notFinalizedLogged = map[string]bool{}
	return m
}

// getUsers returns the accounts to sync to the relay.
func (m *RemoteManager) getUsers() []User {
	if m.usersFunc != nil {
		return m.usersFunc()
	}
	return EffectiveUsers()
}

// syncUsers posts the user list to the relay when it changed or
// is older than 5 minutes. A failure leaves the signature alone
// so the next cycle retries.
func (m *RemoteManager) syncUsers(ctx context.Context, src demandSource) error {
	users := m.getUsers()
	sig := UsersSig(users)
	m.mu.Lock()
	need := sig != m.lastUserSig || time.Since(m.lastUserPost) > 5*time.Minute
	m.mu.Unlock()
	if !need {
		return nil
	}
	if err := src.PostUsers(ctx, users); err != nil {
		return err
	}
	m.mu.Lock()
	m.lastUserPost, m.lastUserSig = time.Now(), sig
	m.mu.Unlock()
	return nil
}

// SyncUsersNow forces an immediate user sync (called after a user
// is added, changed or deleted in the GUI or web UI).
func (m *RemoteManager) SyncUsersNow() {
	s := m.getSettings()
	src := m.newSource(s)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	m.mu.Lock()
	m.lastUserSig = ""
	m.mu.Unlock()
	_ = m.syncUsers(ctx, src)
}

// Start launches the polling loop.
func (m *RemoteManager) Start() {
	m.mu.Lock()
	m.started = true
	m.mu.Unlock()
	go m.loop()
}

// Stop halts polling and every pusher.
func (m *RemoteManager) Stop() {
	m.stopOnce.Do(func() { close(m.stopCh) })
	m.mu.Lock()
	started := m.started
	m.mu.Unlock()
	if started {
		<-m.doneCh
	}
	m.mu.Lock()
	for slug, cancel := range m.cancels {
		cancel()
		delete(m.cancels, slug)
	}
	m.mu.Unlock()
}

// Status returns the latest snapshot.
func (m *RemoteManager) Status() RemoteStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.status
	st.Pushing = append([]string{}, m.status.Pushing...)
	return st
}

func (m *RemoteManager) loop() {
	defer close(m.doneCh)
	defer func() {
		if r := recover(); r != nil {
			startlog.LogPanic("remote manager loop", r)
			m.setConnectivity(false, fmt.Sprintf("panic: %v", r), time.Now())
		}
	}()
	t := time.NewTicker(m.pollInterval)
	defer t.Stop()
	m.cycle() // immediate first cycle
	for {
		select {
		case <-m.stopCh:
			return
		case <-t.C:
			// A panicking cycle must not kill the loop.
			func() {
				defer func() {
					if r := recover(); r != nil {
						startlog.LogPanic("remote manager cycle", r)
					}
				}()
				m.cycle()
			}()
		}
	}
}

// cycle runs one poll + reconcile. It is the unit the tests drive.
func (m *RemoteManager) cycle() {
	s := m.getSettings()
	mode := s.RemoteModeEffective()
	now := time.Now()

	m.mu.Lock()
	m.status.Mode = mode
	m.mu.Unlock()

	if mode == RemoteModeOff {
		// Remote viewing is off, but the user list is still
		// synced: the relay needs it the moment viewing is
		// turned back on, and the viewer signs in against it.
		src := m.newSource(s)
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		_ = m.syncUsers(ctx, src)
		cancel()
		m.reconcile(s, map[string]bool{})
		m.setConnectivity(false, "", now)
		return
	}

	src := m.newSource(s)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	var firstErr error
	// Keep the relay's user list fresh (same cadence as cameras).
	if err := m.syncUsers(ctx, src); err != nil {
		firstErr = err
	}

	// Keep the relay's camera list fresh: on change, and at least
	// every 5 minutes.
	relayCams := s.RelayCameras()
	sig := relayCamsSig(relayCams)
	m.mu.Lock()
	needPost := sig != m.lastPostSig || now.Sub(m.lastPost) > 5*time.Minute
	m.mu.Unlock()
	if needPost {
		if err := src.PostCameras(ctx, relayCams); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		} else {
			m.mu.Lock()
			m.lastPost, m.lastPostSig = now, sig
			m.mu.Unlock()
		}
	}

	demanded := map[string]bool{}
	if mode == RemoteModeOnDemand {
		slugs, err := src.GetDemand(ctx)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
		} else {
			for _, slug := range slugs {
				demanded[slug] = true
			}
		}
	}

	if firstErr != nil {
		m.setConnectivity(false, trimErr(firstErr.Error()), now)
		// Keep existing pushers as they are on a transient API
		// failure; only refresh lastDemand for what we know.
		if len(demanded) == 0 {
			return
		}
	} else {
		m.setConnectivity(true, "", now)
	}

	m.mu.Lock()
	for slug := range demanded {
		m.lastDemand[slug] = now
	}
	last := make(map[string]time.Time, len(m.lastDemand))
	for k, v := range m.lastDemand {
		last[k] = v
	}
	m.mu.Unlock()

	want := DecidePushers(mode, s.Cameras, demanded, last, now, m.grace)
	m.reconcile(s, want)

	// Playback: keep the relay's clip index fresh and upload any
	// clips viewers are waiting on. Never on a failed poll cycle,
	// and never on the short poll context (uploads are slow).
	if firstErr == nil {
		m.handleClips(s, src)
	}
}

func (m *RemoteManager) setConnectivity(ok bool, errText string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Connected = ok
	m.status.LastPoll = now
	m.status.LastError = errText
}

// ---------- remote clip playback ----------

// cachedLibrary returns the recordings scan, rescanning at most
// once a minute so the 3s poll cycle does not walk the disk.
func (m *RemoteManager) cachedLibrary(s Settings) []Clip {
	m.mu.Lock()
	defer m.mu.Unlock()
	if time.Since(m.clipCacheAt) < time.Minute && m.clipCache != nil {
		return m.clipCache
	}
	m.clipCache = ScanLibrary(s.RecordingsDir, s.Cameras)
	m.clipCacheAt = time.Now()
	return m.clipCache
}

func relayClipIndex(clips []Clip) []RelayClip {
	out := make([]RelayClip, 0, len(clips))
	for _, c := range clips {
		if len(out) >= 500 {
			break
		}
		out = append(out, RelayClip{Slug: c.CameraSlug, Name: c.CameraName,
			Start: c.Start.Unix(), Size: c.Size, Duration: c.DurationSec, Kind: c.Kind})
	}
	return out
}

func clipIndexSig(clips []RelayClip) string {
	var parts []string
	for _, c := range clips {
		parts = append(parts, fmt.Sprintf("%s/%d=%d,%.1f,%s", c.Slug, c.Start, c.Size, c.Duration, c.Kind))
	}
	return strings.Join(parts, ",")
}

// DropUpload clears the upload dedup for one clip token (clip +
// snapshot) so a re-recorded clip with the same token uploads
// again. Used after a local delete.
func (m *RemoteManager) DropUpload(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.uploaded, token)
	delete(m.snapUploaded, token)
	m.clipCacheAt = time.Time{}
	m.lastClipSig = ""
}

// handleClips posts the recordings index and starts uploads for
// clips viewers asked for. It runs on the poll cycle but never
// blocks it: uploads happen in background goroutines.
func (m *RemoteManager) handleClips(s Settings, src demandSource) {
	now := time.Now()
	clips := m.cachedLibrary(s)
	index := relayClipIndex(clips)
	sig := clipIndexSig(index)

	m.mu.Lock()
	needPost := sig != m.lastClipSig || now.Sub(m.lastClipPost) > 5*time.Minute
	m.mu.Unlock()
	if needPost {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		if err := src.PostClips(ctx, index); err == nil {
			m.mu.Lock()
			m.lastClipPost, m.lastClipSig = now, sig
			m.status.ClipsPosted = len(index)
			m.mu.Unlock()
		}
		cancel()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	reqs, err := src.GetClipRequests(ctx)
	cancel()
	if err != nil {
		return
	}
	for _, req := range reqs {
		m.maybeUploadClip(s, req)
	}

	// Snapshots upload eagerly (v1.4): they are tiny, and the
	// viewer shows them in the playback list without asking.
	m.uploadSnapshotsEager(s, src)

	// Clip deletes: a viewer deleted clips in the remote viewer;
	// delete them from disk here, re-post the index, and ack.
	m.handleClipDeletes(s, src)
}

// handleClipDeletes carries out pending relay clip deletes:
// delete the file, drop any upload dedup for that token, re-post
// the clip index so the viewer stops listing it, and ack the
// delete so the relay stops reporting it.
func (m *RemoteManager) handleClipDeletes(s Settings, src demandSource) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	deletes, err := src.GetClipDeletes(ctx)
	cancel()
	if err != nil || len(deletes) == 0 {
		return
	}
	deletedAny := false
	for _, d := range deletes {
		token := d.DeleteToken()
		if token == "" {
			continue
		}
		if DeleteClipFile(s.RecordingsDir, s.Cameras, token) {
			deletedAny = true
		}
		m.mu.Lock()
		delete(m.uploaded, token)
		delete(m.snapUploaded, token)
		m.clipCacheAt = time.Time{}
		m.lastClipSig = ""
		m.mu.Unlock()
		if d.ID != "" {
			ctx2, cancel2 := context.WithTimeout(context.Background(), 8*time.Second)
			_ = src.PostClipDeleteDone(ctx2, d.ID)
			cancel2()
		}
	}
	if deletedAny {
		clips := m.cachedLibrary(s)
		index := relayClipIndex(clips)
		ctx3, cancel3 := context.WithTimeout(context.Background(), 8*time.Second)
		if err := src.PostClips(ctx3, index); err == nil {
			m.mu.Lock()
			m.lastClipPost, m.lastClipSig = time.Now(), clipIndexSig(index)
			m.status.ClipsPosted = len(index)
			m.mu.Unlock()
		}
		cancel3()
	}
}

// maybeUploadClip starts a background upload for one requested clip
// unless it is already uploading or was uploaded recently with the
// same file contents.
func (m *RemoteManager) maybeUploadClip(s Settings, req ClipRequest) {
	token := req.Token
	clip, ok := FindClip(m.cachedLibrary(s), token)
	if !ok {
		// Cache may be stale (clip recorded since the last scan):
		// one fresh scan before giving up on this request.
		m.mu.Lock()
		m.clipCacheAt = time.Time{}
		m.mu.Unlock()
		clip, ok = FindClip(m.cachedLibrary(s), token)
		if !ok {
			return
		}
	}
	st, err := os.Stat(clip.Path)
	if err != nil {
		return
	}
	if !hasMoov(clip.Path) {
		// Defense in depth (ScanLibrary already filters these):
		// a segment with no moov atom is still recording or was
		// orphaned when the app stopped mid-segment, and no
		// browser can play it. Do not upload it; leave the
		// request pending — once ffmpeg finalizes the segment it
		// will upload on a later cycle. Log once per token.
		m.mu.Lock()
		if m.notFinalizedLogged == nil {
			m.notFinalizedLogged = map[string]bool{}
		}
		logIt := !m.notFinalizedLogged[token]
		m.notFinalizedLogged[token] = true
		m.mu.Unlock()
		if logIt {
			startlog.Stage("remote: clip %s not finalized, waiting (no moov atom)", token)
		}
		return
	}
	m.mu.Lock()
	if m.uploading[token] {
		m.mu.Unlock()
		return
	}
	if prev, done := m.uploaded[token]; done &&
		prev.size == st.Size() && prev.modUnix == st.ModTime().Unix() &&
		time.Since(prev.uploadedAt) < 20*time.Hour {
		m.mu.Unlock()
		return
	}
	m.uploading[token] = true
	m.mu.Unlock()

	go m.uploadClip(s, token, clip.Path, st.Size(), st.ModTime().Unix())
}

// uploadClip sends one clip file to the relay. It runs on its own
// long-lived context, never the poll cycle's.
func (m *RemoteManager) uploadClip(s Settings, token, path string, size, modUnix int64) {
	defer func() {
		if r := recover(); r != nil {
			startlog.LogPanic("remote clip upload "+token, r)
		}
		m.mu.Lock()
		delete(m.uploading, token)
		uploading := make([]string, 0, len(m.uploading))
		for t := range m.uploading {
			uploading = append(uploading, t)
		}
		sort.Strings(uploading)
		m.status.Uploading = uploading
		m.mu.Unlock()
	}()
	m.mu.Lock()
	uploading := make([]string, 0, len(m.uploading))
	for t := range m.uploading {
		uploading = append(uploading, t)
	}
	sort.Strings(uploading)
	m.status.Uploading = uploading
	m.mu.Unlock()

	// Upload a faststart remux when the clip needs one: the
	// recorder writes moov at the END of the file, so a browser
	// must fetch the tail of a ~90 MB file before it can start
	// playing. The remux moves moov to the front (stream copy,
	// no re-encode); on any failure the original file is uploaded
	// instead. Dedup stays keyed on the source file (size/modUnix
	// params), which is unchanged by the remux.
	uploadPath, uploadSize := path, size
	if fastPath := m.remuxFaststart(path); fastPath != "" {
		defer os.Remove(fastPath)
		if st, err := os.Stat(fastPath); err == nil {
			uploadPath, uploadSize = fastPath, st.Size()
		}
	}
	f, err := os.Open(uploadPath)
	if err != nil {
		return
	}
	defer f.Close()
	src := m.newSource(s)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := src.UploadClip(ctx, token, f, uploadSize); err != nil {
		return
	}
	m.mu.Lock()
	m.uploaded[token] = clipUpload{size: size, modUnix: modUnix, uploadedAt: time.Now()}
	delete(m.notFinalizedLogged, token)
	m.clipUploads++
	m.status.ClipUploads = m.clipUploads
	m.mu.Unlock()
}

// uploadSnapshotsEager scans the library (newest first) for local
// snapshot files whose clip token has not been uploaded yet, and
// starts background uploads for a bounded number per cycle (4).
// Dedup is by token + snapshot size + modtime, exactly like clip
// uploads; a snapshot whose clip was deleted never appears here
// because ScanLibrary only returns clips that still exist.
func (m *RemoteManager) uploadSnapshotsEager(s Settings, src demandSource) {
	clips := m.cachedLibrary(s)
	started := 0
	for _, c := range clips {
		if started >= 4 {
			break
		}
		if c.SnapshotPath == "" {
			continue
		}
		token := ClipToken(c)
		st, err := os.Stat(c.SnapshotPath)
		if err != nil {
			continue
		}
		m.mu.Lock()
		if m.snapUploading[token] {
			m.mu.Unlock()
			continue
		}
		if prev, done := m.snapUploaded[token]; done &&
			prev.size == st.Size() && prev.modUnix == st.ModTime().Unix() &&
			time.Since(prev.uploadedAt) < 20*time.Hour {
			m.mu.Unlock()
			continue
		}
		m.snapUploading[token] = true
		m.mu.Unlock()
		started++
		go m.uploadSnapshot(s, token, c.SnapshotPath, st.Size(), st.ModTime().Unix())
	}
}

// uploadSnapshot sends one snapshot JPEG to the relay.
func (m *RemoteManager) uploadSnapshot(s Settings, token, path string, size, modUnix int64) {
	defer func() {
		if r := recover(); r != nil {
			startlog.LogPanic("remote snapshot upload "+token, r)
		}
		m.mu.Lock()
		delete(m.snapUploading, token)
		m.mu.Unlock()
	}()
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	src := m.newSource(s)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := src.UploadSnapshot(ctx, token, f, size); err != nil {
		return
	}
	m.mu.Lock()
	m.snapUploaded[token] = clipUpload{size: size, modUnix: modUnix, uploadedAt: time.Now()}
	m.snapUploads++
	m.mu.Unlock()
}

// reconcile starts pushers for wanted slugs and stops the rest.
// A pusher also restarts if its inputs changed (sub-stream URL or
// publish target).
func (m *RemoteManager) reconcile(s Settings, want map[string]bool) {
	bySlug := map[string]Camera{}
	for _, c := range s.Cameras {
		bySlug[c.Slug()] = c
	}

	m.mu.Lock()
	for slug, cancel := range m.cancels {
		cam, ok := bySlug[slug]
		if !want[slug] || !ok || m.sigs[slug] != pushSig(cam, s) {
			cancel()
			delete(m.cancels, slug)
			delete(m.sigs, slug)
		}
	}
	for slug := range want {
		if _, ok := m.cancels[slug]; ok {
			continue
		}
		cam, ok := bySlug[slug]
		if !ok {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		m.cancels[slug] = cancel
		m.sigs[slug] = pushSig(cam, s)
		go m.supervise(ctx, cam, s)
	}
	pushing := make([]string, 0, len(m.cancels))
	for slug := range m.cancels {
		pushing = append(pushing, slug)
	}
	sort.Strings(pushing)
	m.status.Pushing = pushing
	m.mu.Unlock()
}

// supervise runs one pusher with backoff until told to stop.
func (m *RemoteManager) supervise(ctx context.Context, cam Camera, s Settings) {
	defer func() {
		if r := recover(); r != nil {
			startlog.LogPanic("remote pusher supervise "+cam.Slug(), r)
		}
	}()
	backoff := 3 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		_ = func() (err error) {
			defer func() {
				if rec := recover(); rec != nil {
					startlog.LogPanic("remote pusher "+cam.Slug(), rec)
					err = fmt.Errorf("pusher panic: %v", rec)
				}
			}()
			return m.runPush(ctx, cam, s)
		}()
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// ffmpegPush is the production pusher: stream-copy the camera's
// sub-stream to the relay. One attempt; supervise() retries.
func (m *RemoteManager) ffmpegPush(ctx context.Context, cam Camera, s Settings) error {
	if m.ffmpeg == "" {
		return errNoFFmpeg
	}
	args := []string{
		"-hide_banner", "-loglevel", "warning",
		"-rtsp_transport", "tcp",
		"-i", cam.SubStreamURL(),
		"-c", "copy",
		"-f", "rtsp", "-rtsp_transport", "tcp",
		s.PublishURL(cam),
	}
	cmd := exec.CommandContext(ctx, m.ffmpeg, args...)
	hideWindow(cmd)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	err := cmd.Wait()
	if err != nil && stderr.Len() > 0 {
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		return &pushError{msg: lines[len(lines)-1]}
	}
	return err
}

type pushError struct{ msg string }

func (e *pushError) Error() string { return e.msg }

var errNoFFmpeg = &pushError{msg: "ffmpeg not found"}

func pushSig(cam Camera, s Settings) string {
	return strings.Join([]string{cam.SubStreamURL(), s.PublishURL(cam)}, "|")
}

func relayCamsSig(cams []RelayCamera) string {
	var parts []string
	for _, c := range cams {
		parts = append(parts, c.Slug+"="+c.Name)
	}
	return strings.Join(parts, ",")
}
