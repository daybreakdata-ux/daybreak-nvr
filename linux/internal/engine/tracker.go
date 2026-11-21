// Person tracking (v1.3): associates per-frame person detections
// into persistent tracks so one person walking across the frame
// keeps one stable ID for as long as they stay visible.
//
// Greedy association: each new detection (highest confidence
// first) is matched to the unmatched live track whose last box
// overlaps it most (IoU), falling back to centre distance when a
// fast mover leaves no overlap between sampled frames. A track
// that has not been seen for StaleAfter expires and its ID is
// retired.
package engine

import (
	"sort"
	"sync"
	"time"
)

// TrackStaleAfter is how long a person can go unseen before
// their track expires.
const TrackStaleAfter = 5 * time.Second

// Track is one person being followed.
type Track struct {
	ID         int       `json:"id"`
	Box        Detection `json:"box"`
	FirstSeen  time.Time `json:"first_seen"`
	LastSeen   time.Time `json:"last_seen"`
	Confidence float32   `json:"confidence"`
}

// PersonTracker keeps the live tracks for one camera.
type PersonTracker struct {
	mu         sync.Mutex
	tracks     map[int]*Track
	nextID     int
	staleAfter time.Duration
}

// NewPersonTracker creates a tracker with the default expiry.
func NewPersonTracker() *PersonTracker {
	return &PersonTracker{tracks: map[int]*Track{}, staleAfter: TrackStaleAfter}
}

// Update matches detections against the live tracks and returns
// the tracks that are live after this frame (sorted by ID).
// Detections that match no track start a new track with a fresh ID.
func (pt *PersonTracker) Update(dets []Detection, now time.Time) []Track {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.expireLocked(now)
	// Match highest-confidence detections first.
	ordered := append([]Detection(nil), dets...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Confidence > ordered[j].Confidence })
	used := map[int]bool{}
	for _, d := range ordered {
		bestID, bestScore := -1, float32(0)
		for id, tr := range pt.tracks {
			if used[id] {
				continue
			}
			if s := matchScore(d, tr.Box); s > bestScore {
				bestScore, bestID = s, id
			}
		}
		if bestID >= 0 {
			used[bestID] = true
			tr := pt.tracks[bestID]
			tr.Box = d
			tr.LastSeen = now
			if d.Confidence > tr.Confidence {
				tr.Confidence = d.Confidence
			}
			continue
		}
		pt.nextID++
		id := pt.nextID
		pt.tracks[id] = &Track{ID: id, Box: d, FirstSeen: now, LastSeen: now, Confidence: d.Confidence}
		used[id] = true
	}
	return pt.activeLocked()
}

// Active returns the currently live tracks (sorted by ID).
func (pt *PersonTracker) Active(now time.Time) []Track {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.expireLocked(now)
	return pt.activeLocked()
}

func (pt *PersonTracker) expireLocked(now time.Time) {
	for id, tr := range pt.tracks {
		if now.Sub(tr.LastSeen) > pt.staleAfter {
			delete(pt.tracks, id)
		}
	}
}

func (pt *PersonTracker) activeLocked() []Track {
	out := make([]Track, 0, len(pt.tracks))
	for _, tr := range pt.tracks {
		out = append(out, *tr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// matchScore ranks how well a detection fits a track's last box.
// Overlap (IoU) wins when there is any; otherwise a close centre
// (a fast mover between sampled frames) is a weaker match.
func matchScore(d, last Detection) float32 {
	if iou := personIoU(d, last); iou > 0.05 {
		return 1 + iou // any overlap beats a centre-only match
	}
	cx1, cy1 := d.Center()
	cx2, cy2 := last.Center()
	dx, dy := cx1-cx2, cy1-cy2
	dist := sqrt32(dx*dx + dy*dy)
	if dist < 0.25 {
		return 0.5 - dist
	}
	return 0
}

func sqrt32(v float32) float32 {
	// Newton iterations — enough for a match score; avoids
	// pulling math into a float32 helper.
	if v <= 0 {
		return 0
	}
	x := v
	for i := 0; i < 8; i++ {
		x = (x + v/x) / 2
	}
	return x
}
