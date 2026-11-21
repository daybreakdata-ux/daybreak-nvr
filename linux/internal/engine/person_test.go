package engine

import (
	"image"
	_ "image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The bundled model/lib live in the repo's build/ directory
// (they are also installed next to the binary in the .deb).
func testModelPaths(t *testing.T) (model, lib string) {
	t.Helper()
	cands := []string{
		"../../build/yolov8n.onnx",
		"/usr/lib/daybreak-nvr/yolov8n.onnx",
		"/usr/lib/daybreak-nvr-linux/yolov8n.onnx",
	}
	for _, c := range cands {
		if _, err := os.Stat(c); err == nil {
			model = c
			break
		}
	}
	libs := []string{
		"../../build/libonnxruntime.so",
		"/usr/lib/daybreak-nvr/libonnxruntime.so",
		"/usr/lib/daybreak-nvr-linux/libonnxruntime.so",
	}
	for _, c := range libs {
		if _, err := os.Stat(c); err == nil {
			lib = c
			break
		}
	}
	if model == "" || lib == "" {
		t.Skip("bundled person model / ONNX Runtime lib not present")
	}
	return model, lib
}

func loadTestImage(t *testing.T, name string) image.Image {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return img
}

// The real proof: the bundled model finds people in real photos
// and finds nobody in a person-free scene.
func TestPersonDetectorRealImages(t *testing.T) {
	model, lib := testModelPaths(t)
	pd, err := NewPersonDetector(model, lib)
	if err != nil {
		t.Fatalf("NewPersonDetector: %v", err)
	}
	defer pd.Close()

	img := loadTestImage(t, "zidane.jpg")
	dets, err := pd.Detect(img, 0.25)
	if err != nil {
		t.Fatalf("Detect(zidane): %v", err)
	}
	if len(dets) < 2 {
		t.Fatalf("zidane.jpg: %d person(s), want >= 2: %+v", len(dets), dets)
	}
	for _, d := range dets {
		if d.Confidence < 0.25 || d.X1 < 0 || d.Y1 < 0 || d.X2 > 1 || d.Y2 > 1 || d.X2 <= d.X1 || d.Y2 <= d.Y1 {
			t.Errorf("bad detection: %+v", d)
		}
		t.Logf("zidane: box [%.2f %.2f %.2f %.2f] conf %.3f", d.X1, d.Y1, d.X2, d.Y2, d.Confidence)
	}

	bus := loadTestImage(t, "bus.jpg")
	dets, err = pd.Detect(bus, 0.25)
	if err != nil {
		t.Fatalf("Detect(bus): %v", err)
	}
	if len(dets) < 3 {
		t.Fatalf("bus.jpg: %d person(s), want >= 3: %+v", len(dets), dets)
	}

	neg := loadTestImage(t, "noperson.jpg")
	dets, err = pd.Detect(neg, 0.25)
	if err != nil {
		t.Fatalf("Detect(noperson): %v", err)
	}
	if len(dets) != 0 {
		t.Fatalf("noperson.jpg: %d person(s), want 0: %+v", len(dets), dets)
	}
}

// The decoder is pure math: a synthetic YOLOv8 output with one
// strong person anchor decodes to the right box, and overlapping
// duplicates are NMS'd into one.
func TestDecodePersonDetections(t *testing.T) {
	out := make([]float32, 84*8400)
	// Anchor 0: a person at centre (320,320) size 200x300 in a
	// 640x640 letterboxed frame of a 640x480 image.
	out[0*8400+0] = 320 // cx
	out[1*8400+0] = 320 // cy
	out[2*8400+0] = 200 // w
	out[3*8400+0] = 300 // h
	out[4*8400+0] = 0.9 // person confidence
	// Anchor 1: an almost-identical duplicate (lower confidence).
	out[0*8400+1] = 322
	out[1*8400+1] = 321
	out[2*8400+1] = 198
	out[3*8400+1] = 298
	out[4*8400+1] = 0.8
	// Anchor 2: a different person, far away, weak confidence.
	out[0*8400+2] = 100
	out[1*8400+2] = 100
	out[2*8400+2] = 60
	out[3*8400+2] = 120
	out[4*8400+2] = 0.3

	img := image.NewRGBA(image.Rect(0, 0, 640, 480))
	dets := decodePersonDetections(out, img, 640, 0.25)
	if len(dets) != 2 {
		t.Fatalf("decoded %d, want 2 (duplicate NMS'd): %+v", len(dets), dets)
	}
	best := dets[0]
	if best.Confidence != 0.9 {
		t.Errorf("best confidence = %v, want 0.9", best.Confidence)
	}
	// Letterbox of a 640x480 image: scale 1, offsetY 80. The box
	// spans x 220..420, y 170..470 in input pixels -> y 90..390
	// in original pixels -> 0.1875..0.8125 in 0..1 coords.
	if best.X1 < 0.33 || best.X1 > 0.36 || best.Y1 < 0.18 || best.Y1 > 0.20 {
		t.Errorf("box = %+v, top-left not near (0.34, 0.19)", best)
	}
	// Below the threshold, nothing survives.
	if got := decodePersonDetections(out, img, 640, 0.95); len(got) != 0 {
		t.Errorf("high threshold decoded %d, want 0", len(got))
	}
}

// A detection only counts for the zone when its centre is in an
// enabled cell.
func TestDetectionInZone(t *testing.T) {
	grid := make([]byte, ZoneCells)
	for i := range grid {
		grid[i] = '0'
	}
	grid[0] = '1' // top-left cell only
	zone := string(grid)
	inside := Detection{X1: 0.01, Y1: 0.01, X2: 0.05, Y2: 0.05}
	if !DetectionInZone(inside, zone) {
		t.Error("centre in the enabled cell should count")
	}
	outside := Detection{X1: 0.9, Y1: 0.9, X2: 0.95, Y2: 0.95}
	if DetectionInZone(outside, zone) {
		t.Error("centre outside the zone should not count")
	}
	if !DetectionInZone(outside, "") {
		t.Error("empty zone grid should count everything")
	}
}

// Zone helpers: the default grid is whole-frame; the mask maps
// 16x9 zone cells onto the 96x54 detector grid.
func TestZoneGridHelpers(t *testing.T) {
	g := DefaultZoneGrid()
	if len(g) != ZoneCells {
		t.Fatalf("default grid length = %d, want %d", len(g), ZoneCells)
	}
	if ZoneEnabledFraction(g) != 1 {
		t.Errorf("default grid fraction = %v, want 1", ZoneEnabledFraction(g))
	}
	if !ZoneCellEnabled(g, 0, 0) || !ZoneCellEnabled(g, 15, 8) {
		t.Error("default grid should enable every cell")
	}
	mask := ZoneMask(g)
	if mask == nil || len(mask) != 96*54 {
		t.Fatalf("mask length = %v, want 96*54", len(mask))
	}
	for i, on := range mask {
		if !on {
			t.Fatalf("mask[%d] off for whole-frame grid", i)
		}
	}
	// A grid with one cell enabled gives exactly 6x6 detector cells.
	one := make([]byte, ZoneCells)
	for i := range one {
		one[i] = '0'
	}
	one[0] = '1'
	mask = ZoneMask(string(one))
	on := 0
	for _, b := range mask {
		if b {
			on++
		}
	}
	if on != 36 {
		t.Errorf("one zone cell enabled %d detector cells, want 36", on)
	}
	if ZoneEnabledFraction(string(one)) >= 0.01 {
		t.Errorf("one cell fraction = %v, want ~0.007", ZoneEnabledFraction(string(one)))
	}
}

// Timing sanity for the CPU budget: a single inference on a real
// frame should complete well inside a second on this machine.
func TestPersonDetectorTiming(t *testing.T) {
	model, lib := testModelPaths(t)
	pd, err := NewPersonDetector(model, lib)
	if err != nil {
		t.Fatalf("NewPersonDetector: %v", err)
	}
	defer pd.Close()
	img := loadTestImage(t, "bus.jpg")
	start := time.Now()
	if _, err := pd.Detect(img, 0.25); err != nil {
		t.Fatalf("Detect: %v", err)
	}
	d := time.Since(start)
	t.Logf("one 640x640 inference took %v", d)
	if d > 5*time.Second {
		t.Errorf("inference took %v, too slow for a background sampler", d)
	}
}
