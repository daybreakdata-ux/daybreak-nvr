// Package startlog is a tiny, dependency-free startup logger for
// Daybreak NVR. The Windows build is a GUI-subsystem binary: when
// Explorer launches it there is no console, so a panic or fatal
// error during startup vanishes without a trace and the app simply
// "never opens". Writing a flushed, timestamped line at every
// startup stage — before anything else runs — makes any such
// failure diagnosable after the fact.
//
// Every write is open-append-sync-close so the last line on disk
// always names the stage that was in progress when the process
// died, even if it died hard.
package startlog

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"
)

var (
	mu   sync.Mutex
	path string
)

// DefaultCandidates returns the startup.log locations to try, most
// preferred first: %LOCALAPPDATA%\DaybreakNVR (always writable for
// the installing user), the OS config dir, beside the executable,
// and finally the temp dir.
func DefaultCandidates() []string {
	var dirs []string
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		dirs = append(dirs, filepath.Join(la, "DaybreakNVR"))
	}
	if cfg, err := os.UserConfigDir(); err == nil && cfg != "" {
		dirs = append(dirs, filepath.Join(cfg, "DaybreakNVR"))
	}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	dirs = append(dirs, os.TempDir())
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		out = append(out, filepath.Join(d, "startup.log"))
	}
	return out
}

// Pick returns the first candidate path that can be created and
// appended to, or "" if none work.
func Pick(candidates []string) string {
	for _, p := range candidates {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			continue
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			continue
		}
		_ = f.Close()
		return p
	}
	return ""
}

// Init selects the log file from the default candidates. Call it
// before anything else in main. Returns the chosen path ("" if no
// location was writable — logging then silently no-ops).
func Init() string {
	return InitFrom(DefaultCandidates())
}

// InitFrom selects the log file from explicit candidates (tests).
func InitFrom(candidates []string) string {
	p := Pick(candidates)
	mu.Lock()
	path = p
	mu.Unlock()
	if p != "" {
		// Keep the log bounded: rotate a big previous log aside.
		if st, err := os.Stat(p); err == nil && st.Size() > 512*1024 {
			_ = os.Rename(p, p+".old")
		}
	}
	return p
}

// Path returns the selected log file path ("" if none).
func Path() string {
	mu.Lock()
	defer mu.Unlock()
	return path
}

// Stage appends one timestamped line and flushes it to disk.
func Stage(format string, args ...any) {
	mu.Lock()
	p := path
	mu.Unlock()
	if p == "" {
		return
	}
	line := time.Now().Format("2006-01-02 15:04:05.000") + "  " +
		fmt.Sprintf(format, args...) + "\n"
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line)
	_ = f.Sync()
	_ = f.Close()
}

// LogPanic records a recovered panic value with its stack trace and
// returns a one-line summary suitable for a message box.
func LogPanic(where string, r any) string {
	summary := fmt.Sprintf("panic in %s: %v", where, r)
	Stage("PANIC: %s\n%s", summary, debug.Stack())
	return summary
}

// Go runs f in a goroutine with panic capture: a panic is written
// to the startup log instead of silently killing a background
// goroutine (or, worse, the whole process).
func Go(where string, f func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				LogPanic(where, r)
			}
		}()
		f()
	}()
}

// Guard runs f on the current goroutine with panic capture and
// reports whether it panicked.
func Guard(where string, f func()) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			LogPanic(where, r)
			panicked = true
		}
	}()
	f()
	return false
}
