package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Clip is one recorded segment on disk.
type Clip struct {
	CameraSlug string
	CameraName string
	Path       string
	Start      time.Time
	Size       int64
	// SnapshotPath is the motion snapshot for this clip ("" when
	// the clip has none): a JPEG in the same day dir whose
	// timestamp falls inside the clip's window.
	SnapshotPath string
	// Kind is "person" / "motion" / "" (derived from tracker
	// events overlapping the clip window).
	Kind string
	// DurationSec is the clip length in seconds from the mp4
	// mvhd box (0 when unknown).
	DurationSec float64
}

// clipWindow returns the window a snapshot/kind lookup uses for a
// clip: its parsed duration, or 10s when the duration is unknown.
// Real ffmpeg segments always carry mvhd, so the fallback only
// matters for hand-made test files.
func clipWindow(c Clip) time.Duration {
	if c.DurationSec > 0 {
		return time.Duration(c.DurationSec * float64(time.Second))
	}
	return 10 * time.Second
}

// ScanLibrary walks the recordings tree
// (<root>/<camera-slug>/<YYYY-MM-DD>/<slug>_YYYYMMDD_HHMMSS.mp4)
// and returns every clip, newest first. Cameras maps slug -> display
// name so clips from renamed cameras still label sensibly.
// Segments ffmpeg has not finalized yet — still recording, or
// orphaned when the app stopped mid-segment — have no moov atom
// and cannot be played, so they are skipped here and never appear
// in the clip list (local UI, LAN web UI, or relay clip index).
// Each clip also carries its parsed duration, its motion snapshot
// (when a snapshot's timestamp falls inside the clip window) and
// its kind (from the persisted tracker events in the recordings
// root, when present).
func ScanLibrary(root string, cameras []Camera) []Clip {
	names := map[string]string{}
	for _, c := range cameras {
		names[c.Slug()] = c.Name
	}
	var clips []Clip
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(info.Name()), ".mp4") {
			return nil
		}
		if !hasMoov(p) {
			return nil
		}
		slug := filepath.Base(filepath.Dir(filepath.Dir(p)))
		name := names[slug]
		if name == "" {
			name = slug
		}
		dur, _ := parseMP4Duration(p)
		clips = append(clips, Clip{
			CameraSlug:  slug,
			CameraName:  name,
			Path:        p,
			Start:       parseClipTime(info.Name(), info.ModTime()),
			Size:        info.Size(),
			DurationSec: dur,
		})
		return nil
	})
	attachSnapshots(root, clips)
	attachKinds(root, clips, cameras)
	sort.Slice(clips, func(i, j int) bool {
		return clips[i].Start.After(clips[j].Start)
	})
	return clips
}

// attachSnapshots walks .jpg files in the same day dirs and
// attaches each snapshot to the clip (same slug) whose window
// contains the snapshot's timestamp. When several clips could
// match, the one with the latest start wins (clips never overlap).
func attachSnapshots(root string, clips []Clip) {
	bySlug := map[string][]int{}
	for i, c := range clips {
		bySlug[c.CameraSlug] = append(bySlug[c.CameraSlug], i)
	}
	if len(bySlug) == 0 {
		return
	}
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(info.Name()), ".jpg") {
			return nil
		}
		slug := filepath.Base(filepath.Dir(filepath.Dir(p)))
		idxs, ok := bySlug[slug]
		if !ok {
			return nil
		}
		snapTime := parseClipTime(info.Name(), info.ModTime())
		best := -1
		for _, i := range idxs {
			c := clips[i]
			if !snapTime.Before(c.Start) && snapTime.Before(c.Start.Add(clipWindow(c))) {
				if best < 0 || c.Start.After(clips[best].Start) {
					best = i
				}
			}
		}
		if best >= 0 && clips[best].SnapshotPath == "" {
			clips[best].SnapshotPath = p
		}
		return nil
	})
}

// attachKinds labels each clip from the persisted tracker events.
// The events file is keyed by camera ID (MotionTracker saves by
// ID), so resolve each clip's slug back to its camera ID; fall back
// to the slug key for files written keyed by slug.
func attachKinds(root string, clips []Clip, cameras []Camera) {
	eventsByCam := loadTrackerEvents(root)
	if len(eventsByCam) == 0 {
		return
	}
	idBySlug := map[string]string{}
	for _, cam := range cameras {
		idBySlug[cam.Slug()] = cam.ID
	}
	for i := range clips {
		c := &clips[i]
		evs := eventsByCam[idBySlug[c.CameraSlug]]
		if len(evs) == 0 {
			// Fall back to the slug key (files written keyed by slug).
			evs = eventsByCam[c.CameraSlug]
		}
		c.Kind = KindForClip(evs, c.Start, c.Start.Add(clipWindow(*c)))
	}
}

// ClipToken is a clip's index-free identity: slug + start timestamp.
// The same token names a clip in the local web UI (/clip?f=), in the
// relay clip index, and in clip upload requests.
func ClipToken(c Clip) string {
	return fmt.Sprintf("%s/%d", c.CameraSlug, c.Start.Unix())
}

// FindClip resolves a token (as built by ClipToken) against a scanned
// library. The start timestamp is the identity: two segments from
// the same camera never share one.
func FindClip(clips []Clip, token string) (Clip, bool) {
	parts := strings.SplitN(token, "/", 2)
	if len(parts) != 2 {
		return Clip{}, false
	}
	var start int64
	if _, err := fmt.Sscanf(parts[1], "%d", &start); err != nil {
		return Clip{}, false
	}
	for _, c := range clips {
		if c.CameraSlug == parts[0] && c.Start.Unix() == start {
			return c, true
		}
	}
	return Clip{}, false
}

// DeleteClipFile deletes one clip from disk by token, plus its
// motion snapshot when it has one. It returns true when a matching
// file was found and removed.
func DeleteClipFile(root string, cameras []Camera, token string) bool {
	clips := ScanLibrary(root, cameras)
	clip, ok := FindClip(clips, token)
	if !ok {
		return false
	}
	if err := os.Remove(clip.Path); err != nil {
		return false
	}
	// The snapshot goes with its clip (a missing snapshot is
	// fine — older clips never had one).
	if clip.SnapshotPath != "" {
		_ = os.Remove(clip.SnapshotPath)
	}
	return true
}

// parseClipTime pulls YYYYMMDD_HHMMSS out of the file name; falls back
// to the file's modification time minus nothing (close enough).
func parseClipTime(name string, mod time.Time) time.Time {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	parts := strings.Split(base, "_")
	for i := 0; i+1 < len(parts); i++ {
		if len(parts[i]) == 8 && len(parts[i+1]) == 6 {
			if t, err := time.ParseInLocation("20060102_150405",
				parts[i]+"_"+parts[i+1], time.Local); err == nil {
				return t
			}
		}
	}
	return mod
}
