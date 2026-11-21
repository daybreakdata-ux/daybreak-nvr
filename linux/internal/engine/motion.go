package engine

import (
	"encoding/json"
	"image"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// triggerFractions maps a 0..100 sensitivity to the fraction of
// the (enabled-zone) analysis grid that must change between frames
// to count as motion. Piecewise-linear through points that
// reproduce the pre-v1.3 levels at the migrated values: 25 -> the
// old Low (0.04), 50 -> old Medium (0.015), 75 -> old High (0.005).
// Higher sensitivity = a smaller change triggers.
var triggerPoints = [][2]float64{
	{0, 0.06}, {25, 0.04}, {50, 0.015}, {75, 0.005}, {100, 0.002},
}

// Detector compares consecutive frames on a small grayscale grid and
// reports the fraction of cells that changed meaningfully. It is the
// classic frame-difference motion detector: cheap enough to run on
// every camera's sub-stream at a few frames per second.
//
// A detection zone (16x9 grid) restricts which cells count: only
// changes inside enabled zone cells are counted, and the fraction
// is computed over the enabled cells only, so a small zone still
// triggers on motion inside it. With no zone set the whole frame
// counts (the pre-v1.3 behaviour).
type Detector struct {
	w, h   int
	prev   []uint8
	primed bool
	mask   []bool // per-cell zone mask (nil = whole frame), len w*h
	// lastCentroid is the centroid (0..1 frame coordinates) of the
	// changed cells from the most recent ScoreCentroid call
	// (0.5, 0.5 when nothing changed).
	lastCX, lastCY float64
}

// NewDetector creates a detector with the standard 96x54 grid.
func NewDetector() *Detector {
	return &Detector{w: 96, h: 54, lastCX: 0.5, lastCY: 0.5}
}

// SetZone installs a zone grid (16x9 '0'/'1' string). An empty or
// malformed grid clears the zone (whole frame counts).
func (d *Detector) SetZone(grid string) {
	d.mask = ZoneMask(grid)
}

// Score returns the changed fraction (0..1) between img and the
// previous frame fed in, over the enabled zone only. The first
// frame scores 0.
func (d *Detector) Score(img image.Image) float64 {
	s, _, _ := d.ScoreCentroid(img)
	return s
}

// LastCentroid returns the centroid of the changed cells from the
// most recent Score/ScoreCentroid call (0.5, 0.5 when nothing
// changed), in 0..1 frame coordinates.
func (d *Detector) LastCentroid() (float64, float64) {
	return d.lastCX, d.lastCY
}

// ScoreCentroid is Score plus the centroid (mean x,y in 0..1 frame
// coordinates) of the changed cells inside the enabled zone. The
// snapshot keeper uses it to pick the frame where the motion was
// closest to the centre of the picture. With no changed cells the
// centroid is the frame centre (0.5, 0.5).
func (d *Detector) ScoreCentroid(img image.Image) (score, cx, cy float64) {
	if img == nil {
		return 0, 0.5, 0.5
	}
	cur := make([]uint8, d.w*d.h)
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return 0, 0.5, 0.5
	}
	for y := 0; y < d.h; y++ {
		sy := b.Min.Y + (y*sh)/d.h
		for x := 0; x < d.w; x++ {
			sx := b.Min.X + (x*sw)/d.w
			r, g, bl, _ := img.At(sx, sy).RGBA()
			cur[y*d.w+x] = uint8((299*(r>>8) + 587*(g>>8) + 114*(bl>>8)) / 1000)
		}
	}
	if !d.primed {
		d.prev = cur
		d.primed = true
		d.lastCX, d.lastCY = 0.5, 0.5
		return 0, 0.5, 0.5
	}
	changed, total := 0, 0
	var sumX, sumY float64
	for i := range cur {
		if d.mask != nil && !d.mask[i] {
			continue
		}
		total++
		diff := int(cur[i]) - int(d.prev[i])
		if diff < 0 {
			diff = -diff
		}
		if diff > 25 { // per-pixel noise floor
			changed++
			// Cell centre in 0..1 frame coordinates.
			sumX += (float64(i%d.w) + 0.5) / float64(d.w)
			sumY += (float64(i/d.w) + 0.5) / float64(d.h)
		}
	}
	d.prev = cur
	if total == 0 {
		d.lastCX, d.lastCY = 0.5, 0.5
		return 0, 0.5, 0.5
	}
	if changed == 0 {
		d.lastCX, d.lastCY = 0.5, 0.5
		return 0, 0.5, 0.5
	}
	d.lastCX = sumX / float64(changed)
	d.lastCY = sumY / float64(changed)
	return float64(changed) / float64(total), d.lastCX, d.lastCY
}

// Trigger returns the motion threshold for a 0..100 sensitivity.
func Trigger(sensitivity int) float64 {
	if sensitivity < 0 {
		sensitivity = 0
	}
	if sensitivity > 100 {
		sensitivity = 100
	}
	s := float64(sensitivity)
	for i := 0; i+1 < len(triggerPoints); i++ {
		x0, y0 := triggerPoints[i][0], triggerPoints[i][1]
		x1, y1 := triggerPoints[i+1][0], triggerPoints[i+1][1]
		if s == x0 {
			return y0
		}
		if s > x0 && s < x1 {
			return y0 + (y1-y0)*(s-x0)/(x1-x0)
		}
	}
	return triggerPoints[len(triggerPoints)-1][1]
}

// MotionEvent is one closed (or still open) span of motion.
// Kind is "" (plain motion, the pre-v1.3 form) or "person" for an
// on-device AI person detection; person events also carry the
// tracking ID and the detection confidence.
type MotionEvent struct {
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"` // zero while open
	Kind       string    `json:"kind,omitempty"`
	TrackID    int       `json:"track_id,omitempty"`
	Confidence float32   `json:"confidence,omitempty"`
}

// MotionTracker records motion events per camera, persisted to disk
// so a restart doesn't make the reaper forget recent motion.
type MotionTracker struct {
	mu     sync.Mutex
	events map[string][]MotionEvent
	open   map[string]time.Time
	path   string
	dirty  bool
}

// NewMotionTracker loads (or starts) the tracker stored at path.
func NewMotionTracker(path string) *MotionTracker {
	t := &MotionTracker{
		events: map[string][]MotionEvent{},
		open:   map[string]time.Time{},
		path:   path,
	}
	if data, err := os.ReadFile(path); err == nil {
		var saved map[string][]MotionEvent
		if json.Unmarshal(data, &saved) == nil && saved != nil {
			t.events = saved
		}
	}
	return t
}

// NoteHot records a hot (motion) frame at time now: it opens an
// event if none is open for the camera, and refreshes the open
// event's end.
func (t *MotionTracker) NoteHot(camID string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.open[camID]; !ok {
		t.open[camID] = now
		t.events[camID] = append(t.events[camID], MotionEvent{Start: now, End: now})
	} else if evs := t.events[camID]; len(evs) > 0 {
		evs[len(evs)-1].End = now
		t.events[camID] = evs
	}
	t.dirty = true
}

// NotePerson records a person detection at time now. A person
// detection is a recording trigger like motion: it opens (or
// refreshes) an event, labelled as a person event with the tracking
// ID and confidence, so motion-mode recording keeps the chunk and
// the event shows up labelled in the events list. A person event
// and a motion event for the same camera share one open slot —
// the tracker keeps a single open event per camera, and a person
// sighting refreshes it (upgrading its kind to "person" when the
// open event is still a plain motion event).
func (t *MotionTracker) NotePerson(camID string, trackID int, confidence float32, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.open[camID]; !ok {
		t.open[camID] = now
		t.events[camID] = append(t.events[camID], MotionEvent{
			Start: now, End: now, Kind: "person", TrackID: trackID, Confidence: confidence,
		})
	} else if evs := t.events[camID]; len(evs) > 0 {
		last := &evs[len(evs)-1]
		last.End = now
		if last.Kind != "person" {
			last.Kind = "person"
			last.TrackID = trackID
			last.Confidence = confidence
		} else if confidence > last.Confidence {
			last.TrackID = trackID
			last.Confidence = confidence
		}
		t.events[camID] = evs
	}
	t.dirty = true
}

// ClosedEvent is one motion event CloseStale just closed, so the
// snapshot keeper knows which camera's best frame to write out.
type ClosedEvent struct {
	CamID string
	Event MotionEvent
}

// CloseStale closes events whose last hot frame is older than the
// quiet window, and reports the events it closed.
func (t *MotionTracker) CloseStale(now time.Time, quiet time.Duration) []ClosedEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	var closed []ClosedEvent
	for camID := range t.open {
		evs := t.events[camID]
		if len(evs) == 0 {
			delete(t.open, camID)
			continue
		}
		if now.Sub(evs[len(evs)-1].End) >= quiet {
			closed = append(closed, ClosedEvent{CamID: camID, Event: evs[len(evs)-1]})
			delete(t.open, camID)
			t.dirty = true
		}
	}
	return closed
}

// OpenEvent returns the currently open event for a camera, if
// any (the last event, while the tracker still has the camera in
// its open set). The EventRecorder uses it to decide whether an
// event should be recording right now.
func (t *MotionTracker) OpenEvent(camID string) (MotionEvent, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.open[camID]; !ok {
		return MotionEvent{}, false
	}
	evs := t.events[camID]
	if len(evs) == 0 {
		return MotionEvent{}, false
	}
	return evs[len(evs)-1], true
}

// LastPersonEvent returns the most recent person event for a
// camera (open or closed), if any. The EventRecorder uses it for
// the post-buffer in Person-only mode, where a plain motion event
// must not extend a person recording.
func (t *MotionTracker) LastPersonEvent(camID string) (MotionEvent, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	evs := t.events[camID]
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind == "person" {
			return evs[i], true
		}
	}
	return MotionEvent{}, false
}

// Active reports whether a motion event is currently open.
func (t *MotionTracker) Active(camID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.open[camID]
	return ok
}

// LastMotion returns the end of the most recent event (zero if none).
func (t *MotionTracker) LastMotion(camID string) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	evs := t.events[camID]
	if len(evs) == 0 {
		return time.Time{}
	}
	return evs[len(evs)-1].End
}

// Events returns a copy of a camera's events.
func (t *MotionTracker) Events(camID string) []MotionEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]MotionEvent, len(t.events[camID]))
	copy(out, t.events[camID])
	return out
}

// Prune drops events that ended long ago (they can no longer protect
// any segment from the reaper).
func (t *MotionTracker) Prune(before time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for camID, evs := range t.events {
		kept := evs[:0]
		for _, e := range evs {
			if e.End.After(before) {
				kept = append(kept, e)
			}
		}
		t.events[camID] = kept
	}
	t.dirty = true
}

// Save persists events if anything changed.
func (t *MotionTracker) Save() {
	t.mu.Lock()
	if !t.dirty {
		t.mu.Unlock()
		return
	}
	t.dirty = false
	snapshot := make(map[string][]MotionEvent, len(t.events))
	for k, v := range t.events {
		cp := make([]MotionEvent, len(v))
		copy(cp, v)
		snapshot[k] = cp
	}
	t.mu.Unlock()
	data, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(t.path), 0o755)
	tmp := t.path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, t.path)
	}
}

// prePad / postPad are the event padding (v1.6: the EventRecorder
// reads them live from settings, never hard-coded), defaulting to
// the historical 15s / 20s when unset.
func prePad(s Settings) time.Duration {
	if s.PreBufferSeconds <= 0 {
		return 15 * time.Second
	}
	return time.Duration(s.PreBufferSeconds) * time.Second
}

func postPad(s Settings) time.Duration {
	if s.PostBufferSeconds <= 0 {
		return 20 * time.Second
	}
	return time.Duration(s.PostBufferSeconds) * time.Second
}

// Analyzer bundles a detector with per-camera state and feeds scores
// into a tracker. One analyzer per camera.
type Analyzer struct {
	det     *Detector
	trigger float64
	cx, cy  float64 // centroid from the most recent Observe call
}

// NewAnalyzer creates an analyzer for one camera's sensitivity
// (0..100) and detection zone grid ("" = whole frame).
func NewAnalyzer(sensitivity int, zoneGrid string) *Analyzer {
	d := NewDetector()
	d.SetZone(zoneGrid)
	return &Analyzer{det: d, trigger: Trigger(sensitivity)}
}

// Observe analyzes one frame; if it shows motion, the tracker is told.
func (a *Analyzer) Observe(tracker *MotionTracker, camID string, img image.Image, now time.Time) bool {
	hot, _, _ := a.ObserveCentroid(tracker, camID, img, now)
	return hot
}

// ObserveCentroid is Observe plus the motion centroid (0..1 frame
// coordinates) of the changed cells — the snapshot keeper's
// best-frame signal. The centroid is only meaningful when hot is
// true; otherwise it is the frame centre.
func (a *Analyzer) ObserveCentroid(tracker *MotionTracker, camID string, img image.Image, now time.Time) (hot bool, cx, cy float64) {
	score, cx, cy := a.det.ScoreCentroid(img)
	a.cx, a.cy = cx, cy
	if score >= a.trigger {
		tracker.NoteHot(camID, now)
		return true, cx, cy
	}
	return false, cx, cy
}

// Centroid returns the centroid from the most recent Observe call.
func (a *Analyzer) Centroid() (float64, float64) { return a.cx, a.cy }
