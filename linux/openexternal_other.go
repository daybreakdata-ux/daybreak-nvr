//go:build !windows

package main

import "os/exec"

// openExternal opens a file or folder with the OS default handler.
func openExternal(path string) error {
	return exec.Command("xdg-open", path).Start()
}
