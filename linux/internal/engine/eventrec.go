// Direct event recording (v1.6).
//
// This replaces the v1.5 chunk + finalizer architecture, which
// Justin reported was still showing 10 s clips: the recorder
// wrote 10 s chunks continuously in motion mode and a background
// finalizer stitched them into event clips, and the stitching
// kept failing on real cameras. The new architecture records ONE
// clip per event directly:
//
//   - While a camera is in Motion or Person-only mode, a rolling
//     pre-buffer runs: one ffmpeg writes short (5 s) temp
//     segments of stream1 into a scratch dir OUTSIDE the
//     recordings tree (never listed as clips), keeping only the
//     last pre+margin seconds. The segments are stream copy for
//     video and AAC 64k for audio — the same mapping as the event
//     body — so they can be concatenated with it.
//   - When the detector opens an event (motion, or a person in
//     Person-only mode), ONE ffmpeg starts writing stream1
//     directly to a single temp file in the scratch dir. It keeps
//     recording while the event is open.
//   - When the event closes AND the post-buffer (read live from
//     settings, never hard-coded) has elapsed with no re-open,
//     ffmpeg is stopped gracefully, the pre-roll segments
//     covering [eventStart-pre, eventStart] are prepended to the
//     event body (one concat, stream copy, into a temp file +
//     atomic rename), and the result is the single event clip in
//     the recordings dir. If pre == 0 there is no prepend — the
//     event body is just remuxed with +faststart into place.
//
// The event body is ONE continuous recording: the pre-roll
// prepend is the only concat and exists only to include footage
// from before motion/person was detected. There is no merging of
// the event body, and no finalizer any more.
//
// Re-opening: if motion/person resumes during the post-buffer
// window, the SAME event file keeps recording (it is extended);
// no second file is started. A 10-minute cap splits a very long
// event into consecutive files (same event, new file).
//
// If the camera refuses a connection, both the rolling buffer and
// the event recorder retry with backoff (the recorder's existing
// behaviour). If the rolling buffer is unavailable, the event is
// still recorded without pre-roll (logged) rather than failing.
package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"daybreaknvr/internal/startlog"
)

// MaxEventClipSeconds caps one event clip at 10 minutes of
// content. A longer event is split into consecutive clips, so an
// endless-motion event can never produce an unbounded file.
const MaxEventClipSeconds = 600

// rollSegmentSeconds is the length of one rolling pre-buffer
// segment. Short segments keep the pre-roll boundary slack small:
// the pre-roll can only use segments that end at or before the
// event body starts, so at most one segment (5 s) of pre-roll
// coverage is lost at the join.
const rollSegmentSeconds = 5

// rollMarginSeconds is the extra pre-roll the janitor keeps beyond
// the configured pre-buffer (slack for segment boundaries and for
// an event that opens just as a segment rolls over).
const rollMarginSeconds = 15

// videoTrackTimescale pins the MP4 video track timescale for both
// the rolling pre-buffer and the event body (v1.8). Without it the
// body is written with the RTP clock (90000) while the pre-roll
// segments use 15360, and the finalize concat then inflates the
// clip duration ~5.86x — so the duration check dropped every
// event clip on 2026-11-15 (22 of 22).
const videoTrackTimescale = "15360"

// rollStallAfter is how long a live pre-buffer may go without
// writing a new segment before the watchdog (v1.8) restarts it.
// A healthy buffer writes a 5 s segment every 5 s; 30 s of
// silence means the RTSP connection has stalled even though the
// process is still alive.
const rollStallAfter = 30 * time.Second

// ScratchDir is the EventRecorder's working directory: a hidden
// sibling of the recordings dir. Same filesystem as the recordings
// (the finalize rename is atomic), but outside the recordings
// tree, so ScanLibrary never lists the rolling pre-roll segments
// or in-progress event files as clips. "" when there is no
// recordings dir to sit beside.
func ScratchDir(recordingsDir string) string {
	if strings.TrimSpace(recordingsDir) == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(recordingsDir), ".nvr-scratch")
}

// EventStatus is one camera's direct-event recording state.
type EventStatus struct {
	Recording bool
	Since     time.Time
	LastError string
}

// ffmpegProc is one managed ffmpeg process with a graceful stop:
// 'q' on stdin first (ffmpeg finalizes the MP4 trailer on 'q'),
// then SIGINT, then SIGKILL after the grace period.
type ffmpegProc struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stderr  strings.Builder
	done    chan struct{}
	exitErr error
}

func startFFmpegProc(ffmpeg string, args []string) (*ffmpegProc, error) {
	cmd := exec.Command(ffmpeg, args...)
	hideWindow(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	p := &ffmpegProc{cmd: cmd, stdin: stdin, done: make(chan struct{})}
	cmd.Stderr = &p.stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		p.exitErr = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

// stop gracefully stops the process and waits for it to exit
// (at most grace, then SIGKILL). It is safe to call twice.
func (p *ffmpegProc) stop(grace time.Duration) {
	if p == nil {
		return
	}
	select {
	case <-p.done:
		return
	default:
	}
	// 'q' on stdin: ffmpeg quits cleanly and writes the MP4
	// trailer, so the file it was writing stays playable.
	if p.stdin != nil {
		_, _ = io.WriteString(p.stdin, "q")
	}
	select {
	case <-p.done:
		return
	case <-time.After(grace):
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Signal(os.Interrupt)
	}
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		if p.cmd != nil && p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		<-p.done
	}
}

// lastErrorText is the last line of the process's stderr, trimmed.
func (p *ffmpegProc) lastErrorText() string {
	if p == nil {
		return ""
	}
	return trimErr(p.stderr.String())
}

// eventRecording is one event file being recorded (or about to be
// finalized) for a camera.
type eventRecording struct {
	nameTime  time.Time // naming time for the final file (event start, or split/recovery time)
	kind      string    // "person" or "motion"
	splitIdx  int       // 0 = first file of the event, >0 = consecutive split
	tmpPath   string    // scratch event file (not in the recordings dir)
	proc      *ffmpegProc
	startedAt time.Time
	lastEnd   time.Time // last known event end (post window is measured from here)
	preRoll   []string  // pinned pre-roll segment paths (piece 0 only)
}

// eventCamState is the EventRecorder's per-camera state.
type eventCamState struct {
	cam Camera
	// Rolling pre-buffer.
	rollProc      *ffmpegProc
	rollScratch   string
	rollErr       string
	rollLastStart time.Time
	rollBackoff   time.Duration
	// rollLastSeg is the newest pre-roll segment mtime seen by
	// the watchdog (v1.8): a live buffer advances it every few
	// seconds, so a value older than rollStallAfter means the
	// worker has stalled.
	rollLastSeg time.Time
	// Current event recording.
	rec *eventRecording
	// pinned pre-roll segment paths for the current event (the
	// janitor must not delete them before finalize).
	pinned map[string]bool
	// Event-start retry backoff (a camera that refuses
	// connections must not be hammered every second).
	lastStartAttempt time.Time
	startBackoff     time.Duration
	// Status.
	recording bool
	since     time.Time
	lastErr   string
}

// EventRecorder records one clip per motion/person event,
// directly, for every camera in Motion or Person-only mode.
type EventRecorder struct {
	ffmpeg      string
	getSettings func() Settings
	tracker     *MotionTracker

	mu     sync.Mutex
	cams   map[string]*eventCamState
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewEventRecorder creates a recorder. getSettings is called on
// every tick so pre/post buffers are always read live.
func NewEventRecorder(ffmpeg string, getSettings func() Settings, tracker *MotionTracker) *EventRecorder {
	return &EventRecorder{
		ffmpeg:      ffmpeg,
		getSettings: getSettings,
		tracker:     tracker,
		cams:        map[string]*eventCamState{},
		stopCh:      make(chan struct{}),
	}
}

// Start wipes stale scratch (segments/event files from a previous
// run are useless — an event in progress when the app stops is
// lost, exactly as the old in-progress chunk was) and starts the
// 1 s tick loop.
func (er *EventRecorder) Start() {
	if er.ffmpeg == "" || er.getSettings == nil || er.tracker == nil {
		return
	}
	if s := er.getSettings(); s.RecordingsDir != "" {
		_ = os.RemoveAll(ScratchDir(s.RecordingsDir))
	}
	er.wg.Add(1)
	go er.loop()
}

// Stop ends the tick loop and stops every ffmpeg process. An
// event in progress at shutdown is abandoned (its temp file stays
// in the scratch dir and is wiped on the next Start).
func (er *EventRecorder) Stop() {
	select {
	case <-er.stopCh:
		return
	default:
		close(er.stopCh)
	}
	er.wg.Wait()
	er.mu.Lock()
	defer er.mu.Unlock()
	for _, st := range er.cams {
		if st.rec != nil && st.rec.proc != nil {
			st.rec.proc.stop(3 * time.Second)
			st.rec = nil
		}
		er.stopRollLocked(st)
		st.recording = false
	}
}

// Status returns a snapshot of one camera's event recording state.
func (er *EventRecorder) Status(id string) EventStatus {
	er.mu.Lock()
	defer er.mu.Unlock()
	st, ok := er.cams[id]
	if !ok {
		return EventStatus{}
	}
	return EventStatus{Recording: st.recording, Since: st.since, LastError: st.lastErr}
}

// RecordingCount reports how many cameras are recording an event
// right now (for the status line).
func (er *EventRecorder) RecordingCount() int {
	er.mu.Lock()
	defer er.mu.Unlock()
	n := 0
	for _, st := range er.cams {
		if st.recording {
			n++
		}
	}
	return n
}

func (er *EventRecorder) loop() {
	defer er.wg.Done()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-er.stopCh:
			return
		case now := <-t.C:
			startlog.Guard("event recorder tick", func() { er.tick(now) })
		}
	}
}

// tick drives one pass over the event cameras.
func (er *EventRecorder) tick(now time.Time) {
	if er.ffmpeg == "" {
		return
	}
	s := er.getSettings()
	er.mu.Lock()
	defer er.mu.Unlock()
	want := map[string]Camera{}
	for _, cam := range s.Cameras {
		if cam.Enabled && cam.Record &&
			(cam.Mode() == RecordMotion || cam.Mode() == RecordPerson) {
			want[cam.ID] = cam
		}
	}
	// Stop state for cameras that left event modes.
	for id, st := range er.cams {
		if _, ok := want[id]; !ok {
			if st.rec != nil && st.rec.proc != nil {
				st.rec.proc.stop(3 * time.Second)
				st.rec = nil
			}
			er.stopRollLocked(st)
			st.recording = false
			delete(er.cams, id)
		}
	}
	for id, cam := range want {
		st, ok := er.cams[id]
		if !ok {
			st = &eventCamState{cam: cam, pinned: map[string]bool{}, rollBackoff: 3 * time.Second, startBackoff: 3 * time.Second}
			er.cams[id] = st
		}
		st.cam = cam
		er.tickCamLocked(st, cam, s, now)
	}
}

// tickCamLocked drives one camera's rolling buffer + event state.
// The caller holds er.mu.
func (er *EventRecorder) tickCamLocked(st *eventCamState, cam Camera, s Settings, now time.Time) {
	pre, post := prePad(s), postPad(s)
	// 1. Rolling pre-buffer (only when pre > 0). Paused while an
	// event is recording (v1.8): the event ffmpeg already holds
	// stream1, and restarting the buffer would put a third RTSP
	// connection on the camera — some cameras refuse it.
	if pre > 0 && st.rec == nil {
		er.ensureRollLocked(st, cam, s, now)
		er.watchRollLocked(st, cam, s, now)
	} else if pre <= 0 {
		er.stopRollLocked(st)
	}
	// 2. Janitor: drop old unpinned pre-roll segments.
	er.janitorLocked(st, cam, pre, now)

	// 3. Event state machine.
	trig, trigStart, trigKind := eventTrigger(cam, er.tracker)
	if st.rec == nil {
		if !trig {
			return
		}
		// Back off a camera that keeps refusing connections.
		if now.Sub(st.lastStartAttempt) < st.startBackoff {
			return
		}
		st.lastStartAttempt = now
		if rec := er.startEventLocked(st, cam, s, trigStart, trigKind, 0); rec != nil {
			st.rec = rec
			st.recording = true
			st.since = now
			st.lastErr = ""
			st.startBackoff = 3 * time.Second
		} else {
			if st.startBackoff < 30*time.Second {
				st.startBackoff *= 2
			}
		}
		return
	}
	rec := st.rec
	// The event ffmpeg exited on its own (camera dropped, crash):
	// finalize what it wrote, if anything playable, and let the
	// next tick start a fresh file if the event is still going.
	select {
	case <-rec.proc.done:
		er.finishRecordingLocked(st, cam, s, now, rec)
		st.rec = nil
		st.recording = false
		return
	default:
	}
	// 10-minute cap: close this file and start a consecutive one
	// (same event, new file, no pre-roll — it is a continuation).
	if now.Sub(rec.startedAt) >= MaxEventClipSeconds*time.Second {
		er.finishRecordingLocked(st, cam, s, now, rec)
		if next := er.startEventLocked(st, cam, s, now, rec.kind, rec.splitIdx+1); next != nil {
			st.rec = next
			st.since = now
		} else {
			st.rec = nil
			st.recording = false
		}
		return
	}
	// Track the event's end so the post window is measured from
	// the last motion/person, not from when the event opened.
	if end := lastTriggerEnd(cam, er.tracker, rec.lastEnd); !end.IsZero() {
		rec.lastEnd = end
	}
	// Close once the event is gone AND the post-buffer has fully
	// elapsed with no re-open. A re-open during the window keeps
	// this same file recording (trig goes true again above).
	if !trig && !rec.lastEnd.IsZero() && now.Sub(rec.lastEnd) >= post {
		er.finishRecordingLocked(st, cam, s, now, rec)
		st.rec = nil
		st.recording = false
	}
}

// eventTrigger reports whether a camera should be recording an
// event right now: Motion mode triggers on any open event
// (motion or person); Person-only mode triggers only on an open
// person event — motion with no person does not record.
func eventTrigger(cam Camera, tracker *MotionTracker) (active bool, start time.Time, kind string) {
	if tracker == nil {
		return false, time.Time{}, ""
	}
	ev, ok := tracker.OpenEvent(cam.ID)
	if !ok {
		return false, time.Time{}, ""
	}
	switch cam.Mode() {
	case RecordMotion:
		kind = ev.Kind
		if kind == "" {
			kind = "motion"
		}
		return true, ev.Start, kind
	case RecordPerson:
		if ev.Kind != "person" {
			return false, time.Time{}, ""
		}
		return true, ev.Start, "person"
	}
	return false, time.Time{}, ""
}

// lastTriggerEnd is the end of the event that governs the
// post-buffer for a camera: the last event of any kind in Motion
// mode, the last person event in Person-only mode.
func lastTriggerEnd(cam Camera, tracker *MotionTracker, fallback time.Time) time.Time {
	if tracker == nil {
		return fallback
	}
	switch cam.Mode() {
	case RecordMotion:
		if evs := tracker.Events(cam.ID); len(evs) > 0 {
			return evs[len(evs)-1].End
		}
	case RecordPerson:
		if ev, ok := tracker.LastPersonEvent(cam.ID); ok {
			return ev.End
		}
	}
	return fallback
}

// ---------- rolling pre-buffer ----------

// rollFFmpegArgs builds the rolling pre-buffer ffmpeg arguments.
// The video track timescale is pinned to videoTrackTimescale so
// the segments concat cleanly with the event body (v1.8). Both
// forms are set: the plain output option (honoured by the
// segment muxer in ffmpeg 7.x) and -segment_format_options
// (the only form ffmpeg 8.x's segment muxer passes to its child
// mp4 muxer — with the plain option alone, 8.x writes the
// segments at the RTP clock 90000 and the finalize concat
// mangles the clip). Either form alone gives 15360 on one
// version and 90000 on the other; together they give 15360 on
// both (verified against 7.0.2 and 8.1.2).
func rollFFmpegArgs(cam Camera, pattern string) []string {
	return []string{
		"-hide_banner", "-loglevel", "warning",
		"-rtsp_transport", "tcp",
		"-i", cam.MainStreamURL(),
		"-map", "0:v:0", "-c:v", "copy",
		"-map", "0:a:0?", "-c:a", "aac", "-b:a", "64k",
		"-video_track_timescale", videoTrackTimescale,
		"-f", "segment",
		"-segment_time", fmt.Sprint(rollSegmentSeconds),
		"-segment_format", "mp4",
		"-segment_format_options", "video_track_timescale="+videoTrackTimescale,
		"-reset_timestamps", "1",
		"-strftime", "1",
		pattern,
	}
}

// eventFFmpegArgs builds the event-body ffmpeg arguments. The
// video track timescale is pinned to videoTrackTimescale (v1.8):
// without it the body is written with the RTP clock (90000)
// while the pre-roll segments use 15360, and the finalize
// concat then inflates the clip duration ~5.86x so the duration
// check drops every clip.
func eventFFmpegArgs(cam Camera, tmpPath string) []string {
	return []string{
		"-hide_banner", "-loglevel", "warning",
		"-rtsp_transport", "tcp",
		"-i", cam.MainStreamURL(),
		"-map", "0:v:0", "-c:v", "copy",
		"-map", "0:a:0?", "-c:a", "aac", "-b:a", "64k",
		"-video_track_timescale", videoTrackTimescale,
		"-f", "mp4", "-y", tmpPath,
	}
}

// ensureRollLocked starts the rolling pre-buffer ffmpeg for a
// camera when it is not running (with backoff after failures).
// The caller holds er.mu.
func (er *EventRecorder) ensureRollLocked(st *eventCamState, cam Camera, s Settings, now time.Time) {
	scratch := ScratchDir(s.RecordingsDir)
	if scratch == "" {
		return
	}
	if st.rollProc != nil {
		select {
		case <-st.rollProc.done:
			// Exited: remember the failure and fall through to
			// the backoff-guarded restart below.
			st.rollErr = st.rollProc.lastErrorText()
			st.rollProc = nil
		default:
			if st.rollScratch == scratch {
				return // already running against this scratch dir
			}
		}
	}
	if now.Sub(st.rollLastStart) < st.rollBackoff {
		return
	}
	st.rollLastStart = now
	dir := filepath.Join(scratch, cam.Slug())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		st.rollErr = err.Error()
		return
	}
	pattern := filepath.Join(dir, "pre_%Y%m%d_%H%M%S.mp4")
	proc, err := startFFmpegProc(er.ffmpeg, rollFFmpegArgs(cam, pattern))
	if err != nil {
		st.rollErr = err.Error()
		if st.rollBackoff < 30*time.Second {
			st.rollBackoff *= 2
		}
		startlog.Stage("event recorder: pre-roll for %s failed to start: %v", cam.Slug(), err)
		return
	}
	st.rollProc = proc
	st.rollScratch = scratch
	st.rollErr = ""
	st.rollBackoff = 3 * time.Second
	// The watchdog gives a fresh process its full grace window
	// from here, so stale segments left by a dead process can
	// never make it look stalled on its first tick.
	st.rollLastSeg = now
}

// newestSegmentTime returns the newest modtime of any pre_*.mp4
// segment in dir, or ok=false when there are none.
func newestSegmentTime(dir string) (time.Time, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return time.Time{}, false
	}
	var newest time.Time
	ok := false
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "pre_") || !strings.HasSuffix(strings.ToLower(name), ".mp4") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if !ok || info.ModTime().After(newest) {
			newest = info.ModTime()
			ok = true
		}
	}
	return newest, ok
}

// watchRollLocked is the pre-buffer watchdog (v1.8): a
// stalled-but-alive pre-buffer ffmpeg (RTSP connection dead,
// process still running, no new segments) is killed so
// ensureRollLocked restarts it on the next tick. The app used to
// only restart a worker after its process exited, so a stalled
// connection sat dead forever — on 2026-11-15 the pre-buffers
// stopped at ~03:30 and ~08:41 and nothing recorded for hours.
// The caller holds er.mu.
func (er *EventRecorder) watchRollLocked(st *eventCamState, cam Camera, s Settings, now time.Time) {
	if st.rollProc == nil || st.rollScratch == "" {
		return
	}
	select {
	case <-st.rollProc.done:
		return // exiting on its own; ensureRollLocked handles it
	default:
	}
	dir := filepath.Join(st.rollScratch, cam.Slug())
	if newest, ok := newestSegmentTime(dir); ok {
		if newest.After(st.rollLastSeg) {
			st.rollLastSeg = newest
			return
		}
	} else if st.rollLastSeg.IsZero() {
		// No segments yet — grace from now, not from a stale
		// zero value, so a slow first segment is never a stall.
		st.rollLastSeg = now
		return
	}
	if now.Sub(st.rollLastSeg) > rollStallAfter {
		startlog.Stage("event recorder: pre-roll for %s stalled (no new segment for %s) — restarting",
			cam.Slug(), rollStallAfter)
		st.rollProc.stop(2 * time.Second)
		st.rollProc = nil
		st.rollScratch = ""
		st.rollLastSeg = time.Time{}
		// Prompt restart on the next tick, not behind a
		// backoff grown by earlier failures.
		st.rollBackoff = 3 * time.Second
		st.rollLastStart = time.Time{}
	}
}

// stopRollLocked stops a camera's rolling pre-buffer. The caller
// holds er.mu.
func (er *EventRecorder) stopRollLocked(st *eventCamState) {
	if st.rollProc != nil {
		st.rollProc.stop(3 * time.Second)
		st.rollProc = nil
	}
	st.rollScratch = ""
}

// janitorLocked deletes rolling segments that are older than the
// pre-buffer plus margin, unless pinned for the current event.
// The caller holds er.mu.
func (er *EventRecorder) janitorLocked(st *eventCamState, cam Camera, pre time.Duration, now time.Time) {
	if st.rollScratch == "" {
		return
	}
	dir := filepath.Join(st.rollScratch, cam.Slug())
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	horizon := now.Add(-(pre + rollMarginSeconds*time.Second))
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "pre_") || !strings.HasSuffix(strings.ToLower(name), ".mp4") {
			continue
		}
		p := filepath.Join(dir, name)
		if st.pinned[p] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// A segment's modtime is when ffmpeg closed it (its
		// end); the in-progress segment is still being written
		// so its modtime stays recent and it is kept.
		if info.ModTime().Before(horizon) {
			_ = os.Remove(p)
		}
	}
}

// pinPreRollLocked returns the finalized rolling segments covering
// the pre-roll window [eventStart-pre, eventStart] and pins them
// so the janitor cannot delete them before finalize. Only
// segments that end at or before the event body starts are
// usable — the segment in progress at the event's start has no
// moov yet and is skipped, so at most one segment (5 s) of
// pre-roll coverage is lost at the join. The caller holds er.mu.
func (er *EventRecorder) pinPreRollLocked(st *eventCamState, cam Camera, eventStart time.Time, pre time.Duration) []string {
	if st.rollScratch == "" || pre <= 0 {
		return nil
	}
	dir := filepath.Join(st.rollScratch, cam.Slug())
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	winStart := eventStart.Add(-pre)
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "pre_") || !strings.HasSuffix(strings.ToLower(name), ".mp4") {
			continue
		}
		p := filepath.Join(dir, name)
		start := parseClipTime(name, time.Time{})
		if start.IsZero() {
			continue
		}
		dur, ok := parseMP4Duration(p)
		if !ok || dur <= 0 {
			continue // not finalized yet (still being written)
		}
		end := start.Add(time.Duration(dur * float64(time.Second)))
		if end.After(winStart) && start.Before(eventStart) {
			out = append(out, p)
		}
	}
	sort.Strings(out) // strftime names sort in time order
	if st.pinned == nil {
		st.pinned = map[string]bool{}
	}
	for _, p := range out {
		st.pinned[p] = true
	}
	return out
}

// ---------- event recording ----------

// startEventLocked starts one ffmpeg writing stream1 directly to a
// scratch temp file for the event. nameTime names the final file
// (the event's start for piece 0; the split/recovery time for
// later pieces). Returns nil on failure (the caller backs off and
// retries). The caller holds er.mu.
func (er *EventRecorder) startEventLocked(st *eventCamState, cam Camera, s Settings, nameTime time.Time, kind string, splitIdx int) *eventRecording {
	scratch := ScratchDir(s.RecordingsDir)
	if scratch == "" {
		st.lastErr = "no recordings dir"
		return nil
	}
	var preRoll []string
	if splitIdx == 0 {
		preRoll = er.pinPreRollLocked(st, cam, nameTime, prePad(s))
	}
	// v1.8: pause the rolling pre-buffer before the event
	// ffmpeg opens stream1, so the camera has only two RTSP
	// connections during the event (detector stream2 + event
	// body stream1) instead of three — some cameras refuse a
	// third connection ("Operation not permitted"). The pre-roll
	// is already pinned to disk above, so nothing is lost. The
	// tick loop restarts the buffer on the next tick after the
	// event ends (st.rec == nil gates it), and the backoff is
	// reset here so that restart is prompt even if the event
	// ffmpeg fails to start.
	er.stopRollLocked(st)
	st.rollBackoff = 3 * time.Second
	st.rollLastStart = time.Time{}
	evDir := filepath.Join(scratch, cam.Slug())
	if err := os.MkdirAll(evDir, 0o755); err != nil {
		st.lastErr = err.Error()
		return nil
	}
	tmpPath := filepath.Join(evDir, "ev_"+nameTime.Format("20060102_150405")+".mp4")
	if _, err := os.Stat(tmpPath); err == nil {
		// A previous attempt left a temp file with this name
		// (crash, or two events in the same second): use a
		// distinct name so the old file is never overwritten.
		tmpPath = filepath.Join(evDir, fmt.Sprintf("ev_%s_%d.mp4",
			nameTime.Format("20060102_150405"), time.Now().UnixNano()%100000))
	}
	proc, err := startFFmpegProc(er.ffmpeg, eventFFmpegArgs(cam, tmpPath))
	if err != nil {
		st.lastErr = err.Error()
		startlog.Stage("event recorder: %s event %s failed to start: %v",
			cam.Slug(), nameTime.Format("15:04:05"), err)
		return nil
	}
	rec := &eventRecording{
		nameTime:  nameTime,
		kind:      kind,
		splitIdx:  splitIdx,
		tmpPath:   tmpPath,
		proc:      proc,
		startedAt: time.Now(),
		lastEnd:   nameTime,
		preRoll:   preRoll,
	}
	startlog.Stage("event recorder: %s recording event %s (%s)", cam.Slug(), nameTime.Format("15:04:05"), kind)
	return rec
}

// finishRecordingLocked stops the event ffmpeg gracefully and
// turns the temp file (+ pre-roll, when any) into the single
// final clip in the recordings dir. A corrupt or empty temp file
// is dropped and logged, never listed as a clip. The caller
// holds er.mu.
func (er *EventRecorder) finishRecordingLocked(st *eventCamState, cam Camera, s Settings, now time.Time, rec *eventRecording) {
	defer func() {
		// Unpin the pre-roll segments (the janitor may clean
		// them up now) and drop the temp file if it is still
		// there.
		for _, p := range rec.preRoll {
			delete(st.pinned, p)
		}
		_ = os.Remove(rec.tmpPath)
	}()
	if rec.proc != nil {
		rec.proc.stop(5 * time.Second)
		if rec.proc.exitErr != nil {
			// An ffmpeg that died on its own (not our graceful
			// stop) may have left an unfinalized file; the moov
			// check below decides whether anything is usable.
			startlog.Stage("event recorder: %s event ffmpeg exited: %v (%s)",
				cam.Slug(), rec.proc.exitErr, rec.proc.lastErrorText())
		}
	}
	if !hasMoov(rec.tmpPath) {
		startlog.Stage("event recorder: %s event %s has no moov atom — dropping (camera likely dropped mid-event)",
			cam.Slug(), rec.nameTime.Format("15:04:05"))
		return
	}
	bodyDur, ok := parseMP4Duration(rec.tmpPath)
	if !ok || bodyDur <= 0 {
		startlog.Stage("event recorder: %s event %s has no readable duration — dropping",
			cam.Slug(), rec.nameTime.Format("15:04:05"))
		return
	}
	// Pre-roll segments that are still present and finalized.
	// A segment the janitor deleted (unpinned — should not
	// happen) or one that lost its moov is skipped, and the
	// event is still recorded without that piece of pre-roll.
	var inputs []string
	var expected float64
	for _, p := range rec.preRoll {
		if !hasMoov(p) {
			continue
		}
		dur, ok := parseMP4Duration(p)
		if !ok || dur <= 0 {
			continue
		}
		inputs = append(inputs, p)
		expected += dur
	}
	inputs = append(inputs, rec.tmpPath)
	expected += bodyDur

	dayDir := filepath.Join(s.RecordingsDir, cam.Slug(), rec.nameTime.Format("2006-01-02"))
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		startlog.Stage("event recorder: %s cannot create day dir: %v", cam.Slug(), err)
		return
	}
	finalPath, err := resolveEventTarget(dayDir, cam.Slug(), rec.nameTime)
	if err != nil {
		startlog.Stage("event recorder: %s %v", cam.Slug(), err)
		return
	}
	// Temp output in the destination dir (same filesystem, so
	// the rename is atomic); the .tmp suffix keeps ScanLibrary
	// from seeing it while it exists.
	tmp, err := os.CreateTemp(dayDir, ".event-*.tmp")
	if err != nil {
		startlog.Stage("event recorder: %s cannot create temp file: %v", cam.Slug(), err)
		return
	}
	tmpPath := tmp.Name()
	tmp.Close()

	var cmdErr error
	if len(inputs) > 1 {
		// The pre-roll prepend — the ONLY concat in v1.6, and
		// it exists only to include footage from before the
		// event was detected. The event body itself is one
		// continuous recording and is never merged or stitched.
		cmdErr = concatInto(er.ffmpeg, inputs, tmpPath)
	} else {
		cmdErr = remuxFaststartInto(er.ffmpeg, rec.tmpPath, tmpPath)
	}
	if cmdErr != nil {
		os.Remove(tmpPath)
		startlog.Stage("event recorder: %s finalize failed: %v", cam.Slug(), cmdErr)
		return
	}
	if !hasMoov(tmpPath) {
		os.Remove(tmpPath)
		startlog.Stage("event recorder: %s finalized file has no moov atom", cam.Slug())
		return
	}
	dur, ok := parseMP4Duration(tmpPath)
	if !ok || !eventDurationOK(dur, expected) {
		os.Remove(tmpPath)
		startlog.Stage("event recorder: %s finalized duration %.1fs, expected ~%.1fs — dropping",
			cam.Slug(), dur, expected)
		return
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		startlog.Stage("event recorder: %s rename failed: %v", cam.Slug(), err)
		return
	}
	// Retention ages clips by modtime: the content ends now
	// (the post window just elapsed), so stamp it now.
	_ = os.Chtimes(finalPath, now, now)
	startlog.Stage("event recorder: %s wrote %s (%.0fs, %s)",
		cam.Slug(), filepath.Base(finalPath), dur, rec.kind)
}

// eventDurationOK reports whether a finalized event file's
// duration is consistent with the content that went into it. The
// bounds are generous (0.5×–1.5×) to absorb container rounding
// and the ≤5 s pre-roll boundary slack, but a genuinely short or
// corrupt finalize still fails.
func eventDurationOK(dur, expected float64) bool {
	if dur <= 0 || expected <= 0 {
		return false
	}
	return dur >= 0.5*expected && dur <= 1.5*expected
}

// resolveEventTarget picks the filename for a finalized event
// clip: the canonical <slug>_<nameTime>.mp4, or a suffixed
// variant (_2 … _10) when the canonical name is already taken
// (two events in the same second, or a split landing on an
// existing file). It never overwrites an existing clip.
func resolveEventTarget(dayDir, slug string, nameTime time.Time) (string, error) {
	stamp := nameTime.Format("20060102_150405")
	candidates := []string{filepath.Join(dayDir, slug+"_"+stamp+".mp4")}
	for i := 2; i <= 10; i++ {
		candidates = append(candidates, filepath.Join(dayDir,
			fmt.Sprintf("%s_%s_%d.mp4", slug, stamp, i)))
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p, nil
		}
	}
	return "", fmt.Errorf("no usable event target name for %s_%s", slug, stamp)
}

// concatInto concatenates inputs (pre-roll segments + event body)
// into out with the ffmpeg concat demuxer, stream copy,
// +faststart. The inputs must share codecs (they do: same
// camera, same mapping).
func concatInto(ffmpeg string, inputs []string, out string) error {
	listFile, err := os.CreateTemp("", ".nvr-concat-*.txt")
	if err != nil {
		return err
	}
	listPath := listFile.Name()
	defer os.Remove(listPath)
	for _, p := range inputs {
		fmt.Fprintf(listFile, "file '%s'\n", strings.ReplaceAll(p, "'", "'\\''"))
	}
	if err := listFile.Close(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-loglevel", "warning", "-y",
		"-f", "concat", "-safe", "0", "-i", listPath,
		"-map", "0:v:0", "-c:v", "copy",
		"-map", "0:a:0?", "-c:a", "copy",
		"-movflags", "+faststart", "-f", "mp4", out)
	hideWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg concat: %v: %s", err, trimErr(string(out)))
	}
	return nil
}

// remuxFaststartInto rewrites src into out with the moov atom at
// the front (stream copy, no re-encode).
func remuxFaststartInto(ffmpeg, src, out string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-loglevel", "warning", "-y",
		"-i", src, "-c", "copy", "-movflags", "+faststart",
		"-f", "mp4", out)
	hideWindow(cmd)
	if o, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg remux: %v: %s", err, trimErr(string(o)))
	}
	return nil
}
