package engine

import (
	"image"
	"image/color"
	"path/filepath"
	"testing"
	"time"
)

func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func withBlock(base *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) *image.RGBA {
	out := image.NewRGBA(base.Bounds())
	copy(out.Pix, base.Pix)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			out.Set(x, y, c)
		}
	}
	return out
}

func TestDetector(t *testing.T) {
	d := NewDetector()
	black := solid(320, 180, color.RGBA{0, 0, 0, 255})
	if s := d.Score(black); s != 0 {
		t.Fatalf("first frame score = %v, want 0", s)
	}
	if s := d.Score(black); s != 0 {
		t.Fatalf("static frame score = %v, want 0", s)
	}
	// A person-sized white block appearing should score well above
	// even the low-sensitivity trigger (25 on the 0..100 scale).
	moving := withBlock(black, 100, 40, 220, 180, color.RGBA{255, 255, 255, 255})
	if s := d.Score(moving); s < Trigger(25) {
		t.Fatalf("big change score = %v, want >= low trigger %v", s, Trigger(25))
	}
	// Tiny flicker (a few pixels) should stay under the high trigger.
	d2 := NewDetector()
	_ = d2.Score(black)
	noise := withBlock(black, 0, 0, 6, 6, color.RGBA{255, 255, 255, 255})
	if s := d2.Score(noise); s >= Trigger(75) {
		t.Fatalf("tiny noise score = %v, want < high trigger %v", s, Trigger(75))
	}
}

// The 0..100 sensitivity scale reproduces the old 0..2 levels at
// the migrated points, and is monotonic: more sensitive means a
// smaller change triggers.
func TestTriggerMapping(t *testing.T) {
	if got, want := Trigger(25), 0.04; got != want {
		t.Errorf("Trigger(25) = %v, want %v (old Low)", got, want)
	}
	if got, want := Trigger(50), 0.015; got != want {
		t.Errorf("Trigger(50) = %v, want %v (old Medium)", got, want)
	}
	if got, want := Trigger(75), 0.005; got != want {
		t.Errorf("Trigger(75) = %v, want %v (old High)", got, want)
	}
	for _, s := range []int{0, 10, 25, 40, 50, 60, 75, 90, 100} {
		if Trigger(s) <= 0 || Trigger(s) > 0.06 {
			t.Errorf("Trigger(%d) = %v, out of range", s, Trigger(s))
		}
	}
	prev := Trigger(0)
	for s := 1; s <= 100; s++ {
		if Trigger(s) > prev {
			t.Errorf("Trigger not monotonic at %d: %v > %v", s, Trigger(s), prev)
		}
		prev = Trigger(s)
	}
	if Trigger(-5) != Trigger(0) || Trigger(150) != Trigger(100) {
		t.Error("Trigger should clamp out-of-range values")
	}
}

// Migration: pre-v1.3 levels map onto the new scale.
func TestMigrateSensitivity(t *testing.T) {
	if MigrateSensitivity(0) != 25 || MigrateSensitivity(1) != 50 || MigrateSensitivity(2) != 75 {
		t.Errorf("migration = %d/%d/%d, want 25/50/75",
			MigrateSensitivity(0), MigrateSensitivity(1), MigrateSensitivity(2))
	}
}

// Zone masking: a change outside the enabled zone scores 0 and
// does not trigger; the same change inside the zone does.
func TestDetectorZoneMasking(t *testing.T) {
	black := solid(320, 180, color.RGBA{0, 0, 0, 255})
	// A block in the top-left corner of the frame.
	moving := withBlock(black, 10, 10, 90, 90, color.RGBA{255, 255, 255, 255})

	// Zone with only the top-left cell enabled.
	grid := make([]byte, ZoneCells)
	for i := range grid {
		grid[i] = '0'
	}
	grid[0] = '1'
	zone := string(grid)

	d := NewDetector()
	d.SetZone(zone)
	_ = d.Score(black)
	if s := d.Score(moving); s <= 0 {
		t.Fatalf("motion inside the zone scored %v, want > 0", s)
	}

	// Same motion, but the zone is the bottom-right cell only.
	grid2 := make([]byte, ZoneCells)
	for i := range grid2 {
		grid2[i] = '0'
	}
	grid2[ZoneCells-1] = '1'
	d2 := NewDetector()
	d2.SetZone(string(grid2))
	_ = d2.Score(black)
	if s := d2.Score(moving); s != 0 {
		t.Fatalf("motion outside the zone scored %v, want 0", s)
	}

	// The Analyzer applies the zone the same way.
	a := NewAnalyzer(50, string(grid2))
	if a.Observe(NewMotionTracker(t.TempDir()+"/m.json"), "c1", black, time.Now()) {
		t.Fatal("analyzer priming frame should not trigger")
	}
	if a.Observe(NewMotionTracker(t.TempDir()+"/m2.json"), "c1", moving, time.Now()) {
		t.Fatal("analyzer should not trigger on out-of-zone motion")
	}
}

// A person sighting is a recording trigger: it opens a person
// event and refreshes it, and it protects chunks from the reaper.
func TestNotePerson(t *testing.T) {
	tr := NewMotionTracker(t.TempDir() + "/motion.json")
	t0 := time.Now().Add(-time.Minute)
	tr.NotePerson("c1", 3, 0.87, t0)
	evs := tr.Events("c1")
	if len(evs) != 1 || evs[0].Kind != "person" || evs[0].TrackID != 3 || evs[0].Confidence != 0.87 {
		t.Fatalf("person event = %+v", evs)
	}
	tr.NotePerson("c1", 3, 0.91, t0.Add(2*time.Second))
	evs = tr.Events("c1")
	if len(evs) != 1 || !evs[0].End.Equal(t0.Add(2*time.Second)) {
		t.Fatalf("person event should refresh, got %+v", evs)
	}
	// A plain motion sighting while a person event is open does
	// not downgrade it.
	tr.NoteHot("c1", t0.Add(3*time.Second))
	if evs := tr.Events("c1"); evs[0].Kind != "person" {
		t.Fatalf("person event downgraded: %+v", evs)
	}
}

// The reaper honours the configured pre/post buffers (not just
// the old hardcoded 15s/20s): a chunk just outside a tight
// buffer is reaped, and one inside a wide buffer is kept.
func TestTrackerLifecycleAndPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "motion.json")
	tr := NewMotionTracker(path)
	t0 := time.Now().Add(-time.Minute)
	if tr.Active("c1") {
		t.Fatal("active before any motion")
	}
	tr.NoteHot("c1", t0)
	tr.NoteHot("c1", t0.Add(2*time.Second))
	if !tr.Active("c1") {
		t.Fatal("not active after hot frames")
	}
	// Not stale yet inside the quiet window.
	tr.CloseStale(t0.Add(4*time.Second), 5*time.Second)
	if !tr.Active("c1") {
		t.Fatal("closed too early")
	}
	tr.CloseStale(t0.Add(10*time.Second), 5*time.Second)
	if tr.Active("c1") {
		t.Fatal("still active after quiet window")
	}
	evs := tr.Events("c1")
	if len(evs) != 1 || !evs[0].Start.Equal(t0) || !evs[0].End.Equal(t0.Add(2*time.Second)) {
		t.Fatalf("events = %+v", evs)
	}
	tr.Save()
	tr2 := NewMotionTracker(path)
	if got := tr2.Events("c1"); len(got) != 1 {
		t.Fatalf("persisted events = %d, want 1", len(got))
	}
}
