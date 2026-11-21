//go:build !windows

package main

// showErrorBox is a no-op off Windows (the startup log still gets
// the details; there is no native message box to show here).
func showErrorBox(title, text string) {}
