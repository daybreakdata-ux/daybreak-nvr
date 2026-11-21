// Tests for the v1.6 direct event recorder: one ffmpeg writes one
// clip per event straight to the recordings dir — no 10s chunks, no
// finalizer. The tests run against a local MediaMTX RTSP server
// (same harness as TestRecorderWritesSegments) and drive the motion
// tracker directly, the way the app's motionTick does.
package engine

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"daybreaknvr/internal/startlog"
)

// startEventTestRTSP starts MediaMTX with a test-pattern publisher
// and returns the RTSP URL. Skips the test when MediaMTX is not
// available (same as the recorder test).
func startEventTestRTSP(t *testing.T) string {
	t.Helper()
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
	t.Cleanup(func() { _ = srv.Process.Kill() })
	waitForRTSP(t, "127.0.0.1:8554", 10*time.Second)
	ff := FindFFmpeg()
	pub := exec.Command(ff, "-hide_banner", "-loglevel", "error",
		"-re", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=15",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-g", "30",
		"-f", "rtsp", "-rtsp_transport", "tcp", "rtsp://127.0.0.1:8554/testcam")
	hideWindow(pub)
	if err := pub.Start(); err != nil {
		t.Fatalf("start publisher: %v", err)
	}
	t.Cleanup(func() { _ = pub.Process.Kill() })
	waitForStream(t, ffprobePath(ff), "rtsp://127.0.0.1:8554/testcam", 10*time.Second)
	return "rtsp://127.0.0.1:8554/testcam"
}

// driveTrackerCloser mimics the app's motionTick: it closes stale
// events every 500 ms with the given quiet period, until stopCh is
// closed. The test drives the tracker with a short quiet (1 s)
// instead of the app's 5 s so the post-buffer is the thing being
// measured, not the stale-close delay.
func driveTrackerCloser(t *testing.T, tr *MotionTracker, quiet time.Duration) chan struct{} {
	t.Helper()
	stopCh := make(chan struct{})
	go func() {
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stopCh:
				return
			case now := <-tick.C:
				tr.CloseStale(now, quiet)
				// Persist events the way the app does, so the
				// library can label clips with their kind.
				tr.Save()
			}
		}
	}()
	return stopCh
}

// listRecordedMP4s returns every .mp4 under the recordings dir
// (the scratch dir is a sibling, never under it).
func listRecordedMP4s(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(strings.ToLower(d.Name()), ".mp4") {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// waitForClips polls until n clips are listed (or the deadline
// passes) and returns what the library found.
func waitForClips(t *testing.T, dir string, cams []Camera, n int, timeout time.Duration) []Clip {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if clips := ScanLibrary(dir, cams); len(clips) >= n {
			return clips
		}
		time.Sleep(500 * time.Millisecond)
	}
	return ScanLibrary(dir, cams)
}

// probeClipDuration returns the container duration of a file via
// ffprobe (JSON output).
func probeClipDuration(t *testing.T, probe, path string) float64 {
	t.Helper()
	out, err := exec.Command(probe, "-v", "error",
		"-show_entries", "format=duration", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var parsed struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("ffprobe parse %s: %v", path, err)
	}
	d, err := strconv.ParseFloat(parsed.Format.Duration, 64)
	if err != nil {
		t.Fatalf("ffprobe duration %q: %v", parsed.Format.Duration, err)
	}
	return d
}

// isFaststart reports whether the moov box comes before the mdat
// box in the file (faststart: playable before the whole file is
// downloaded).
func isFaststart(t *testing.T, path string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	moov := bytes.Index(data, []byte("moov"))
	mdat := bytes.Index(data, []byte("mdat"))
	return moov >= 0 && mdat >= 0 && moov < mdat
}

// eventTestSetup builds the settings/tracker/recorder for one
// event-recorder test and starts the recorder. The caller drives
// the tracker with NoteHot/NotePerson and closes the test with
// stopCh (tracker closer) + rec.Stop().
func eventTestSetup(t *testing.T, url string, mode string, pre, post int) (dir string, s Settings, tr *MotionTracker, probe string) {
	t.Helper()
	ff := ffmpegOrSkip(t)
	dir = t.TempDir()
	s = DefaultSettings()
	s.RecordingsDir = dir
	s.PreBufferSeconds = pre
	s.PostBufferSeconds = post
	cam := Camera{ID: "t1", Name: "Test Cam", Enabled: true, Record: true,
		RecordMode: mode, MainURL: url}
	s.Cameras = []Camera{cam}
	tr = NewMotionTracker(filepath.Join(dir, ".motion-events.json"))
	get := func() Settings { return s }
	rec := NewEventRecorder(ff, get, tr)
	rec.Start()
	t.Cleanup(func() { rec.Stop() })
	return dir, s, tr, ffprobePath(ff)
}

// noteHotFor calls NoteHot every 500 ms for d, keeping the event
// open for d (the way the analyzer would during real motion).
func noteHotFor(t *testing.T, tr *MotionTracker, camID string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		tr.NoteHot(camID, time.Now())
		time.Sleep(500 * time.Millisecond)
	}
}

// TestEventRecorderOneEventOneFile is the core v1.6 behaviour: one
// motion event produces exactly ONE clip in the recordings dir
// (no 10s chunks), whose duration is about event + pre + post.
func TestEventRecorderOneEventOneFile(t *testing.T) {
	url := startEventTestRTSP(t)
	// pre 10 / post 10, so both buffers are unmistakable in the
	// clip length (expected ≈ 10 pre + 5 event + 10 post ≈ 25 s,
	// with a few seconds of slack for segment boundaries and the
	// 1 s recorder tick).
	dir, s, tr, probe := eventTestSetup(t, url, RecordMotion, 10, 10)
	stopCh := driveTrackerCloser(t, tr, time.Second)
	defer close(stopCh)
	// Warm up: the rolling pre-buffer needs two finalized 5 s
	// segments before a full 10 s of pre-roll exists.
	time.Sleep(12 * time.Second)
	noteHotFor(t, tr, "t1", 5*time.Second)
	clips := waitForClips(t, dir, s.Cameras, 1, 45*time.Second)
	if len(clips) != 1 {
		t.Fatalf("library listed %d clips for one event, want exactly 1", len(clips))
	}
	if files := listRecordedMP4s(t, dir); len(files) != 1 {
		t.Fatalf("recordings dir holds %d mp4 files for one event, want exactly 1: %v", len(files), files)
	}
	d := probeClipDuration(t, probe, clips[0].Path)
	// 10 pre + 5 event + 10 post = 25 s expected; allow ±7 s for
	// pre-roll segment-boundary slack (a 10 s window can hold as
	// little as one ~4 s segment when boundaries align badly),
	// the recorder tick, and the v1.8 pause (the pre-buffer stop
	// delays the event-body start by up to ~1 s).
	if d < 18 || d > 32 {
		t.Fatalf("clip duration %.1fs, want ~25s (10 pre + 5 event + 10 post)", d)
	}
	if !isFaststart(t, clips[0].Path) {
		t.Fatal("clip is not faststart (moov must come before mdat)")
	}
	if clips[0].Kind != "motion" {
		t.Fatalf("clip kind = %q, want %q", clips[0].Kind, "motion")
	}
}

// TestEventRecorderReopenExtendsSameFile: motion resumes during
// the post-buffer window, so the same event file keeps recording
// instead of a second file starting.
func TestEventRecorderReopenExtendsSameFile(t *testing.T) {
	url := startEventTestRTSP(t)
	dir, s, tr, probe := eventTestSetup(t, url, RecordMotion, 5, 10)
	stopCh := driveTrackerCloser(t, tr, time.Second)
	defer close(stopCh)
	time.Sleep(12 * time.Second)
	// First burst of motion.
	noteHotFor(t, tr, "t1", 4*time.Second)
	// Wait for the event to close (1 s quiet) but stay inside
	// the 10 s post window, then move again.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, open := tr.OpenEvent("t1"); !open {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, open := tr.OpenEvent("t1"); open {
		t.Fatal("first event did not close before the re-open")
	}
	noteHotFor(t, tr, "t1", 4*time.Second)
	clips := waitForClips(t, dir, s.Cameras, 1, 45*time.Second)
	if len(clips) != 1 {
		t.Fatalf("library listed %d clips after a re-opened event, want exactly 1", len(clips))
	}
	if files := listRecordedMP4s(t, dir); len(files) != 1 {
		t.Fatalf("recordings dir holds %d mp4 files after a re-opened event, want exactly 1", len(files))
	}
	// Two 4 s bursts + 10 s post + ~5 s pre: the single clip
	// must be clearly longer than one burst alone (> 20 s).
	d := probeClipDuration(t, probe, clips[0].Path)
	if d < 20 {
		t.Fatalf("re-opened clip duration %.1fs, want > 20s (two bursts in one file)", d)
	}
}

// TestEventRecorderNoPreBuffer: pre==0 means no pre-roll is
// prepended — the clip is just the event + post.
func TestEventRecorderNoPreBuffer(t *testing.T) {
	url := startEventTestRTSP(t)
	dir, s, tr, probe := eventTestSetup(t, url, RecordMotion, 0, 10)
	stopCh := driveTrackerCloser(t, tr, time.Second)
	defer close(stopCh)
	// No warm-up needed: with pre==0 there is no rolling buffer.
	noteHotFor(t, tr, "t1", 5*time.Second)
	clips := waitForClips(t, dir, s.Cameras, 1, 45*time.Second)
	if len(clips) != 1 {
		t.Fatalf("library listed %d clips, want exactly 1", len(clips))
	}
	d := probeClipDuration(t, probe, clips[0].Path)
	// 5 s event + 10 s post ≈ 15 s; allow ±4 s for the tick.
	if d < 11 || d > 19 {
		t.Fatalf("clip duration %.1fs with pre==0, want ~15s (5 event + 10 post, no pre-roll)", d)
	}
}

// TestEventRecorderPersonOnlyMotionAloneWritesNothing: in
// Person-only mode, motion with no person must not record.
func TestEventRecorderPersonOnlyMotionAloneWritesNothing(t *testing.T) {
	url := startEventTestRTSP(t)
	dir, s, tr, _ := eventTestSetup(t, url, RecordPerson, 5, 5)
	stopCh := driveTrackerCloser(t, tr, time.Second)
	defer close(stopCh)
	time.Sleep(12 * time.Second)
	noteHotFor(t, tr, "t1", 6*time.Second)
	// The event has closed and the post window has passed; the
	// recorder must never have started.
	time.Sleep(8 * time.Second)
	if files := listRecordedMP4s(t, dir); len(files) != 0 {
		t.Fatalf("person-only mode wrote %d files for motion without a person, want 0: %v", len(files), files)
	}
	if clips := ScanLibrary(dir, s.Cameras); len(clips) != 0 {
		t.Fatalf("library listed %d clips for motion without a person in person-only mode", len(clips))
	}
}

// TestEventRecorderPersonOnlyPersonWritesOneFile: in Person-only
// mode, a person detection opens an event and records exactly one
// clip, kind "person".
func TestEventRecorderPersonOnlyPersonWritesOneFile(t *testing.T) {
	url := startEventTestRTSP(t)
	dir, s, tr, probe := eventTestSetup(t, url, RecordPerson, 5, 10)
	stopCh := driveTrackerCloser(t, tr, time.Second)
	defer close(stopCh)
	time.Sleep(12 * time.Second)
	// A person walks through: hot frames plus person sightings
	// (the person service reports a person about once a second
	// while motion is hot).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tr.NoteHot("t1", time.Now())
		tr.NotePerson("t1", 1, 0.8, time.Now())
		time.Sleep(500 * time.Millisecond)
	}
	clips := waitForClips(t, dir, s.Cameras, 1, 45*time.Second)
	if len(clips) != 1 {
		t.Fatalf("library listed %d clips for one person event, want exactly 1", len(clips))
	}
	if files := listRecordedMP4s(t, dir); len(files) != 1 {
		t.Fatalf("recordings dir holds %d mp4 files for one person event, want exactly 1", len(files))
	}
	d := probeClipDuration(t, probe, clips[0].Path)
	if d < 14 || d > 26 {
		t.Fatalf("person clip duration %.1fs, want ~20s (5 pre + 5 event + 10 post)", d)
	}
	if clips[0].Kind != "person" {
		t.Fatalf("clip kind = %q, want %q", clips[0].Kind, "person")
	}
}

// TestEventRecorderSkipsContinuous: the event recorder only
// handles Motion and Person-only cameras; a continuous camera
// writes nothing here (the segment Recorder owns it, as before).
func TestEventRecorderSkipsContinuous(t *testing.T) {
	url := startEventTestRTSP(t)
	ff := ffmpegOrSkip(t)
	dir := t.TempDir()
	s := DefaultSettings()
	s.RecordingsDir = dir
	s.PreBufferSeconds = 5
	s.PostBufferSeconds = 5
	cam := Camera{ID: "t1", Name: "Test Cam", Enabled: true, Record: true,
		RecordMode: RecordContinuous, MainURL: url}
	s.Cameras = []Camera{cam}
	tr := NewMotionTracker(filepath.Join(dir, ".motion-events.json"))
	rec := NewEventRecorder(ff, func() Settings { return s }, tr)
	rec.Start()
	defer rec.Stop()
	stopCh := driveTrackerCloser(t, tr, time.Second)
	defer close(stopCh)
	noteHotFor(t, tr, "t1", 4*time.Second)
	time.Sleep(8 * time.Second)
	if files := listRecordedMP4s(t, dir); len(files) != 0 {
		t.Fatalf("event recorder wrote %d files for a continuous camera, want 0", len(files))
	}
}

// --- v1.8: timescale fix, pre-buffer watchdog, event pause ---

// TestEventAndRollArgsPinVideoTimescale: both the event-body and
// the pre-roll ffmpeg arguments must pin the video track
// timescale to 15360 (as output options, after -i), so the
// finalize concat never mixes the RTP clock (90000) with 15360.
// The roll args must carry the timescale in BOTH forms: the plain
// output option (ffmpeg 7.x segment muxer) and
// -segment_format_options (ffmpeg 8.x segment muxer ignores the
// plain option and writes 90000 segments without it).
func TestEventAndRollArgsPinVideoTimescale(t *testing.T) {
	cam := Camera{ID: "t1", Name: "Test Cam", Enabled: true, Record: true,
		RecordMode: RecordMotion, MainURL: "rtsp://127.0.0.1:8554/testcam"}
	for _, args := range [][]string{
		rollFFmpegArgs(cam, "/tmp/pre_%Y%m%d_%H%M%S.mp4"),
		eventFFmpegArgs(cam, "/tmp/ev_test.mp4"),
	} {
		inIdx, flagIdx := -1, -1
		for i, a := range args {
			if a == "-i" {
				inIdx = i
			}
			if a == "-video_track_timescale" {
				flagIdx = i
				if i+1 >= len(args) || args[i+1] != videoTrackTimescale {
					t.Fatalf("timescale flag value = %v, want %s (args %v)", args[i+1:], videoTrackTimescale, args)
				}
			}
		}
		if flagIdx < 0 {
			t.Fatalf("args missing -video_track_timescale %s: %v", videoTrackTimescale, args)
		}
		if flagIdx < inIdx {
			t.Fatalf("timescale flag must be an output option (after -i): %v", args)
		}
	}
	roll := rollFFmpegArgs(cam, "/tmp/pre_%Y%m%d_%H%M%S.mp4")
	found := false
	for i, a := range roll {
		if a == "-segment_format_options" && i+1 < len(roll) &&
			roll[i+1] == "video_track_timescale="+videoTrackTimescale {
			found = true
		}
	}
	if !found {
		t.Fatalf("roll args missing -segment_format_options video_track_timescale=%s: %v", videoTrackTimescale, roll)
	}
}

// withDuration inserts "-t <secs>" before the output path (the
// last arg) of a production arg list.
func withDuration(args []string, secs string) []string {
	out := append([]string{}, args[:len(args)-1]...)
	return append(out, "-t", secs, args[len(args)-1])
}

// withoutTimescale drops the -video_track_timescale pair from a
// production arg list, reproducing the pre-v1.8 behaviour.
func withoutTimescale(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		if args[i] == "-video_track_timescale" {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// probeVideoTimeBase returns the video stream's time_base via ffprobe.
func probeVideoTimeBase(t *testing.T, probe, path string) string {
	t.Helper()
	out, err := exec.Command(probe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=time_base", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var parsed struct {
		Streams []struct {
			TimeBase string `json:"time_base"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("ffprobe parse %s: %v", path, err)
	}
	if len(parsed.Streams) == 0 {
		t.Fatalf("ffprobe found no video stream in %s", path)
	}
	return parsed.Streams[0].TimeBase
}

// bundledFFmpegOrSkip returns the bundled static ffmpeg shipped in
// the .deb when present, else the system ffmpeg.
func bundledFFmpegOrSkip(t *testing.T) string {
	t.Helper()
	if p := "/home/hatch/workspace/daybreak-nvr-linux/build/ffmpeg"; func() bool {
		_, err := os.Stat(p)
		return err == nil
	}() {
		return p
	}
	return ffmpegOrSkip(t)
}

// finalizedSegments returns the finalized (moov + duration)
// pre-roll segments in dir, sorted by name.
func finalizedSegments(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if !hasMoov(p) {
			continue
		}
		if d, ok := parseMP4Duration(p); !ok || d <= 0 {
			continue
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// runFFmpeg runs ffmpeg with args and fails the test on error.
func runFFmpeg(t *testing.T, ff string, args []string) {
	t.Helper()
	cmd := exec.Command(ff, args...)
	hideWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v: %v: %s", args, err, trimErr(string(out)))
	}
}

// TestConcatTimescaleMismatch reproduces the 2026-11-15 failure
// and pins the v1.8 fix. An event body recorded WITHOUT the
// timescale flag is written with the RTP clock (90000) while the
// pre-roll segments use 15360; the finalize concat then produces
// a file ~5.86x too long, so eventDurationOK drops the clip.
// The same body recorded WITH the flag concats to the correct
// duration. Both bodies and the segments are recorded with the
// production arg builders, against the same RTSP test stream.
func TestConcatTimescaleMismatch(t *testing.T) {
	url := startEventTestRTSP(t)
	ff := bundledFFmpegOrSkip(t)
	probe := ffprobePath(ff)
	dir := t.TempDir()
	cam := Camera{ID: "t1", Name: "Test Cam", MainURL: url}

	// Pre-roll segments, recorded with the production roll args.
	segDir := filepath.Join(dir, "segs")
	if err := os.MkdirAll(segDir, 0o755); err != nil {
		t.Fatalf("mkdir segs: %v", err)
	}
	segPattern := filepath.Join(segDir, "pre_%Y%m%d_%H%M%S.mp4")
	runFFmpeg(t, ff, withDuration(rollFFmpegArgs(cam, segPattern), "16"))
	segs := finalizedSegments(t, segDir)
	if len(segs) < 3 {
		t.Fatalf("only %d finalized pre-roll segments, want >= 3", len(segs))
	}
	segs = segs[:3]
	if tb := probeVideoTimeBase(t, probe, segs[0]); tb != "1/15360" {
		t.Fatalf("pre-roll segment time_base = %s, want 1/15360", tb)
	}

	// Event body WITHOUT the flag (pre-v1.8 behaviour).
	bodyBad := filepath.Join(dir, "body_noflag.mp4")
	runFFmpeg(t, ff, withDuration(withoutTimescale(eventFFmpegArgs(cam, bodyBad)), "8"))
	if tb := probeVideoTimeBase(t, probe, bodyBad); tb != "1/90000" {
		t.Fatalf("body without flag time_base = %s, want 1/90000 (RTP clock)", tb)
	}

	// Event body WITH the flag (v1.8).
	bodyGood := filepath.Join(dir, "body_flag.mp4")
	runFFmpeg(t, ff, withDuration(eventFFmpegArgs(cam, bodyGood), "8"))
	if tb := probeVideoTimeBase(t, probe, bodyGood); tb != "1/15360" {
		t.Fatalf("body with flag time_base = %s, want 1/15360", tb)
	}

	// The buggy concat: inflated, so the duration check drops it.
	badInputs := append(append([]string{}, segs...), bodyBad)
	var badExpected float64
	for _, p := range badInputs {
		d, ok := parseMP4Duration(p)
		if !ok {
			t.Fatalf("no duration for %s", p)
		}
		badExpected += d
	}
	badOut := filepath.Join(dir, "out_bad.mp4")
	if err := concatInto(ff, badInputs, badOut); err != nil {
		t.Fatalf("concat (no flag): %v", err)
	}
	badDur := probeClipDuration(t, probe, badOut)
	badRatio := badDur / badExpected
	t.Logf("no-flag concat: %.1fs out vs %.1fs expected (ratio %.2fx)", badDur, badExpected, badRatio)
	if eventDurationOK(badDur, badExpected) {
		t.Fatalf("no-flag concat passed the duration check (%.1fs vs %.1fs) — the bug is not reproduced", badDur, badExpected)
	}
	if badRatio < 2 {
		t.Fatalf("no-flag concat only inflated %.2fx, want a clear >2x inflation", badRatio)
	}

	// The fixed concat: correct duration, passes the check.
	goodInputs := append(append([]string{}, segs...), bodyGood)
	var goodExpected float64
	for _, p := range goodInputs {
		d, ok := parseMP4Duration(p)
		if !ok {
			t.Fatalf("no duration for %s", p)
		}
		goodExpected += d
	}
	goodOut := filepath.Join(dir, "out_good.mp4")
	if err := concatInto(ff, goodInputs, goodOut); err != nil {
		t.Fatalf("concat (flag): %v", err)
	}
	goodDur := probeClipDuration(t, probe, goodOut)
	t.Logf("flag concat: %.1fs out vs %.1fs expected (ratio %.3fx)", goodDur, goodExpected, goodDur/goodExpected)
	if !eventDurationOK(goodDur, goodExpected) {
		t.Fatalf("flag concat failed the duration check (%.1fs vs %.1fs)", goodDur, goodExpected)
	}
}

// fakeSleepFFmpeg writes a shell script that ignores its arguments
// and sleeps: a stand-in ffmpeg that stays alive but produces no
// output, for watchdog/pause tests (no camera needed).
func fakeSleepFFmpeg(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-ffmpeg")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexec sleep 300\n"), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return p
}

// fakeRecorderSetup builds an EventRecorder driven by a fake
// ffmpeg for one Motion-mode camera. The recorder is NOT started:
// the caller drives ticks manually with rec.tick(now), using a
// fabricated clock. Returns the recorder, its settings, the
// tracker and the recordings dir.
func fakeRecorderSetup(t *testing.T, pre, post int) (*EventRecorder, Settings, *MotionTracker, string) {
	t.Helper()
	dir := t.TempDir()
	s := DefaultSettings()
	s.RecordingsDir = dir
	s.PreBufferSeconds = pre
	s.PostBufferSeconds = post
	cam := Camera{ID: "t1", Name: "Test Cam", Enabled: true, Record: true,
		RecordMode: RecordMotion, MainURL: "rtsp://127.0.0.1:8554/testcam"}
	s.Cameras = []Camera{cam}
	tr := NewMotionTracker(filepath.Join(dir, ".motion-events.json"))
	rec := NewEventRecorder(fakeSleepFFmpeg(t), func() Settings { return s }, tr)
	return rec, s, tr, dir
}

// TestPreBufferWatchdogRestartsStalledRoll: a pre-buffer whose
// process is alive but has written no segment for >30 s is killed
// by the watchdog and restarted on the next tick, with a log line.
func TestPreBufferWatchdogRestartsStalledRoll(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "startup.log")
	startlog.InitFrom([]string{logPath})
	rec, _, _, _ := fakeRecorderSetup(t, 15, 120)
	defer rec.Stop()

	now := time.Now()
	rec.tick(now)
	st := rec.cams["t1"]
	if st == nil || st.rollProc == nil {
		t.Fatalf("pre-buffer did not start on the first tick")
	}
	first := st.rollProc

	// 31 s later, still no segments: the watchdog must kill it.
	rec.tick(now.Add(31 * time.Second))
	if st.rollProc != nil {
		t.Fatalf("watchdog did not kill the stalled pre-buffer")
	}

	// Next tick: a replacement must start promptly (backoff reset).
	rec.tick(now.Add(32 * time.Second))
	if st.rollProc == nil || st.rollProc == first {
		t.Fatalf("pre-buffer was not restarted after the watchdog kill")
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read startlog: %v", err)
	}
	if !strings.Contains(string(data), "stalled") {
		t.Fatalf("watchdog log line missing; log:\n%s", data)
	}
}

// TestEventPausesPreBuffer: when an event starts, the rolling
// pre-buffer stops (so the camera has only two RTSP connections
// during the event), and it runs again after the event finishes.
func TestEventPausesPreBuffer(t *testing.T) {
	rec, _, tr, _ := fakeRecorderSetup(t, 15, 2)
	defer rec.Stop()

	now := time.Now()
	rec.tick(now)
	st := rec.cams["t1"]
	if st == nil || st.rollProc == nil {
		t.Fatalf("pre-buffer did not start on the first tick")
	}

	// Open a motion event: the event starts and the pre-buffer
	// must be paused.
	tr.NoteHot("t1", now.Add(time.Second))
	rec.tick(now.Add(2 * time.Second))
	if st.rec == nil {
		t.Fatalf("event did not start")
	}
	if st.rollProc != nil {
		t.Fatalf("pre-buffer was not paused when the event started")
	}

	// Close the tracker event, then tick past the post buffer:
	// the event finishes, and the next tick restarts the buffer.
	tr.CloseStale(now.Add(3*time.Second), time.Second)
	rec.tick(now.Add(10 * time.Second))
	if st.rec != nil {
		t.Fatalf("event did not finish after the post buffer")
	}
	rec.tick(now.Add(11 * time.Second))
	if st.rollProc == nil {
		t.Fatalf("pre-buffer did not resume after the event finished")
	}
}
