package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"daybreaknvr/internal/startlog"
)

// moovAtFront reports whether the MP4's moov box sits at the front
// of the file (first or second top-level box, i.e. right after
// ftyp) — a faststart file a browser can start playing after the
// first chunk, instead of fetching the tail first.
func moovAtFront(path string) bool {
	types := []string{}
	_ = walkTopLevelBoxes(path, func(typ string, _ uint64) bool {
		types = append(types, typ)
		return len(types) < 2
	})
	for i, t := range types {
		if t == "moov" {
			return i <= 1
		}
	}
	return false
}

// remuxFaststart rewrites src into a temp file in the same directory
// with the moov atom moved to the front:
//
//	ffmpeg -i <src> -c copy -movflags +faststart <tmp>
//
// Stream copy only: no re-encode, quality unchanged. It returns the
// temp path (the caller deletes it after the upload), or "" when no
// remux is needed — the file is already faststart, ffmpeg is
// unavailable, or the remux failed, in which case the caller
// uploads the original file (a slow start beats no upload). The
// temp file uses a .tmp suffix so ScanLibrary never lists it as a
// clip while it exists.
func (m *RemoteManager) remuxFaststart(src string) string {
	if m.ffmpeg == "" || moovAtFront(src) {
		return ""
	}
	tmp, err := os.CreateTemp(filepath.Dir(src), ".faststart-*.tmp")
	if err != nil {
		return ""
	}
	tmpPath := tmp.Name()
	tmp.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.ffmpeg,
		"-hide_banner", "-loglevel", "warning", "-y",
		"-i", src, "-c", "copy", "-movflags", "+faststart",
		"-f", "mp4", tmpPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.Remove(tmpPath)
		startlog.Stage("remote: faststart remux failed for %s (%v: %s), uploading original",
			filepath.Base(src), err, trimErr(string(out)))
		return ""
	}
	if st, err := os.Stat(tmpPath); err != nil || st.Size() == 0 {
		os.Remove(tmpPath)
		return ""
	}
	return tmpPath
}
