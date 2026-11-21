// On-device AI person detection service (v1.3).
//
// One worker goroutine, one ONNX Runtime session, shared by the
// desktop app and --headless. It samples frames from the pumps the
// app already runs (never a new RTSP connection), runs the bundled
// YOLOv8n model, and feeds the results into the motion tracker as
// person events — which, in Motion mode, keep/start recording
// exactly like frame-difference motion.
//
// Sampling is motion-gated to keep the CPU cost sane: a camera is
// sampled when it recently had frame-difference motion, when it
// already has a live person track, or once every 10s as a sweep
// (so a person who walks in slowly, or a continuous-mode camera
// with no motion analysis, is still seen). At ~250 ms per
// inference on a VM CPU, one full round over 4 AI-enabled cameras
// is about a second.
package main

import (
	"image"
	"sync"
	"time"

	"daybreaknvr/internal/engine"
	"daybreaknvr/internal/startlog"
)

// personSweepInterval is the periodic re-check for cameras with
// no recent motion (and for continuous-mode cameras, which have
// no motion analysis at all).
const personSweepInterval = 10 * time.Second

type personService struct {
	det       *engine.PersonDetector
	trackers  map[string]*engine.PersonTracker
	lastSweep map[string]time.Time
	mu        sync.Mutex
	stopCh    chan struct{}
	wg        sync.WaitGroup
	// onPersonFrame, when set, is called for every live track after
	// each round with the track's box centre and the frame it came
	// from — the snapshot keeper's person-frame feed (v1.4).
	onPersonFrame func(camID string, cx, cy float32, img image.Image, now time.Time)
}

// newPersonService loads the bundled model + runtime. If either is
// missing the service is disabled (det == nil) and person
// detection is simply off — motion detection is unaffected.
func newPersonService() *personService {
	ps := &personService{
		trackers:  map[string]*engine.PersonTracker{},
		lastSweep: map[string]time.Time{},
		stopCh:    make(chan struct{}),
	}
	model := engine.FindPersonModel()
	lib := engine.FindORTLib()
	if model == "" || lib == "" {
		startlog.Stage("AI person detection unavailable (model or runtime not found next to the app)")
		return ps
	}
	det, err := engine.NewPersonDetector(model, lib)
	if err != nil {
		startlog.Stage("AI person detection failed to start: %v", err)
		return ps
	}
	ps.det = det
	startlog.Stage("AI person detection ready (YOLOv8n, on-device, %s)", model)
	return ps
}

// Enabled reports whether the detector loaded.
func (ps *personService) Enabled() bool { return ps.det != nil }

// Start begins the worker loop. getSettings / getPump are
// callbacks into the owning app; tracker is the motion tracker
// person events are recorded into.
func (ps *personService) Start(getSettings func() engine.Settings, getPump func(id string) *engine.Pump, tracker *engine.MotionTracker) {
	if ps.det == nil {
		return
	}
	ps.wg.Add(1)
	go ps.loop(getSettings, getPump, tracker)
}

// Stop shuts the worker down and releases the detector.
func (ps *personService) Stop() {
	close(ps.stopCh)
	ps.wg.Wait()
	if ps.det != nil {
		ps.det.Close()
		ps.det = nil
	}
}

func (ps *personService) loop(getSettings func() engine.Settings, getPump func(id string) *engine.Pump, tracker *engine.MotionTracker) {
	defer ps.wg.Done()
	for {
		select {
		case <-ps.stopCh:
			return
		default:
		}
		startlog.Guard("person detection round", func() {
			ps.round(getSettings, getPump, tracker)
		})
		select {
		case <-ps.stopCh:
			return
		case <-time.After(time.Second):
		}
	}
}

func (ps *personService) round(getSettings func() engine.Settings, getPump func(id string) *engine.Pump, tracker *engine.MotionTracker) {
	now := time.Now()
	for _, cam := range getSettings().Cameras {
		// Off ("Do not record") cameras get no person detection
		// at all: they are live view only.
		if !cam.Enabled || !cam.Record || !cam.HumanDetect || cam.Mode() == engine.RecordOff {
			continue
		}
		// Motion gate: sample when the camera recently moved,
		// when a person is already being tracked, or on the
		// periodic sweep. Otherwise skip — no work, no CPU.
		ps.mu.Lock()
		tr, ok := ps.trackers[cam.ID]
		if !ok {
			tr = engine.NewPersonTracker()
			ps.trackers[cam.ID] = tr
		}
		lastSweep := ps.lastSweep[cam.ID]
		ps.mu.Unlock()
		active := len(tr.Active(now)) > 0
		if !tracker.Active(cam.ID) && !active && now.Sub(lastSweep) < personSweepInterval {
			continue
		}
		ps.mu.Lock()
		ps.lastSweep[cam.ID] = now
		ps.mu.Unlock()

		p := getPump(cam.ID)
		if p == nil {
			continue
		}
		img := p.Latest()
		if img == nil {
			continue
		}
		conf := float32(cam.HumanConfidence) / 100
		if conf <= 0 {
			conf = 0.5
		}
		dets, err := ps.det.Detect(img, conf)
		if err != nil {
			startlog.Stage("person detection error (%s): %v", cam.Name, err)
			continue
		}
		// Only people inside the enabled zone count.
		var inZone []engine.Detection
		for _, d := range dets {
			if engine.DetectionInZone(d, cam.ZoneGrid) {
				inZone = append(inZone, d)
			}
		}
		tracks := tr.Update(inZone, now)
		for _, t := range tracks {
			tracker.NotePerson(cam.ID, t.ID, t.Confidence, now)
		}
		// Feed live person tracks to the snapshot keeper (v1.4):
		// the box centre is the person centroid, already
		// zone-gated above (inZone only).
		if ps.onPersonFrame != nil {
			for _, t := range tracks {
				cx, cy := t.Box.Center()
				ps.onPersonFrame(cam.ID, cx, cy, img, now)
			}
		}
		if len(tracks) > 0 {
			startlog.Stage("person: %s — %d tracked (best conf %.2f)", cam.Name, len(tracks), tracks[0].Confidence)
		}
	}
}

// ActiveTracks returns the live person tracks for a camera (for
// the Motion page's "people now" line). Empty when disabled.
func (ps *personService) ActiveTracks(camID string) []engine.Track {
	if ps == nil {
		return nil
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	tr, ok := ps.trackers[camID]
	if !ok {
		return nil
	}
	return tr.Active(time.Now())
}
