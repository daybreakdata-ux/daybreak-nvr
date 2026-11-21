package engine

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTestBox appends one top-level MP4 box (8-byte header + payload).
func writeTestBox(b *bytes.Buffer, typ string, payload []byte) {
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:4], uint32(8+len(payload)))
	copy(hdr[4:8], typ)
	b.Write(hdr[:])
	b.Write(payload)
}

// minimalMP4 builds a tiny MP4: ftyp + a small mdat, plus a moov box
// when finalized is true. An unfinalized file (no moov) is what
// ffmpeg leaves behind when a segment is still recording or the app
// stopped mid-segment.
func minimalMP4(finalized bool) []byte {
	var b bytes.Buffer
	writeTestBox(&b, "ftyp", []byte("isom\x00\x00\x00\x00isomiso2"))
	writeTestBox(&b, "mdat", []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88, 0x84, 0x00})
	if finalized {
		writeTestBox(&b, "moov", []byte{0x00, 0x00, 0x00, 0x00})
	}
	return b.Bytes()
}

func TestHasMoov(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"finalized ftyp+mdat+moov", minimalMP4(true), true},
		{"unfinalized ftyp+mdat, no moov", minimalMP4(false), false},
		{"empty file", []byte{}, false},
		{"truncated header", []byte{0x00, 0x00, 0x00}, false},
		{"garbage, no boxes", []byte("fake-mp4-bytes"), false},
		{"moov first", func() []byte {
			var b bytes.Buffer
			writeTestBox(&b, "moov", []byte{0x00})
			writeTestBox(&b, "mdat", []byte{0x00})
			return b.Bytes()
		}(), true},
		{"moov with 64-bit largesize", func() []byte {
			var b bytes.Buffer
			writeTestBox(&b, "ftyp", []byte("isom\x00\x00\x00\x00isomiso2"))
			// moov box: size==1, largesize 24 (16 header + 8 payload).
			var hdr [8]byte
			binary.BigEndian.PutUint32(hdr[0:4], 1)
			copy(hdr[4:8], "moov")
			b.Write(hdr[:])
			var ext [8]byte
			binary.BigEndian.PutUint64(ext[:], 24)
			b.Write(ext[:])
			b.Write([]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
			return b.Bytes()
		}(), true},
		{"mdat size 0 (to EOF), no moov after", func() []byte {
			var b bytes.Buffer
			writeTestBox(&b, "ftyp", []byte("isom\x00\x00\x00\x00isomiso2"))
			var hdr [8]byte
			binary.BigEndian.PutUint32(hdr[0:4], 0)
			copy(hdr[4:8], "mdat")
			b.Write(hdr[:])
			b.Write([]byte{0x00, 0x00, 0x00, 0x00})
			return b.Bytes()
		}(), false},
	}
	for _, tc := range cases {
		p := filepath.Join(dir, tc.name+".mp4")
		if err := os.WriteFile(p, tc.data, 0o644); err != nil {
			t.Fatal(err)
		}
		if got := hasMoov(p); got != tc.want {
			t.Errorf("%s: hasMoov = %v, want %v", tc.name, got, tc.want)
		}
	}
	if hasMoov(filepath.Join(dir, "does-not-exist.mp4")) {
		t.Error("hasMoov(missing file) = true, want false")
	}
}

// TestHasMoovSkipsHugeMdat proves the check seeks past a huge mdat
// by its declared size instead of reading the file: a sparse file
// whose mdat claims 40 MB, with moov right after it.
func TestHasMoovSkipsHugeMdat(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sparse.mp4")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:4], 24)
	copy(hdr[4:8], "ftyp")
	if _, err := f.Write(hdr[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("isom\x00\x00\x00\x00isomiso2")); err != nil {
		t.Fatal(err)
	}
	// mdat claims 40 MB; the file stays sparse (only headers on disk).
	binary.BigEndian.PutUint32(hdr[0:4], 40_000_000)
	copy(hdr[4:8], "mdat")
	if _, err := f.Write(hdr[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(24+40_000_000, 0); err != nil {
		t.Fatal(err)
	}
	var moov bytes.Buffer
	writeTestBox(&moov, "moov", []byte{0x00, 0x00, 0x00, 0x00})
	if _, err := f.Write(moov.Bytes()); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if !hasMoov(p) {
		t.Fatal("hasMoov(sparse 40 MB mdat + moov) = false, want true")
	}
	if st, err := os.Stat(p); err != nil || st.Size() < 40_000_000 {
		t.Fatalf("sparse test file wrong size: %v, %v", st.Size(), err)
	}
}

func TestScanLibrarySkipsUnfinalized(t *testing.T) {
	root := t.TempDir()
	start := time.Date(2026, 11, 14, 3, 26, 0, 0, time.Local)
	// Finalized (playable) driveway clip.
	p1 := writeTestClip(t, root, "driveway", start)
	// Unfinalized back-yard clip: same layout, no moov atom.
	dir := filepath.Join(root, "back-yard", start.Format("2006-01-02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p2 := filepath.Join(dir, "back-yard_"+start.Format("20060102_150405")+".mp4")
	if err := os.WriteFile(p2, minimalMP4(false), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = p1
	cams := []Camera{{Name: "Driveway"}, {Name: "Back yard"}}
	clips := ScanLibrary(root, cams)
	if len(clips) != 1 {
		t.Fatalf("ScanLibrary returned %d clips, want 1 (the finalized one): %v", len(clips), clips)
	}
	if clips[0].CameraSlug != "driveway" {
		t.Fatalf("listed clip slug = %q, want %q", clips[0].CameraSlug, "driveway")
	}
}

// TestMaybeUploadClipSkipsUnfinalized exercises the defense-in-depth
// guard: even if a stale library entry still points at a segment
// with no moov atom, a viewer request for it must not upload.
func TestMaybeUploadClipSkipsUnfinalized(t *testing.T) {
	root := t.TempDir()
	start := time.Date(2026, 11, 14, 3, 36, 0, 0, time.Local)
	dir := filepath.Join(root, "back-yard", start.Format("2006-01-02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "back-yard_"+start.Format("20060102_150405")+".mp4")
	if err := os.WriteFile(p, minimalMP4(false), 0o644); err != nil {
		t.Fatal(err)
	}
	settings := testSettings(RemoteModeOnDemand)
	settings.RecordingsDir = root
	src := &fakeSource{}
	fp := &fakePushes{running: map[string]bool{}, starts: map[string]int{}}
	m := newTestManager(RemoteModeOnDemand, src, fp)
	// Seed a stale cache entry for the unfinalized file, as if the
	// clip index was built before the moov check existed.
	stale := Clip{CameraSlug: "back-yard", CameraName: "Back yard", Path: p, Start: start}
	m.clipCache = []Clip{stale}
	m.clipCacheAt = time.Now()
	tok := ClipToken(stale)
	m.maybeUploadClip(settings, ClipRequest{Token: tok, Slug: "back-yard", Start: start.Unix()})
	time.Sleep(200 * time.Millisecond)
	if src.uploadCount() != 0 {
		t.Fatalf("uploads = %d, want 0 (unfinalized clip must not upload)", src.uploadCount())
	}
	if st := m.Status(); len(st.Uploading) != 0 {
		t.Fatalf("Uploading = %v, want empty", st.Uploading)
	}
	m.Stop()
}
