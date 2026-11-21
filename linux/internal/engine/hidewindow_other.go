//go:build !windows

package engine

import "os/exec"

// hideWindow is a no-op off Windows.
func hideWindow(cmd *exec.Cmd) {}
