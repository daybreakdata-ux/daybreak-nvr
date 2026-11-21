//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// showErrorBox displays a native Windows message box. The app is a
// GUI-subsystem binary with no console, so this is the only way to
// tell the user a startup failure happened (the details are in the
// startup log).
func showErrorBox(title, text string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	messageBox := user32.NewProc("MessageBoxW")
	t, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	b, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	const (
		mbOK            = 0x00000000
		mbIconError     = 0x00000010
		mbSetForeground = 0x00010000
	)
	_, _, _ = messageBox.Call(0,
		uintptr(unsafe.Pointer(b)),
		uintptr(unsafe.Pointer(t)),
		uintptr(mbOK|mbIconError|mbSetForeground))
}
