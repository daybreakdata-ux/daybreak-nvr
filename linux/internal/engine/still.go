// One-frame still grabs from a camera (v1.6).
//
// The zone picker (desktop and web UI) shows the camera's picture
// with the detection-zone grid overlaid on it, so the user can see
// exactly which spots they are choosing. GrabStillJPEG grabs one
// fresh frame from the camera's sub-stream with ffmpeg and returns
// it as JPEG bytes. The desktop picker prefers the live pump's
// latest frame (free, instant) and uses this only when no pump is
// running; the web UI's /api/camera-snapshot endpoint uses it
// directly (the browser has no pump).
package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// StillGrabTimeout is how long a still grab may take before it is
// abandoned (a camera that will not answer must not hang the UI).
const StillGrabTimeout = 10 * time.Second

// GrabStillJPEG grabs one frame from url (an RTSP URL or a local
// file, for tests) and returns it as JPEG bytes. It runs ffmpeg
// with a timeout and writes to a temp file in the OS temp dir; the
// caller gets the bytes, never a path. A nil/empty ffmpeg or url,
// a timeout, or an unreadable frame all return an error — the
// caller falls back to the plain zone grid instead.
func GrabStillJPEG(ffmpeg, url string) ([]byte, error) {
	if ffmpeg == "" {
		return nil, fmt.Errorf("ffmpeg not available")
	}
	if url == "" {
		return nil, fmt.Errorf("no camera stream URL")
	}
	tmp, err := os.CreateTemp("", ".nvr-still-*.jpg")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	ctx, cancel := context.WithTimeout(context.Background(), StillGrabTimeout)
	defer cancel()
	// The sub-stream is fine for a still (it is what the live
	// wall and the detector use); -q:v 2 keeps the picture
	// readable without a big file.
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-rtsp_transport", "tcp",
		"-i", url,
		"-frames:v", "1", "-q:v", "2",
		tmpPath)
	hideWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("still grab failed: %v: %s", err, trimErr(string(out)))
	}
	b, err := os.ReadFile(tmpPath)
	if err != nil {
		return nil, err
	}
	if len(b) < 100 || b[0] != 0xFF || b[1] != 0xD8 {
		return nil, fmt.Errorf("still grab produced no JPEG frame")
	}
	return b, nil
}

// stillCacheEntry is one cached still grab: the web snapshot
// endpoint serves the cached frame for a few seconds so a burst
// of zone-picker refreshes does not hammer the camera with a new
// RTSP connection each time.
type stillCacheEntry struct {
	jpeg []byte
	at   time.Time
}

// StillCache is a tiny per-camera still cache (5 s TTL). It is
// deliberately separate from GrabStillJPEG so tests can drive the
// grab and the cache independently.
type StillCache struct {
	mu    sync.Mutex
	items map[string]*stillCacheEntry
	ttl   time.Duration
	grab  func(ffmpeg, url string) ([]byte, error)
}

// NewStillCache creates a cache with the given TTL and grab
// function (nil grab = GrabStillJPEG).
func NewStillCache(ttl time.Duration, grab func(ffmpeg, url string) ([]byte, error)) *StillCache {
	if grab == nil {
		grab = GrabStillJPEG
	}
	if ttl <= 0 {
		ttl = 5 * time.Second
	}
	return &StillCache{items: map[string]*stillCacheEntry{}, ttl: ttl, grab: grab}
}

// Get returns a fresh JPEG still for key (usually the camera ID),
// grabbing a new one only when the cached frame is older than the
// TTL. The grab runs outside the lock so concurrent callers for
// different cameras do not block each other.
func (c *StillCache) Get(ffmpeg, key, url string) ([]byte, error) {
	c.mu.Lock()
	if e, ok := c.items[key]; ok && time.Since(e.at) < c.ttl {
		b := e.jpeg
		c.mu.Unlock()
		return b, nil
	}
	c.mu.Unlock()
	b, err := c.grab(ffmpeg, url)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.items[key] = &stillCacheEntry{jpeg: b, at: time.Now()}
	c.mu.Unlock()
	return b, nil
}

// SnapshotStillPath is a helper for tests: it writes b as a JPEG
// next to the given dir and returns the path.
func SnapshotStillPath(dir string, b []byte) (string, error) {
	p := filepath.Join(dir, "still.jpg")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return "", err
	}
	return p, nil
}
