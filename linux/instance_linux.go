//go:build linux

package main

import (
	"os"
	"path/filepath"
	"syscall"

	"daybreaknvr/internal/engine"
)

// acquireSingleInstance uses an flock on a lock file in the config
// dir, so a second launch (desktop or headless) exits instead of
// fighting the first copy over the web port and the recorders.
// The lock is held for the life of the process and released by the
// OS on exit, including on a crash.
var instanceLockFile *os.File

// lockFilePath is the flock file in the config dir. In the
// renamed variant the sed rename appends "-linux" to the lock
// name, matching that variant's config dir.
func lockFilePath() string {
	return filepath.Join(filepath.Dir(engine.ConfigPath()), "daybreak-nvr.lock")
}

func acquireSingleInstance() bool {
	dir := filepath.Dir(engine.ConfigPath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return true // cannot lock — don't block startup over it
	}
	f, err := os.OpenFile(lockFilePath(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return false
	}
	instanceLockFile = f
	return true
}
