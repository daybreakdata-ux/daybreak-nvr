// Tests for the v1.6 zone picker mapping: the overlaid grid's
// cell (col,row) must map to the correct character index in the
// 144-char zone string, and the string must round-trip through
// the picker logic unchanged.
package engine

import (
	"strings"
	"testing"
)

func TestZoneCellIndex(t *testing.T) {
	// Row-major: index = row*16 + col.
	if got := ZoneCellIndex(0, 0); got != 0 {
		t.Fatalf("ZoneCellIndex(0,0) = %d, want 0", got)
	}
	if got := ZoneCellIndex(15, 0); got != 15 {
		t.Fatalf("ZoneCellIndex(15,0) = %d, want 15", got)
	}
	if got := ZoneCellIndex(0, 1); got != 16 {
		t.Fatalf("ZoneCellIndex(0,1) = %d, want 16", got)
	}
	if got := ZoneCellIndex(15, 8); got != 143 {
		t.Fatalf("ZoneCellIndex(15,8) = %d, want 143", got)
	}
	if got := ZoneCellIndex(7, 4); got != 71 {
		t.Fatalf("ZoneCellIndex(7,4) = %d, want 71", got)
	}
	// ZoneCellIndex is a pure row-major mapping (row*16+col);
	// out-of-range cells are guarded by ZoneToggle, not here.
	if got := ZoneCellIndex(16, 0); got != 16 {
		t.Fatalf("ZoneCellIndex(16,0) = %d, want 16 (pure mapping)", got)
	}
	if got := ZoneCellIndex(0, 9); got != 144 {
		t.Fatalf("ZoneCellIndex(0,9) = %d, want 144 (pure mapping)", got)
	}
}

func TestZoneToggle(t *testing.T) {
	grid := DefaultZoneGrid()
	// Toggle the top-left cell off: only index 0 changes.
	off := ZoneToggle(grid, 0, 0)
	if off[0] != '0' || off[1:] != grid[1:] {
		t.Fatal("ZoneToggle(0,0) did not flip only the first cell")
	}
	// Toggle it back on.
	if got := ZoneToggle(off, 0, 0); got != grid {
		t.Fatal("ZoneToggle(0,0) twice did not restore the grid")
	}
	// Toggle the bottom-right cell: only index 143 changes.
	br := ZoneToggle(grid, 15, 8)
	if br[143] != '0' || br[:143] != grid[:143] {
		t.Fatal("ZoneToggle(15,8) did not flip only the last cell")
	}
	// Toggle a middle cell (col 7, row 4 → index 71).
	mid := ZoneToggle(grid, 7, 4)
	if mid[71] != '0' || mid[:71] != grid[:71] || mid[72:] != grid[72:] {
		t.Fatal("ZoneToggle(7,4) did not flip only index 71")
	}
	// Out-of-range taps are ignored (unchanged grid).
	if got := ZoneToggle(grid, 16, 0); got != grid {
		t.Fatal("ZoneToggle(16,0) changed the grid")
	}
	if got := ZoneToggle(grid, 0, 9); got != grid {
		t.Fatal("ZoneToggle(0,9) changed the grid")
	}
	// A malformed grid is repaired to the default (fail-open:
	// the whole frame stays on rather than silently clearing
	// the zone), and the toggle then applies.
	if got := ZoneToggle("short", 0, 0); got[0] != '0' || len(got) != ZoneCells {
		t.Fatalf("ZoneToggle on a malformed grid = %q (len %d), want a 144-char grid with cell 0 off", got, len(got))
	}
}

func TestZoneGridRoundTrip(t *testing.T) {
	// A saved zone (e.g. only the driveway half on) must load
	// and display exactly: toggling a cell and saving must
	// produce the same string the picker shows.
	orig := strings.Repeat("1", 72) + strings.Repeat("0", 72)
	if len(orig) != ZoneCells {
		t.Fatalf("test grid len %d", len(orig))
	}
	// Simulate the picker: read the grid, toggle one cell off,
	// toggle another on, save.
	g := ZoneToggle(orig, 3, 2) // index 35, was '1' → '0'
	if g[35] != '0' {
		t.Fatal("round-trip: cell (3,2) did not turn off")
	}
	g = ZoneToggle(g, 10, 6) // index 106, was '0' → '1'
	if g[106] != '1' {
		t.Fatal("round-trip: cell (10,6) did not turn on")
	}
	// Everything else unchanged.
	for i, c := range orig {
		if i == 35 || i == 106 {
			continue
		}
		if g[i] != byte(c) {
			t.Fatalf("round-trip: index %d changed (%c → %c)", i, c, g[i])
		}
	}
}
