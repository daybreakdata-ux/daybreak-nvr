package engine

import (
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// DiskUsage reports total bytes and file count under the recordings dir.
func DiskUsage(root string) (total int64, files int) {
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
			files++
		}
		return nil
	})
	return total, files
}

// CameraDiskUsage is the recordings usage for one camera folder.
type CameraDiskUsage struct {
	CameraID   string
	CameraName string
	Bytes      int64
	Files      int
}

// DiskUsageByCamera reports usage per camera (by slug folder)
// plus the total. A camera with no folder yet reports 0.
func DiskUsageByCamera(s Settings) ([]CameraDiskUsage, int64, int) {
	out := make([]CameraDiskUsage, 0, len(s.Cameras))
	var total int64
	var files int
	for _, cam := range s.Cameras {
		b, n := DiskUsage(filepath.Join(s.RecordingsDir, cam.Slug()))
		out = append(out, CameraDiskUsage{CameraID: cam.ID, CameraName: cam.Name, Bytes: b, Files: n})
		total += b
		files += n
	}
	return out, total, files
}

// DiskFree reports the free and total bytes of the filesystem
// containing path (0s when the path cannot be stat'ed).
func DiskFree(path string) (free, total int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	total = int64(st.Blocks) * int64(st.Bsize)
	free = int64(st.Bavail) * int64(st.Bsize)
	return free, total
}

// RunRetention enforces the two storage rules, oldest first:
//  1. delete clips older than RetentionDays (by modification time);
//  2. if MaxTotalGB > 0 and the library still exceeds it, delete the
//     oldest clips until it fits.
//
// It returns how many files were deleted and how many bytes freed.
// Empty camera/date folders are removed as it goes.
func RunRetention(s Settings) (deleted int, freed int64) {
	root := s.RecordingsDir
	if root == "" {
		return 0, 0
	}
	type fileEnt struct {
		path string
		size int64
		mod  time.Time
	}
	var files []fileEnt
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			files = append(files, fileEnt{p, info.Size(), info.ModTime()})
		}
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })

	var total int64
	for _, f := range files {
		total += f.size
	}

	removed := map[string]bool{}
	remove := func(f fileEnt) {
		if err := os.Remove(f.path); err == nil {
			deleted++
			freed += f.size
			total -= f.size
			removed[f.path] = true
		}
	}

	if s.RetentionDays > 0 {
		cutoff := time.Now().Add(-time.Duration(s.RetentionDays) * 24 * time.Hour)
		for _, f := range files {
			if f.mod.Before(cutoff) {
				remove(f)
			}
		}
	}
	if s.MaxTotalGB > 0 {
		capBytes := int64(s.MaxTotalGB) << 30
		for _, f := range files {
			if total <= capBytes {
				break
			}
			if !removed[f.path] {
				remove(f)
			}
		}
	}

	// Prune empty date/camera folders (deepest first).
	var dirs []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() && p != root {
			dirs = append(dirs, p)
		}
		return nil
	})
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, d := range dirs {
		_ = os.Remove(d) // only succeeds when empty
	}
	return deleted, freed
}
