//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"daybreaknvr/internal/startlog"
)

// Kill-others on launch (v1.7).
//
// The 2026-11-14 incident: the GUI was restarted into a new version,
// but THREE orphaned -segment_time 10 ffmpeg recorders from the old
// version kept running (their parent was dead), writing 10 s chunks
// alongside the new GUI's own recorder — two recorders writing the
// same back-yard files at once, and up to 4 concurrent RTSP
// connections per camera (which also made live video laggy). A plain
// GUI restart did not fix it, because the orphans were not the new
// process's children.
//
// So every launch now terminates ALL other NVR processes before
// taking the single-instance lock: the main binary and the bundled
// ffmpeg workers (including orphans). After this runs, exactly one
// NVR is left — this one. This replaces the old "already running"
// dead-end box.

// nvrBinaryNames are the main-binary basenames. Matching by basename
// (not full path) covers the installed binary (/usr/bin or
// /usr/lib/...), the build-tree binary and a renamed variant alike —
// no other program is called this. Kept as a slice (not a map): the
// renamed-variant build renames "daybreak-nvr" to
// "daybreak-nvr-linux" by sed, which would turn a map literal into
// duplicate keys; a slice just dedupes harmlessly.
var nvrBinaryNames = []string{"daybreak-nvr", "daybreak-nvr-linux"}

// nvrFFmpegPaths are the ONLY ffmpeg binaries this matches: the
// bundled static builds shipped in the two packages. A system ffmpeg
// (/usr/bin/ffmpeg, or any other path) is never matched, so unrelated
// transcoding work is never touched. Slice, not a map, for the same
// sed reason as nvrBinaryNames.
var nvrFFmpegPaths = []string{
	"/usr/lib/daybreak-nvr/ffmpeg",
	"/usr/lib/daybreak-nvr-linux/ffmpeg",
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// nvrProc is one other NVR process found in /proc.
type nvrProc struct {
	pid      int
	headless bool   // cmdline contains --headless (a service instance)
	cmdline  string // space-joined cmdline, for the log
}

// matchNVRProcess reports whether a process (by its executable path
// and argv) is an NVR process: the main binary (by basename, either
// on the exe path or argv[0]) or the bundled ffmpeg (by exact path,
// either on the exe path or argv[0]). It also reports whether the
// process is a headless service instance (any arg is --headless).
// Pure function — unit-tested.
func matchNVRProcess(exe string, argv []string) (isNVR bool, isHeadless bool) {
	if containsStr(nvrBinaryNames, filepath.Base(exe)) {
		isNVR = true
	}
	if len(argv) > 0 && containsStr(nvrBinaryNames, filepath.Base(argv[0])) {
		isNVR = true
	}
	if containsStr(nvrFFmpegPaths, exe) {
		isNVR = true
	}
	if len(argv) > 0 && containsStr(nvrFFmpegPaths, argv[0]) {
		isNVR = true
	}
	if !isNVR {
		return false, false
	}
	for _, a := range argv {
		if a == "--headless" {
			isHeadless = true
			break
		}
	}
	return isNVR, isHeadless
}

// findNVRProcessesIn scans a procfs-style root for other NVR
// processes (excluding selfPID). procRoot is /proc in production;
// tests point it at a fake tree. A process is skipped if its exe
// link and cmdline are both unreadable (kernel threads, or a
// process that exited mid-scan).
func findNVRProcessesIn(procRoot string, selfPID int) []nvrProc {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	var found []nvrProc
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == selfPID || pid <= 0 {
			continue
		}
		dir := filepath.Join(procRoot, e.Name())
		exe, _ := os.Readlink(filepath.Join(dir, "exe"))
		raw, err := os.ReadFile(filepath.Join(dir, "cmdline"))
		if err != nil {
			raw = nil
		}
		if exe == "" && len(raw) == 0 {
			continue // kernel thread, or gone already
		}
		argv := splitCmdline(raw)
		isNVR, isHeadless := matchNVRProcess(exe, argv)
		if !isNVR {
			continue
		}
		found = append(found, nvrProc{
			pid:      pid,
			headless: isHeadless,
			cmdline:  strings.Join(argv, " "),
		})
	}
	return found
}

// splitCmdline splits NUL-separated /proc/<pid>/cmdline bytes into
// argv, dropping the trailing empty element.
func splitCmdline(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	parts := strings.Split(string(raw), "\x00")
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// processAlive reports whether pid is a live (non-zombie) process.
// A zombie has already released its locks, so it counts as gone.
func processAlive(pid int) bool {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		// No /proc entry (or no permission): fall back to signal 0.
		return syscall.Kill(pid, 0) == nil
	}
	// /proc/<pid>/stat: pid (comm) state ... — comm may contain
	// spaces/parens, so find the state after the last ')'.
	rest := string(stat)
	if i := strings.LastIndex(rest, ")"); i >= 0 && i+2 < len(rest) {
		return rest[i+2] != 'Z'
	}
	return syscall.Kill(pid, 0) == nil
}

// killOtherNVRInstances terminates every other NVR process on the
// machine: SIGTERM, wait up to ~2 s for them to exit, then SIGKILL
// any survivors. It never touches this process (self PID excluded),
// never touches unrelated ffmpeg (only the bundled paths match),
// and never touches the caller's shell/SSH session (those are not
// NVR processes). Errors signalling a process (already gone) are
// ignored; a headless service instance being killed is logged —
// the GUI takes over from it.
func killOtherNVRInstances() {
	self := os.Getpid()
	others := findNVRProcessesIn("/proc", self)
	if len(others) == 0 {
		return
	}
	var headless int
	for _, p := range others {
		if p.headless {
			headless++
		}
		_ = syscall.Kill(p.pid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(2 * time.Second)
	remaining := others
	for time.Now().Before(deadline) && len(remaining) > 0 {
		time.Sleep(100 * time.Millisecond)
		kept := remaining[:0]
		for _, p := range remaining {
			if processAlive(p.pid) {
				kept = append(kept, p)
			}
		}
		remaining = kept
	}
	for _, p := range remaining {
		_ = syscall.Kill(p.pid, syscall.SIGKILL)
	}
	// One last short grace for the SIGKILLs to land.
	for i := 0; i < 5; i++ {
		alive := false
		for _, p := range remaining {
			if processAlive(p.pid) {
				alive = true
				break
			}
		}
		if !alive {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	startlog.Stage("kill-others: terminated %d other NVR process(es) (%d needed SIGKILL)",
		len(others), len(remaining))
	if headless > 0 {
		startlog.Stage("kill-others: a headless service instance was stopped; the GUI takes over")
	}
}

// takeOverSingleInstance is the v1.7 launch sequence: kill every
// other NVR instance, drop any stale lock file, then take the
// single-instance lock. Returns false only if the lock itself
// cannot be taken (another copy genuinely started in between).
func takeOverSingleInstance() bool {
	killOtherNVRInstances()
	_ = os.Remove(lockFilePath())
	return acquireSingleInstance()
}
