package engine

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- v1.4: motion centroid ---

func TestDetectorCentroid(t *testing.T) {
	black := solid(320, 180, color.RGBA{0, 0, 0, 255})
	d := NewDetector()
	_, cx, cy := d.ScoreCentroid(black)
	if cx != 0.5 || cy != 0.5 {
		t.Fatalf("priming frame centroid = (%v,%v), want (0.5,0.5)", cx, cy)
	}
	// White block in the left third of the frame.
	left := withBlock(black, 10, 60, 100, 120, color.RGBA{255, 255, 255, 255})
	s, cx, cy := d.ScoreCentroid(left)
	if s <= 0 {
		t.Fatal("left block should score > 0")
	}
	if cx > 0.4 || cy < 0.3 || cy > 0.7 {
		t.Fatalf("left block centroid = (%v,%v), want cx < 0.4, cy ~0.5", cx, cy)
	}
	// Same-sized block on the right: centroid must move right.
	d2 := NewDetector()
	_, _, _ = d2.ScoreCentroid(black)
	right := withBlock(black, 220, 60, 310, 120, color.RGBA{255, 255, 255, 255})
	_, cx2, _ := d2.ScoreCentroid(right)
	if cx2 < 0.6 {
		t.Fatalf("right block centroid cx = %v, want > 0.6", cx2)
	}
	// Analyzer exposes the same centroid.
	a := NewAnalyzer(50, "")
	tr := NewMotionTracker(t.TempDir() + "/m.json")
	if a.Observe(tr, "c1", black, time.Now()) {
		t.Fatal("priming frame should not trigger")
	}
	hot, cx3, _ := a.ObserveCentroid(tr, "c1", left, time.Now())
	if !hot {
		t.Fatal("left block should trigger at sensitivity 50")
	}
	if acx, _ := a.Centroid(); acx != cx3 {
		t.Fatalf("Analyzer.Centroid = %v, want %v", acx, cx3)
	}
}

// --- v1.4: mvhd duration parsing ---

func mp4WithMVHD(t *testing.T, version byte, timescale uint32, duration uint64) []byte {
	t.Helper()
	var mvhd bytes.Buffer
	payload := make([]byte, 100)
	payload[0] = version
	if version == 1 {
		binary.BigEndian.PutUint32(payload[20:24], timescale)
		binary.BigEndian.PutUint64(payload[24:32], duration)
	} else {
		binary.BigEndian.PutUint32(payload[12:16], timescale)
		binary.BigEndian.PutUint32(payload[16:20], uint32(duration))
	}
	writeTestBox(&mvhd, "mvhd", payload)
	var b bytes.Buffer
	writeTestBox(&b, "ftyp", []byte("isom\x00\x00\x00\x00isomiso2"))
	writeTestBox(&b, "mdat", []byte{0x00, 0x00, 0x00, 0x01})
	writeTestBox(&b, "moov", mvhd.Bytes())
	return b.Bytes()
}

func TestParseMP4Duration(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		data []byte
		want float64
		ok   bool
	}{
		{"v0 timescale 1000 dur 10540", mp4WithMVHD(t, 0, 1000, 10540), 10.54, true},
		{"v0 timescale 15360 dur 153600", mp4WithMVHD(t, 0, 15360, 153600), 10.0, true},
		{"v1 timescale 1000 dur 8000", mp4WithMVHD(t, 1, 1000, 8000), 8.0, true},
		{"no mvhd in moov", minimalMP4(true), 0, false},
		{"no moov at all", minimalMP4(false), 0, false},
	}
	for _, tc := range cases {
		p := filepath.Join(dir, tc.name+".mp4")
		if err := os.WriteFile(p, tc.data, 0o644); err != nil {
			t.Fatal(err)
		}
		got, ok := parseMP4Duration(p)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("%s: parseMP4Duration = (%v,%v), want (%v,%v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
	if _, ok := parseMP4Duration(filepath.Join(dir, "missing.mp4")); ok {
		t.Error("parseMP4Duration(missing) = true, want false")
	}
}

// --- v1.4: snapshot keeper ---

func TestSnapshotKeeperBestFrame(t *testing.T) {
	dir := t.TempDir()
	tr := NewMotionTracker(filepath.Join(dir, "motion.json"))
	k := NewSnapshotKeeper(dir, tr)
	camID, slug := "c1", "driveway"
	black := solid(640, 360, color.RGBA{0, 0, 0, 255})
	edge := withBlock(black, 10, 10, 100, 100, color.RGBA{255, 255, 255, 255})
	centre := withBlock(black, 270, 130, 370, 230, color.RGBA{255, 255, 255, 255})
	t0 := time.Now()

	// No event open: frames are ignored.
	k.NoteFrame(camID, slug, edge, 0.1, 0.1, t0)
	if got := k.WriteClosed([]ClosedEvent{{CamID: camID}}); len(got) != 0 {
		t.Fatalf("no open event: wrote %v", got)
	}

	// Open an event, offer an edge frame then a centred frame:
	// the centred one must win even though it came second.
	tr.NoteHot(camID, t0)
	k.NoteFrame(camID, slug, edge, 0.1, 0.1, t0.Add(time.Second))
	k.NoteFrame(camID, slug, centre, 0.5, 0.5, t0.Add(2*time.Second))
	// A later, worse frame must not displace it.
	k.NoteFrame(camID, slug, edge, 0.05, 0.05, t0.Add(3*time.Second))
	written := k.WriteClosed([]ClosedEvent{{CamID: camID, Event: tr.Events(camID)[0]}})
	if len(written) != 1 {
		t.Fatalf("wrote %d snapshots, want 1", len(written))
	}
	want := SnapshotPathFor(dir, slug, t0.Add(2*time.Second))
	if written[0] != want {
		t.Fatalf("snapshot = %s, want %s (best-frame time)", written[0], want)
	}
	if st, err := os.Stat(want); err != nil || st.Size() == 0 {
		t.Fatalf("snapshot file missing/empty: %v %v", st, err)
	}
	// Person frames count too, via the box centre.
	tr.NotePerson(camID, 7, 0.9, t0.Add(10*time.Second))
	k.NotePersonFrame(camID, slug, centre, 0.5, 0.5, t0.Add(11*time.Second))
	written = k.WriteClosed([]ClosedEvent{{CamID: camID}})
	if len(written) != 1 || written[0] != SnapshotPathFor(dir, slug, t0.Add(11*time.Second)) {
		t.Fatalf("person snapshot = %v", written)
	}
}

// --- v1.4: snapshot-to-clip matching, kind, duration, delete ---

func TestScanLibrarySnapshotKindDuration(t *testing.T) {
	root := t.TempDir()
	start := time.Date(2026, 11, 13, 15, 4, 5, 0, time.Local)
	// A finalized clip with a real mvhd (10.54s).
	dayDir := filepath.Join(root, "driveway", start.Format("2006-01-02"))
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	clipPath := filepath.Join(dayDir, "driveway_"+start.Format("20060102_150405")+".mp4")
	if err := os.WriteFile(clipPath, mp4WithMVHD(t, 0, 1000, 10540), 0o644); err != nil {
		t.Fatal(err)
	}
	// Snapshot 3s into the clip (best-frame time).
	snapTime := start.Add(3 * time.Second)
	snapPath := filepath.Join(dayDir, "driveway_"+snapTime.Format("20060102_150405")+".jpg")
	if err := os.WriteFile(snapPath, []byte("fake-jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A snapshot for a different camera must not attach.
	otherDir := filepath.Join(root, "garage", start.Format("2006-01-02"))
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Tracker events: a person event overlapping the clip.
	// Production keys this file by camera ID (MotionTracker saves
	// by ID), so the file must be keyed by ID here — a slug key
	// is only the fallback.
	events := map[string][]MotionEvent{
		"cam-driveway": {{Start: start.Add(2 * time.Second), End: start.Add(4 * time.Second), Kind: "person"}},
	}
	data, _ := json.Marshal(events)
	if err := os.WriteFile(filepath.Join(root, ".motion-events.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	cams := []Camera{{ID: "cam-driveway", Name: "Driveway"}, {ID: "cam-garage", Name: "Garage"}}
	clips := ScanLibrary(root, cams)
	if len(clips) != 1 {
		t.Fatalf("clips = %d, want 1", len(clips))
	}
	c := clips[0]
	if c.SnapshotPath != snapPath {
		t.Fatalf("SnapshotPath = %q, want %q", c.SnapshotPath, snapPath)
	}
	if c.Kind != "person" {
		t.Fatalf("Kind = %q, want %q", c.Kind, "person")
	}
	if c.DurationSec != 10.54 {
		t.Fatalf("DurationSec = %v, want 10.54", c.DurationSec)
	}

	// Fallback: a file keyed by slug still labels the clip.
	events = map[string][]MotionEvent{
		"driveway": {{Start: start.Add(2 * time.Second), End: start.Add(4 * time.Second), Kind: "person"}},
	}
	data, _ = json.Marshal(events)
	if err := os.WriteFile(filepath.Join(root, ".motion-events.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if clips := ScanLibrary(root, cams); len(clips) != 1 || clips[0].Kind != "person" {
		t.Fatalf("slug-keyed events: kind = %q, want person", clips[0].Kind)
	}

	// Deleting the clip also deletes its snapshot.
	if !DeleteClipFile(root, cams, ClipToken(c)) {
		t.Fatal("DeleteClipFile failed")
	}
	if _, err := os.Stat(clipPath); !os.IsNotExist(err) {
		t.Fatal("clip file still on disk")
	}
	if _, err := os.Stat(snapPath); !os.IsNotExist(err) {
		t.Fatal("snapshot still on disk after clip delete")
	}
}

func TestKindForClip(t *testing.T) {
	start := time.Now()
	end := start.Add(10 * time.Second)
	if got := KindForClip(nil, start, end); got != "" {
		t.Fatalf("no events: kind = %q", got)
	}
	motion := []MotionEvent{{Start: start.Add(2 * time.Second), End: start.Add(3 * time.Second)}}
	if got := KindForClip(motion, start, end); got != "motion" {
		t.Fatalf("motion event: kind = %q", got)
	}
	person := []MotionEvent{
		{Start: start.Add(-time.Hour), End: start.Add(-time.Hour + time.Second)}, // no overlap
		{Start: start.Add(5 * time.Second), End: start.Add(6 * time.Second), Kind: "person"},
	}
	if got := KindForClip(person, start, end); got != "person" {
		t.Fatalf("person event: kind = %q", got)
	}
	// An event that ends exactly at the clip start does not overlap.
	edge := []MotionEvent{{Start: start.Add(-5 * time.Second), End: start}}
	if got := KindForClip(edge, start, end); got != "" {
		t.Fatalf("touching event: kind = %q, want \"\"", got)
	}
}

func TestAutostartCreateRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	if AutostartEnabled() {
		t.Fatal("autostart should start disabled")
	}
	if err := SetAutostart(true); err != nil {
		t.Fatal(err)
	}
	if !AutostartEnabled() {
		t.Fatal("autostart should be enabled after SetAutostart(true)")
	}
	p := AutostartPath()
	if !strings.HasSuffix(p, ".config/autostart/daybreak-nvr.desktop") {
		t.Fatalf("AutostartPath = %q", p)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Exec=/usr/bin/daybreak-nvr\n") {
		t.Fatalf("autostart file wrong Exec: %s", data)
	}
	if err := SetAutostart(false); err != nil {
		t.Fatal(err)
	}
	if AutostartEnabled() {
		t.Fatal("autostart should be disabled after SetAutostart(false)")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("autostart file still on disk")
	}
	// Disabling twice is a no-op, not an error.
	if err := SetAutostart(false); err != nil {
		t.Fatalf("second disable: %v", err)
	}
}

// --- v1.4: eager snapshot upload ---

func TestManagerEagerSnapshotUpload(t *testing.T) {
	root := t.TempDir()
	start := time.Date(2026, 11, 13, 15, 4, 5, 0, time.Local)
	writeTestClip(t, root, "front-door", start)
	snapPath := filepath.Join(root, "front-door", start.Format("2006-01-02"),
		"front-door_"+start.Add(3*time.Second).Format("20060102_150405")+".jpg")
	if err := os.WriteFile(snapPath, []byte("fake-jpeg-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	settings := testSettings(RemoteModeOnDemand)
	settings.RecordingsDir = root
	src := &fakeSource{}
	fp := &fakePushes{running: map[string]bool{}, starts: map[string]int{}}
	m := NewRemoteManager("ffmpeg", func() Settings { return settings })
	m.newSource = func(Settings) demandSource { return src }
	m.runPush = fp.run
	m.pollInterval = time.Hour
	m.usersFunc = func() []User { return []User{{Username: "admin", PasswordHash: "hash1", Role: RoleAdmin}} }

	m.cycle()
	// The snapshot goes up eagerly with no clip request at all.
	for i := 0; i < 100; i++ {
		if src.snapUploadCount() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if src.snapUploadCount() != 1 {
		t.Fatalf("snapshot uploads = %d, want 1 (eager)", src.snapUploadCount())
	}
	src.mu.Lock()
	rec := src.snapUploads[0]
	src.mu.Unlock()
	tok := ClipToken(Clip{CameraSlug: "front-door", Start: start})
	if rec.token != tok || rec.size != int64(len("fake-jpeg-bytes")) {
		t.Fatalf("snapshot upload wrong: %+v (want token %s)", rec, tok)
	}
	// Next cycle: dedup — no second upload.
	m.cycle()
	time.Sleep(100 * time.Millisecond)
	if src.snapUploadCount() != 1 {
		t.Fatalf("snapshot uploads = %d, want 1 (dedup)", src.snapUploadCount())
	}
	// DropUpload clears the dedup, so the next cycle re-uploads.
	m.DropUpload(tok)
	m.cycle()
	for i := 0; i < 100; i++ {
		if src.snapUploadCount() >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if src.snapUploadCount() != 2 {
		t.Fatalf("snapshot uploads = %d, want 2 after DropUpload", src.snapUploadCount())
	}
	m.Stop()
}

func TestRelayClipIndexCarriesDurationKind(t *testing.T) {
	clips := []Clip{{CameraSlug: "front-door", CameraName: "Front Door",
		Start: time.Unix(1794612000, 0), Size: 1234, DurationSec: 10.5, Kind: "person"}}
	idx := relayClipIndex(clips)
	if len(idx) != 1 || idx[0].Duration != 10.5 || idx[0].Kind != "person" {
		t.Fatalf("relay index = %+v", idx)
	}
}
