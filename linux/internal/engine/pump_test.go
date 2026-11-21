// Tests for the v1.8 detector watchdog: a pump whose ffmpeg is
// alive but produces no frames is stalled, and syncPumps restarts
// it. Stalled() is time-based, so the tests fabricate the pump's
// start/last-frame times directly (same package).
package engine

import (
	"testing"
	"time"
)

func TestPumpStalled(t *testing.T) {
	// Fresh pump: never stalled (startup grace).
	fresh := &Pump{startedAt: time.Now()}
	if fresh.Stalled(30 * time.Second) {
		t.Fatal("fresh pump reported stalled")
	}
	if !fresh.LastFrameTime().IsZero() {
		t.Fatal("fresh pump has a last-frame time with no frames")
	}

	// Old pump, no frames ever: stalled.
	old := &Pump{startedAt: time.Now().Add(-31 * time.Second)}
	if !old.Stalled(30 * time.Second) {
		t.Fatal("old pump with no frames did not report stalled")
	}

	// Old pump, recent frame: not stalled.
	recent := &Pump{startedAt: time.Now().Add(-60 * time.Second)}
	recent.lastFrame.Store(time.Now().UnixNano())
	if recent.Stalled(30 * time.Second) {
		t.Fatal("pump with a recent frame reported stalled")
	}
	if recent.LastFrameTime().IsZero() {
		t.Fatal("pump with a frame has a zero last-frame time")
	}

	// Old pump, stale frame: stalled.
	stale := &Pump{startedAt: time.Now().Add(-60 * time.Second)}
	stale.lastFrame.Store(time.Now().Add(-31 * time.Second).UnixNano())
	if !stale.Stalled(30 * time.Second) {
		t.Fatal("pump with a stale frame did not report stalled")
	}

	// Stopped pump: never stalled (its process already exited;
	// ensurePumpLocked restarts those on its own).
	stopped := &Pump{startedAt: time.Now().Add(-60 * time.Second)}
	stopped.stopped.Store(true)
	if stopped.Stalled(30 * time.Second) {
		t.Fatal("stopped pump reported stalled")
	}
}
