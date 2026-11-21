//go:build windows

package main

import (
	"os/exec"
)

// openExternal opens a file with the Windows default player.
func openExternal(path string) error {
	cmd := exec.Command("cmd", "/c", "start", "", path)
	return cmd.Start()
}
