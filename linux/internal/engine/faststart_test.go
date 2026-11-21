package engine

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// topBoxTypes returns the top-level box types of an MP4 in order.
func topBoxTypes(t *testing.T, path string) []string {
	t.Helper()
	var types []string
	if err := walkTopLevelBoxes(path, func(typ string, _ uint64) bool {
		types = append(types, typ)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	return types
}

func TestMoovAtFront(t *testing.T) {
	dir := t.TempDir()
	// minimalMP4(true) is ftyp, mdat, moov — moov last, not front.
	p := filepath.Join(dir, "end.mp4")
	if err := os.WriteFile(p, minimalMP4(true), 0o644); err != nil {
		t.Fatal(err)
	}
	if moovAtFront(p) {
		t.Error("moovAtFront(moov-last file) = true, want false")
	}
	if moovAtFront(filepath.Join(dir, "missing.mp4")) {
		t.Error("moovAtFront(missing file) = true, want false")
	}
	// ftyp, moov, mdat — faststart layout.
	fast := filepath.Join(dir, "fast.mp4")
	var fastBuf bytes.Buffer
	writeTestBox(&fastBuf, "ftyp", []byte("isom\x00\x00\x00\x00isomiso2"))
	writeTestBox(&fastBuf, "moov", []byte{0x00, 0x00, 0x00, 0x00})
	writeTestBox(&fastBuf, "mdat", []byte{0x00, 0x00, 0x00, 0x01})
	if err := os.WriteFile(fast, fastBuf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if !moovAtFront(fast) {
		t.Error("moovAtFront(ftyp+moov+mdat) = false, want true")
	}
	// Unfinalized file (no moov at all) is not faststart either.
	unfin := filepath.Join(dir, "unfin.mp4")
	if err := os.WriteFile(unfin, minimalMP4(false), 0o644); err != nil {
		t.Fatal(err)
	}
	if moovAtFront(unfin) {
		t.Error("moovAtFront(no-moov file) = true, want false")
	}
}

// TestRemuxFaststart runs the real remux on a real ffmpeg-encoded
// segment: moov must move from the end of the file to right after
// ftyp, with the same duration and codecs (stream copy).
func TestRemuxFaststart(t *testing.T) {
	ff := ffmpegOrSkip(t)
	dir := t.TempDir()
	dayDir := filepath.Join(dir, "back-yard", "2026-11-14")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcPath := filepath.Join(dayDir, "back-yard_20261114_032600.mp4")
	// A real segment like the recorder writes: H264 video + AAC
	// audio, moov at the END (ffmpeg's default, no faststart).
	cmd := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=15:duration=4",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=4",
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "64k", "-shortest", srcPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("encode source: %v (%s)", err, out)
	}
	before := topBoxTypes(t, srcPath)
	if len(before) < 3 || before[len(before)-1] != "moov" {
		t.Fatalf("source box order = %v, want moov last", before)
	}
	if moovAtFront(srcPath) {
		t.Fatalf("source already faststart, box order = %v", before)
	}
	srcDur := probeDuration(t, ff, srcPath)

	m := newTestManager(RemoteModeOnDemand, &fakeSource{}, &fakePushes{running: map[string]bool{}, starts: map[string]int{}})
	m.ffmpeg = ff
	fastPath := m.remuxFaststart(srcPath)
	if fastPath == "" {
		t.Fatal("remuxFaststart returned empty, want a temp file")
	}
	defer os.Remove(fastPath)
	after := topBoxTypes(t, fastPath)
	if len(after) < 2 || after[0] != "ftyp" || after[1] != "moov" {
		t.Fatalf("remuxed box order = %v, want [ftyp moov ...]", after)
	}
	t.Logf("box order before: %v", before)
	t.Logf("box order after:  %v", after)
	if got := probeDuration(t, ff, fastPath); got != srcDur {
		t.Fatalf("remuxed duration = %q, want %q (source)", got, srcDur)
	}
	// Already faststart: no remux, no temp file.
	if again := m.remuxFaststart(fastPath); again != "" {
		os.Remove(again)
		t.Fatalf("remuxFaststart(faststart file) = %q, want empty", again)
	}
	// No ffmpeg configured: fall back (empty = upload original).
	m.ffmpeg = ""
	if again := m.remuxFaststart(srcPath); again != "" {
		os.Remove(again)
		t.Fatalf("remuxFaststart(no ffmpeg) = %q, want empty", again)
	}
	// The temp file must not look like a clip to ScanLibrary: only
	// the source is listed, with its own path (no duplicate token).
	clips := ScanLibrary(dir, []Camera{{Name: "Back yard"}})
	if len(clips) != 1 || clips[0].Path != srcPath {
		t.Fatalf("ScanLibrary = %v, want exactly the source clip", clips)
	}
}

func probeDuration(t *testing.T, ff, path string) string {
	t.Helper()
	fp := ffprobePath(ff)
	out, err := exec.Command(fp, "-v", "error",
		"-show_entries", "format=duration",
		"-show_entries", "stream=codec_name",
		"-of", "default=noprint_wrappers=1", path).CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe %s: %v (%s)", path, err, out)
	}
	s := string(out)
	if !strings.Contains(s, "h264") || !strings.Contains(s, "aac") {
		t.Fatalf("ffprobe %s missing h264/aac:\n%s", path, s)
	}
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "duration=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "duration="))
		}
	}
	t.Fatalf("ffprobe %s: no duration in:\n%s", path, s)
	return ""
}
