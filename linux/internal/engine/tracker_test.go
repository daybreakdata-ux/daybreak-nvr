package engine

import (
	"testing"
	"time"
)

func det(x1, y1, x2, y2, conf float32) Detection {
	return Detection{X1: x1, Y1: y1, X2: x2, Y2: y2, Confidence: conf}
}

// One person walking keeps one ID across frames; a second person
// gets a second ID.
func TestTrackerAssociation(t *testing.T) {
	pt := NewPersonTracker()
	t0 := time.Now()
	// One person, centre-left.
	tracks := pt.Update([]Detection{det(0.10, 0.20, 0.20, 0.60, 0.8)}, t0)
	if len(tracks) != 1 || tracks[0].ID != 1 {
		t.Fatalf("first frame tracks = %+v, want one track ID 1", tracks)
	}
	// Same person, shifted a little.
	tracks = pt.Update([]Detection{det(0.12, 0.20, 0.22, 0.60, 0.85)}, t0.Add(time.Second))
	if len(tracks) != 1 || tracks[0].ID != 1 {
		t.Fatalf("moved person should keep ID 1, got %+v", tracks)
	}
	// A second person appears on the right.
	tracks = pt.Update([]Detection{
		det(0.13, 0.20, 0.23, 0.60, 0.85),
		det(0.70, 0.20, 0.80, 0.60, 0.9),
	}, t0.Add(2*time.Second))
	if len(tracks) != 2 {
		t.Fatalf("two people should be two tracks, got %+v", tracks)
	}
	ids := map[int]bool{}
	for _, tr := range tracks {
		ids[tr.ID] = true
	}
	if !ids[1] || !ids[2] {
		t.Errorf("track IDs = %v, want {1, 2}", ids)
	}
	// Both keep their IDs next frame.
	tracks = pt.Update([]Detection{
		det(0.14, 0.20, 0.24, 0.60, 0.85),
		det(0.71, 0.20, 0.81, 0.60, 0.9),
	}, t0.Add(3*time.Second))
	if len(tracks) != 2 {
		t.Fatalf("two people should stay two tracks, got %+v", tracks)
	}
}

// A track that goes unseen past the stale window expires, and a
// returning person gets a fresh ID.
func TestTrackerExpiry(t *testing.T) {
	pt := NewPersonTracker()
	t0 := time.Now()
	pt.Update([]Detection{det(0.10, 0.20, 0.20, 0.60, 0.8)}, t0)
	// Still alive just inside the window.
	if got := pt.Active(t0.Add(TrackStaleAfter - time.Second)); len(got) != 1 {
		t.Fatalf("track expired too early: %+v", got)
	}
	// Gone after the window.
	if got := pt.Active(t0.Add(TrackStaleAfter + time.Second)); len(got) != 0 {
		t.Fatalf("stale track still active: %+v", got)
	}
	// A person reappearing after expiry is a new track.
	tracks := pt.Update([]Detection{det(0.10, 0.20, 0.20, 0.60, 0.8)}, t0.Add(TrackStaleAfter+2*time.Second))
	if len(tracks) != 1 || tracks[0].ID != 2 {
		t.Fatalf("reappearing person should be new ID 2, got %+v", tracks)
	}
}

// A fast mover with no box overlap between sampled frames is
// still matched by centre distance.
func TestTrackerFastMover(t *testing.T) {
	pt := NewPersonTracker()
	t0 := time.Now()
	pt.Update([]Detection{det(0.10, 0.20, 0.18, 0.60, 0.8)}, t0)
	tracks := pt.Update([]Detection{det(0.20, 0.20, 0.28, 0.60, 0.8)}, t0.Add(time.Second))
	if len(tracks) != 1 || tracks[0].ID != 1 {
		t.Fatalf("fast mover should keep ID 1, got %+v", tracks)
	}
	// A person on the far side of the frame is a different person.
	tracks = pt.Update([]Detection{
		det(0.22, 0.20, 0.30, 0.60, 0.8),
		det(0.80, 0.20, 0.88, 0.60, 0.8),
	}, t0.Add(2*time.Second))
	if len(tracks) != 2 {
		t.Fatalf("far person should be a new track, got %+v", tracks)
	}
}
