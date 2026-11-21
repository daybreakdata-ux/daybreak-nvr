// On-device AI person detection (v1.3).
//
// A YOLOv8n model (ONNX, ~13 MB) runs through the ONNX Runtime
// Go binding on the CPU — entirely on this PC. No cloud API, no
// account, no API key, no network call: the model file and the
// ONNX Runtime shared library are bundled in the .deb next to the
// binary (yolov8n.onnx, libonnxruntime.so) and located the same
// way as the bundled ffmpeg.
//
// Inference is ~250-300 ms per frame at the model's fixed 640x640
// input on a typical VM CPU, so detection runs on sampled frames
// (motion-gated, ~1 fps per camera while something moves) through
// a single worker — never a new RTSP connection per camera, and
// never concurrently on the same session (ORT sessions are not
// safe for concurrent Run calls).
package engine

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"sort"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
	"golang.org/x/image/draw"
)

// PersonInputSize is the model's fixed input (the exported YOLOv8n
// only accepts 640x640).
const PersonInputSize = 640

// PersonModelFile / PersonLibFile are the bundled filenames.
const (
	PersonModelFile = "yolov8n.onnx"
	PersonLibFile   = "libonnxruntime.so"
)

var ortEnvOnce sync.Once
var ortEnvErr error

// FindORTLib locates the bundled ONNX Runtime shared library:
// next to the executable first, then the system library path.
func FindORTLib() string {
	for _, c := range []string{
		filepath.Join(ExeDir(), PersonLibFile),
		"/usr/lib/daybreak-nvr/" + PersonLibFile,
		"/usr/lib/daybreak-nvr-linux/" + PersonLibFile,
	} {
		if fileExists(c) {
			return c
		}
	}
	if p, err := lookPath(PersonLibFile); err == nil {
		return p
	}
	return ""
}

// FindPersonModel locates the bundled person-detection model.
func FindPersonModel() string {
	for _, c := range []string{
		filepath.Join(ExeDir(), PersonModelFile),
		"/usr/lib/daybreak-nvr/" + PersonModelFile,
		"/usr/lib/daybreak-nvr-linux/" + PersonModelFile,
	} {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// Detection is one person in a frame. Coordinates are 0..1
// relative to the original frame (top-left origin).
type Detection struct {
	X1, Y1, X2, Y2 float32
	Confidence     float32
}

// Center returns the box centre in 0..1 frame coordinates.
func (d Detection) Center() (x, y float32) {
	return (d.X1 + d.X2) / 2, (d.Y1 + d.Y2) / 2
}

// DetectionInZone reports whether a detection counts for a zone
// grid: the box centre must fall in an enabled zone cell. An
// empty/malformed grid means the whole frame counts.
func DetectionInZone(d Detection, zoneGrid string) bool {
	if len(zoneGrid) != ZoneCells {
		return true
	}
	cx, cy := d.Center()
	col := int(cx * ZoneCols)
	row := int(cy * ZoneRows)
	return ZoneCellEnabled(zoneGrid, col, row)
}

// PersonDetector runs the bundled YOLOv8n model. It is not safe
// for concurrent use — callers must serialize Detect calls
// (the app runs one worker goroutine for all cameras).
type PersonDetector struct {
	session     *ort.AdvancedSession
	inputTensor *ort.Tensor[float32]
	outputTensor *ort.Tensor[float32]
	inputData   []float32
	inputSize   int
	mu          sync.Mutex
}

// NewPersonDetector loads the model and runtime. libPath and
// modelPath must be explicit paths (see FindORTLib /
// FindPersonModel). The ONNX Runtime environment is initialized
// once per process.
func NewPersonDetector(modelPath, libPath string) (*PersonDetector, error) {
	if modelPath == "" {
		return nil, fmt.Errorf("person model not found (expected %s next to the app)", PersonModelFile)
	}
	if libPath == "" {
		return nil, fmt.Errorf("ONNX Runtime library not found (expected %s next to the app)", PersonLibFile)
	}
	ortEnvOnce.Do(func() {
		ort.SetSharedLibraryPath(libPath)
		ortEnvErr = ort.InitializeEnvironment()
	})
	if ortEnvErr != nil {
		return nil, fmt.Errorf("ONNX Runtime init: %w", ortEnvErr)
	}
	size := PersonInputSize
	pd := &PersonDetector{inputSize: size}
	pd.inputData = make([]float32, 1*3*size*size)
	in, err := ort.NewTensor(ort.NewShape(1, 3, int64(size), int64(size)), pd.inputData)
	if err != nil {
		return nil, fmt.Errorf("person input tensor: %w", err)
	}
	pd.inputTensor = in
	out, err := ort.NewEmptyTensor[float32](ort.NewShape(1, 84, 8400))
	if err != nil {
		in.Destroy()
		return nil, fmt.Errorf("person output tensor: %w", err)
	}
	pd.outputTensor = out
	opts, err := ort.NewSessionOptions()
	if err != nil {
		in.Destroy()
		out.Destroy()
		return nil, fmt.Errorf("person session options: %w", err)
	}
	defer opts.Destroy()
	// Keep inference polite: two worker threads is plenty for a
	// background detector and leaves the rest of the machine
	// alone for recording and preview.
	_ = opts.SetIntraOpNumThreads(2)
	_ = opts.SetInterOpNumThreads(1)
	sess, err := ort.NewAdvancedSession(modelPath,
		[]string{"images"}, []string{"predictions"},
		[]ort.Value{in}, []ort.Value{out}, opts)
	if err != nil {
		in.Destroy()
		out.Destroy()
		return nil, fmt.Errorf("person session: %w", err)
	}
	pd.session = sess
	return pd, nil
}

// Close releases the session and tensors.
func (pd *PersonDetector) Close() {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	if pd.session != nil {
		_ = pd.session.Destroy()
		pd.session = nil
	}
	if pd.inputTensor != nil {
		_ = pd.inputTensor.Destroy()
		pd.inputTensor = nil
	}
	if pd.outputTensor != nil {
		_ = pd.outputTensor.Destroy()
		pd.outputTensor = nil
	}
}

// Detect runs person detection on one frame and returns the
// detections at or above confThreshold (0..1), in 0..1 frame
// coordinates. It is serialized internally.
func (pd *PersonDetector) Detect(img image.Image, confThreshold float32) ([]Detection, error) {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	if pd.session == nil {
		return nil, fmt.Errorf("person detector is closed")
	}
	if img == nil {
		return nil, nil
	}
	letterboxInto(pd.inputData, img, pd.inputSize)
	if err := pd.session.Run(); err != nil {
		return nil, fmt.Errorf("person inference: %w", err)
	}
	return decodePersonDetections(pd.outputTensor.GetData(), img, pd.inputSize, confThreshold), nil
}

// letterboxInto scales img to fit size×size (grey 114 padding,
// the YOLO convention) and writes it as NCHW float32 0..1.
func letterboxInto(data []float32, img image.Image, size int) {
	b := img.Bounds()
	sw, sh := float32(b.Dx()), float32(b.Dy())
	scale := float32(size) / maxF32(sw, sh)
	nw, nh := int(sw*scale+0.5), int(sh*scale+0.5)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	for i := range dst.Pix {
		dst.Pix[i] = 114
	}
	tmp := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.ApproxBiLinear.Scale(tmp, tmp.Bounds(), img, b, draw.Over, nil)
	ox, oy := (size-nw)/2, (size-nh)/2
	for y := 0; y < nh; y++ {
		for x := 0; x < nw; x++ {
			dst.SetRGBA(ox+x, oy+y, tmp.RGBAAt(x, y))
		}
	}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			c := dst.RGBAAt(x, y)
			data[0*size*size+y*size+x] = float32(c.R) / 255
			data[1*size*size+y*size+x] = float32(c.G) / 255
			data[2*size*size+y*size+x] = float32(c.B) / 255
		}
	}
}

func maxF32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

// decodePersonDetections decodes the YOLOv8 (1,84,8400) output:
// rows 0..3 are cx,cy,w,h in input pixels; rows 4..83 are class
// scores, class 0 = person. Boxes are mapped back out of the
// letterbox into 0..1 coordinates of the original frame.
func decodePersonDetections(out []float32, img image.Image, inputSize int, confThr float32) []Detection {
	const n = 8400
	b := img.Bounds()
	origW, origH := float32(b.Dx()), float32(b.Dy())
	scale := float32(inputSize) / maxF32(origW, origH)
	nw, nh := origW*scale, origH*scale
	ox, oy := (float32(inputSize)-nw)/2, (float32(inputSize)-nh)/2
	var cands []Detection
	for i := 0; i < n; i++ {
		conf := out[4*n+i] // class 0 (person)
		if conf < confThr {
			continue
		}
		cx, cy := out[0*n+i], out[1*n+i]
		w, h := out[2*n+i], out[3*n+i]
		x1 := (cx - w/2 - ox) / scale / origW
		y1 := (cy - h/2 - oy) / scale / origH
		x2 := (cx + w/2 - ox) / scale / origW
		y2 := (cy + h/2 - oy) / scale / origH
		cands = append(cands, Detection{X1: clamp01(x1), Y1: clamp01(y1), X2: clamp01(x2), Y2: clamp01(y2), Confidence: conf})
	}
	return personNMS(cands, 0.45)
}

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func personIoU(a, b Detection) float32 {
	x1 := maxF32(a.X1, b.X1)
	y1 := maxF32(a.Y1, b.Y1)
	x2 := minF32(a.X2, b.X2)
	y2 := minF32(a.Y2, b.Y2)
	iw, ih := x2-x1, y2-y1
	if iw <= 0 || ih <= 0 {
		return 0
	}
	inter := iw * ih
	ua := (a.X2 - a.X1) * (a.Y2 - a.Y1)
	ub := (b.X2 - b.X1) * (b.Y2 - b.Y1)
	return inter / (ua + ub - inter)
}

func minF32(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}

// personNMS keeps the highest-confidence box per overlapping group.
func personNMS(boxes []Detection, iouThr float32) []Detection {
	sort.Slice(boxes, func(i, j int) bool { return boxes[i].Confidence > boxes[j].Confidence })
	var kept []Detection
	for _, b := range boxes {
		suppress := false
		for _, k := range kept {
			if personIoU(b, k) > iouThr {
				suppress = true
				break
			}
		}
		if !suppress {
			kept = append(kept, b)
		}
	}
	return kept
}
