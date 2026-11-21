package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"daybreaknvr/internal/startlog"
)

// CamStatus is the live recording state of one camera.
type CamStatus struct {
	Recording bool
	Since     time.Time
	Restarts  int
	LastError string
}

// Recorder supervises one ffmpeg segment-recording process per
// camera that has recording enabled, restarting with backoff when a
// stream drops — the core NVR behaviour.
type Recorder struct {
	ffmpeg string

	mu       sync.Mutex
	cancels  map[string]context.CancelFunc
	sigs     map[string]string
	statuses map[string]*CamStatus
}

// NewRecorder creates a recorder using the given ffmpeg binary.
func NewRecorder(ffmpeg string) *Recorder {
	return &Recorder{
		ffmpeg:   ffmpeg,
		cancels:  map[string]context.CancelFunc{},
		sigs:     map[string]string{},
		statuses: map[string]*CamStatus{},
	}
}

// camSig captures everything that should force a recorder restart
// when it changes (URL, mode, segment length, output folder).
func camSig(cam Camera, s Settings) string {
	return strings.Join([]string{
		cam.MainStreamURL(), cam.Mode(),
		fmt.Sprint(s.SegmentMinutes), s.RecordingsDir,
	}, "|")
}

// wantsRecorder reports whether a camera should have a running
// segment recorder: enabled, Record on, a stream to record from,
// and in Continuous mode. Motion and Person-only cameras are
// recorded by the EventRecorder (one clip per event, direct —
// no chunks), and an Off camera is live view only and must never
// have a recorder, even with Record on.
func wantsRecorder(cam Camera) bool {
	return cam.Enabled && cam.Record && cam.Mode() == RecordContinuous &&
		cam.MainStreamURL() != ""
}

// Sync reconciles running recorders with the settings: starts
// recorders for enabled cameras with Record on, stops the rest, and
// restarts any whose configuration changed.
func (r *Recorder) Sync(s Settings) {
	r.mu.Lock()
	defer r.mu.Unlock()

	want := map[string]Camera{}
	for _, cam := range s.Cameras {
		if wantsRecorder(cam) {
			want[cam.ID] = cam
		}
	}
	// Stop recorders no longer wanted or whose config changed.
	for id, cancel := range r.cancels {
		cam, ok := want[id]
		if !ok || r.sigs[id] != camSig(cam, s) {
			cancel()
			delete(r.cancels, id)
			delete(r.sigs, id)
		}
	}
	// Start recorders not yet running.
	for id, cam := range want {
		if _, ok := r.cancels[id]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		r.cancels[id] = cancel
		r.sigs[id] = camSig(cam, s)
		if _, ok := r.statuses[id]; !ok {
			r.statuses[id] = &CamStatus{}
		}
		go r.runCamera(ctx, cam, s)
	}
}

// StopAll stops every recorder (app shutdown).
func (r *Recorder) StopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, cancel := range r.cancels {
		cancel()
		delete(r.cancels, id)
	}
}

// Status returns a snapshot of one camera's recording state.
func (r *Recorder) Status(id string) CamStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	if st, ok := r.statuses[id]; ok {
		return *st
	}
	return CamStatus{}
}

func (r *Recorder) setStatus(id string, fn func(*CamStatus)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.statuses[id]
	if !ok {
		st = &CamStatus{}
		r.statuses[id] = st
	}
	fn(st)
}

func (r *Recorder) runCamera(ctx context.Context, cam Camera, s Settings) {
	backoff := 3 * time.Second
	for {
		select {
		case <-ctx.Done():
			r.setStatus(cam.ID, func(st *CamStatus) { st.Recording = false })
			return
		default:
		}

		err := func() (err error) {
			defer func() {
				if rec := recover(); rec != nil {
					startlog.LogPanic("recorder for "+cam.ID, rec)
					err = fmt.Errorf("recorder panic: %v", rec)
				}
			}()
			return r.recordOnce(ctx, cam, s)
		}()
		if ctx.Err() != nil {
			r.setStatus(cam.ID, func(st *CamStatus) { st.Recording = false })
			return
		}
		r.setStatus(cam.ID, func(st *CamStatus) {
			st.Recording = false
			st.Restarts++
			if err != nil {
				st.LastError = trimErr(err.Error())
			}
		})
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (r *Recorder) recordOnce(ctx context.Context, cam Camera, s Settings) error {
	if r.ffmpeg == "" {
		return fmt.Errorf("ffmpeg not found")
	}
	dayDir := filepath.Join(s.RecordingsDir, cam.Slug(),
		time.Now().Format("2006-01-02"))
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		return err
	}
	pattern := filepath.Join(dayDir, cam.Slug()+"_%Y%m%d_%H%M%S.mp4")
	// v1.6: this recorder serves Continuous mode only. Motion and
	// Person-only cameras are recorded by the EventRecorder (one
	// clip per event, written directly — no 10 s chunks, no
	// finalizer), so there is no small-chunk branch here any more.
	segSecs := s.SegmentMinutes * 60
	if segSecs < 60 {
		segSecs = 600
	}
	args := []string{
		"-hide_banner", "-loglevel", "warning",
		"-rtsp_transport", "tcp",
		"-i", cam.MainStreamURL(),
		"-map", "0:v:0", "-c:v", "copy",
		"-map", "0:a:0?", "-c:a", "aac", "-b:a", "64k",
		"-f", "segment",
		"-segment_time", fmt.Sprint(segSecs),
		"-segment_format", "mp4",
		"-reset_timestamps", "1",
		"-strftime", "1",
		pattern,
	}
	cmd := exec.CommandContext(ctx, r.ffmpeg, args...)
	hideWindow(cmd)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	r.setStatus(cam.ID, func(st *CamStatus) {
		st.Recording = true
		st.Since = time.Now()
		st.LastError = ""
	})
	err := cmd.Wait()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			lines := strings.Split(msg, "\n")
			return fmt.Errorf("%s", lines[len(lines)-1])
		}
		return err
	}
	return nil
}

func trimErr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 180 {
		return s[len(s)-180:]
	}
	return s
}
