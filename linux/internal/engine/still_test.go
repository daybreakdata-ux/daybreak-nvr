// Tests for the v1.6 still grabber (zone picker pictures):
// GrabStillJPEG returns a JPEG for a reachable stream and a clear
// error otherwise; StillCache avoids hammering the camera.
package engine

import (
	"bytes"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestGrabStillJPEGUnreachable(t *testing.T) {
	ff := ffmpegOrSkip(t)
	// Nothing listens on this port: the grab must fail fast with
	// a clear error (not hang, not return garbage).
	_, err := GrabStillJPEG(ff, "rtsp://127.0.0.1:9/nonexistent")
	if err == nil {
		t.Fatal("GrabStillJPEG on an unreachable camera returned no error")
	}
}

func TestGrabStillJPEGNoFFmpeg(t *testing.T) {
	_, err := GrabStillJPEG("", "rtsp://127.0.0.1:8554/testcam")
	if err == nil {
		t.Fatal("GrabStillJPEG with no ffmpeg binary returned no error")
	}
}

func TestGrabStillJPEGFromStream(t *testing.T) {
	ff := ffmpegOrSkip(t)
	url := startEventTestRTSP(t)
	jpeg, err := GrabStillJPEG(ff, url)
	if err != nil {
		t.Fatalf("GrabStillJPEG on the test stream: %v", err)
	}
	// JPEG magic bytes.
	if len(jpeg) < 3 || jpeg[0] != 0xFF || jpeg[1] != 0xD8 || jpeg[2] != 0xFF {
		t.Fatalf("GrabStillJPEG returned %d bytes that do not start with the JPEG magic", len(jpeg))
	}
	if len(jpeg) < 1000 {
		t.Fatalf("GrabStillJPEG returned only %d bytes (suspiciously small for a 640x360 frame)", len(jpeg))
	}
}

func TestStillCache(t *testing.T) {
	calls := atomic.Int32{}
	grab := func(ffmpegBin, url string) ([]byte, error) {
		calls.Add(1)
		return []byte{0xFF, 0xD8, 0xFF, 0x01}, nil
	}
	c := NewStillCache(5*time.Second, grab)
	// Two grabs within the TTL: the camera is hit once.
	a, err := c.Get("ff", "cam1", "rtsp://x/stream2")
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Get("ff", "cam1", "rtsp://x/stream2")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("cached still differs from the first grab")
	}
	if calls.Load() != 1 {
		t.Fatalf("grab called %d times within the TTL, want 1", calls.Load())
	}
	// A different camera is a different cache entry.
	if _, err := c.Get("ff", "cam2", "rtsp://y/stream2"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("grab called %d times after a second camera, want 2", calls.Load())
	}
	// After the TTL expires, the camera is hit again.
	c2 := NewStillCache(10*time.Millisecond, grab)
	if _, err := c2.Get("ff", "cam1", "rtsp://x/stream2"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := c2.Get("ff", "cam1", "rtsp://x/stream2"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 {
		t.Fatalf("grab called %d times after TTL expiry, want 4", calls.Load())
	}
	// Errors are not cached: a failing grab is retried next time.
	fail := NewStillCache(time.Minute, func(string, string) ([]byte, error) {
		return nil, errors.New("camera down")
	})
	if _, err := fail.Get("ff", "cam1", "rtsp://x/stream2"); err == nil {
		t.Fatal("failing grab returned no error")
	}
	if _, err := fail.Get("ff", "cam1", "rtsp://x/stream2"); err == nil {
		t.Fatal("failing grab returned no error on the second call")
	}
}
