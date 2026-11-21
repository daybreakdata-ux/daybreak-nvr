// Motion snapshots (v1.4): one still per motion/person event, taken
// when the motion is closest to the frame centre.
//
// The keeper holds the best frame seen while an event is open for a
// camera (best = centroid nearest (0.5, 0.5)) and writes it as a
// JPEG when the tracker closes the event. The snapshot lives beside
// its clips:
//
//	<RecordingsDir>/<slug>/<YYYY-MM-DD>/<slug>_<YYYYMMDD_HHMMSS>.jpg
//
// where the timestamp is the best frame's time. Snapshots are
// uploaded to the relay eagerly by the RemoteManager (they are
// tiny); retention deletes them with their clips because
// RunRetention walks every file in the recordings tree.
package engine

import (
	"encoding/json"
	"image"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/image/draw"
)

// SnapshotJPEGQuality is the JPEG quality for event snapshots.
const SnapshotJPEGQuality = 80

// SnapshotPathFor is the snapshot filename for a camera slug and a
// frame time (the best-frame time, not the event-close time).
func SnapshotPathFor(recordingsDir, slug string, t time.Time) string {
	day := t.Format("2006-01-02")
	name := slug + "_" + t.Format("20060102_150405") + ".jpg"
	return filepath.Join(recordingsDir, slug, day, name)
}

// snapCandidate is the best frame seen so far for one open event.
type snapCandidate struct {
	slug  string
	img   image.Image // a private copy of the pump frame
	at    time.Time
	dist  float64 // distance of the centroid from frame centre
}

// SnapshotKeeper keeps one best frame per camera while its motion
// event is open, and writes it out when the event closes.
type SnapshotKeeper struct {
	mu            sync.Mutex
	dir           string
	tracker       *MotionTracker
	best          map[string]*snapCandidate
}

// NewSnapshotKeeper creates a keeper for a recordings directory.
// tracker may be nil (no events are ever open, so nothing is kept).
func NewSnapshotKeeper(recordingsDir string, tracker *MotionTracker) *SnapshotKeeper {
	return &SnapshotKeeper{
		dir:     recordingsDir,
		tracker: tracker,
		best:    map[string]*snapCandidate{},
	}
}

// NoteFrame offers a motion frame to the keeper: if a motion event
// is open for the camera and this frame's centroid (cx, cy in 0..1
// frame coordinates) is nearer the frame centre than the frame
// already kept, it becomes the new best frame. Frames offered
// while no event is open are ignored.
func (k *SnapshotKeeper) NoteFrame(camID, slug string, img image.Image, cx, cy float64, at time.Time) {
	k.note(camID, slug, img, cx, cy, at)
}

// NotePersonFrame is NoteFrame for a person detection: the centroid
// is the centre of the person's box (already zone-gated by the
// caller via DetectionInZone).
func (k *SnapshotKeeper) NotePersonFrame(camID, slug string, img image.Image, cx, cy float32, at time.Time) {
	k.note(camID, slug, img, float64(cx), float64(cy), at)
}

func (k *SnapshotKeeper) note(camID, slug string, img image.Image, cx, cy float64, at time.Time) {
	if k == nil || img == nil {
		return
	}
	if k.tracker != nil && !k.tracker.Active(camID) {
		return
	}
	dist := math.Hypot(cx-0.5, cy-0.5)
	k.mu.Lock()
	defer k.mu.Unlock()
	if cur, ok := k.best[camID]; ok && cur.dist <= dist {
		return
	}
	// Copy the frame: the pump reuses its image buffer, so the
	// keeper must own its copy until the event closes.
	b := img.Bounds()
	cp := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(cp, cp.Bounds(), img, b.Min, draw.Src)
	k.best[camID] = &snapCandidate{slug: slug, img: cp, at: at, dist: dist}
}

// WriteClosed writes the kept snapshot for each closed event and
// clears the kept frame. Events with no kept frame (no frame was
// ever near-centre, or no frame arrived) write nothing. It returns
// the snapshot paths written.
func (k *SnapshotKeeper) WriteClosed(closed []ClosedEvent) []string {
	if k == nil {
		return nil
	}
	var written []string
	for _, c := range closed {
		k.mu.Lock()
		cand, ok := k.best[c.CamID]
		if ok {
			delete(k.best, c.CamID)
		}
		k.mu.Unlock()
		if !ok || cand == nil || cand.img == nil {
			continue
		}
		path := SnapshotPathFor(k.dir, cand.slug, cand.at)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			continue
		}
		f, err := os.Create(path)
		if err != nil {
			continue
		}
		_ = jpeg.Encode(f, cand.img, &jpeg.Options{Quality: SnapshotJPEGQuality})
		_ = f.Close()
		// Keep the file's modtime at the best-frame time so
		// retention (which is modtime-based) ages the snapshot
		// with its clip.
		_ = os.Chtimes(path, cand.at, cand.at)
		written = append(written, path)
	}
	return written
}

// KindForClip derives a clip's kind from tracker events overlapping
// its window [start, end): "person" if any overlapping event is a
// person event, else "motion" if any event overlaps, else "".
func KindForClip(events []MotionEvent, start, end time.Time) string {
	kind := ""
	for _, e := range events {
		if e.Start.Before(end) && e.End.After(start) {
			if e.Kind == "person" {
				return "person"
			}
			kind = "motion"
		}
	}
	return kind
}

// loadTrackerEvents reads the persisted motion-events file from the
// recordings root (written by MotionTracker.Save), so ScanLibrary
// can label clips with their kind without a live tracker. A
// missing or unreadable file means no kinds.
func loadTrackerEvents(root string) map[string][]MotionEvent {
	data, err := os.ReadFile(filepath.Join(root, ".motion-events.json"))
	if err != nil || len(data) == 0 {
		return nil
	}
	var out map[string][]MotionEvent
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}
