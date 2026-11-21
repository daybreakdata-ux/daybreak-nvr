package engine

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	"io"
	"os/exec"
	"sync/atomic"
	"time"

	"daybreaknvr/internal/startlog"
)

// PumpStallAfter is how long a pump may go without decoding a
// frame before the watchdog (v1.8) restarts it. On 2026-11-15
// every detector ffmpeg stalled with its process still alive
// (0 CPU, no frames for hours) while the cameras were fine, and
// the app only restarted a pump after its process exited — so a
// stalled connection never recovered.
const PumpStallAfter = 30 * time.Second

// Pump runs one ffmpeg process that decodes a stream (live RTSP or a
// recorded file) into a sequence of JPEG frames, always keeping the
// most recent decoded frame for the UI to display. This avoids any
// native video-codec dependency in the app itself.
type Pump struct {
	cancel  context.CancelFunc
	latest  atomic.Value // image.Image
	lastErr atomic.Value // string
	frames  atomic.Int64
	stopped atomic.Bool
	// startedAt is when the ffmpeg process was launched; the
	// watchdog gives a fresh pump its full grace window before
	// it can be called stalled.
	startedAt time.Time
	// lastFrame is the unix-nano time the most recent frame was
	// decoded (0 when no frame has arrived yet).
	lastFrame atomic.Int64
}

// PumpOptions configures a frame pump.
type PumpOptions struct {
	Input   string // RTSP URL or file path
	IsFile  bool
	SeekSec int // file playback start offset
	Width   int // decode width (height auto, even)
	FPS     int
}

// StartPump launches ffmpeg and returns the running pump.
func StartPump(ffmpeg string, o PumpOptions) *Pump {
	p := &Pump{startedAt: time.Now()}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	width := o.Width
	if width <= 0 {
		width = 640
	}
	fps := o.FPS
	if fps <= 0 {
		fps = 10
	}

	var args []string
	args = append(args, "-hide_banner", "-loglevel", "error")
	if !o.IsFile {
		args = append(args, "-rtsp_transport", "tcp")
	}
	if o.IsFile && o.SeekSec > 0 {
		args = append(args, "-ss", fmt.Sprint(o.SeekSec))
	}
	args = append(args,
		"-i", o.Input,
		"-an",
		"-vf", fmt.Sprintf("fps=%d,scale=%d:-2", fps, width),
		"-f", "image2pipe",
		"-vcodec", "mjpeg",
		"-q:v", "7",
		"pipe:1",
	)

	go p.run(ctx, ffmpeg, args)
	return p
}

func (p *Pump) run(ctx context.Context, ffmpeg string, args []string) {
	defer p.stopped.Store(true)
	defer func() {
		if r := recover(); r != nil {
			startlog.LogPanic("frame pump", r)
			p.lastErr.Store(fmt.Sprintf("pump panic: %v", r))
		}
	}()
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	hideWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		p.lastErr.Store(err.Error())
		return
	}
	if err := cmd.Start(); err != nil {
		p.lastErr.Store(err.Error())
		return
	}
	p.readFrames(stdout)
	_ = cmd.Wait()
}

var (
	jpegSOI = []byte{0xFF, 0xD8}
	jpegEOI = []byte{0xFF, 0xD9}
)

func (p *Pump) readFrames(r io.Reader) {
	buf := make([]byte, 0, 1<<20)
	chunk := make([]byte, 32*1024)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			buf = p.drain(buf)
		}
		if err != nil {
			return
		}
		// Safety valve against a malformed stream.
		if len(buf) > 8<<20 {
			buf = buf[:0]
		}
	}
}

// drain extracts every complete JPEG in buf, decodes the newest one,
// and returns the unconsumed remainder.
func (p *Pump) drain(buf []byte) []byte {
	for {
		start := bytes.Index(buf, jpegSOI)
		if start < 0 {
			if len(buf) > 1 {
				return buf[len(buf)-1:]
			}
			return buf
		}
		end := bytes.Index(buf[start+2:], jpegEOI)
		if end < 0 {
			return buf[start:]
		}
		frameEnd := start + 2 + end + 2
		frame := buf[start:frameEnd]
		img, _, err := image.Decode(bytes.NewReader(frame))
		if err == nil {
			p.latest.Store(img)
			p.frames.Add(1)
			p.lastFrame.Store(time.Now().UnixNano())
		}
		buf = buf[frameEnd:]
	}
}

// Latest returns the most recent decoded frame, or nil.
func (p *Pump) Latest() image.Image {
	v := p.latest.Load()
	if v == nil {
		return nil
	}
	return v.(image.Image)
}

// Err returns the last ffmpeg error text, if any.
func (p *Pump) Err() string {
	v := p.lastErr.Load()
	if v == nil {
		return ""
	}
	return v.(string)
}

// Frames counts decoded frames (liveness signal for the UI).
func (p *Pump) Frames() int64 { return p.frames.Load() }

// LastFrameTime is when the most recent frame was decoded, or the
// zero time when no frame has arrived yet.
func (p *Pump) LastFrameTime() time.Time {
	ns := p.lastFrame.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// Stalled reports whether the ffmpeg process is alive but has
// produced no frame for longer than threshold. A pump younger
// than the threshold is never stalled (startup grace: connecting
// and the first frame take a few seconds).
func (p *Pump) Stalled(threshold time.Duration) bool {
	if p.Stopped() {
		return false
	}
	now := time.Now()
	if now.Sub(p.startedAt) <= threshold {
		return false
	}
	lf := p.lastFrame.Load()
	if lf == 0 {
		return true
	}
	return now.Sub(time.Unix(0, lf)) > threshold
}

// Stopped reports whether the ffmpeg process has exited.
func (p *Pump) Stopped() bool { return p.stopped.Load() }

// Stop terminates the pump's ffmpeg process.
func (p *Pump) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
}

// WaitStopped polls until the process exits or the timeout passes.
func (p *Pump) WaitStopped(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if p.stopped.Load() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
