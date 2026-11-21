//go:build windows

package engine

import (
	"os/exec"
	"syscall"
)

// hideWindow stops ffmpeg console windows from flashing up on Windows.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
