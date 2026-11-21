//go:build windows

package main

import (
	"syscall"
	"unsafe"

	"daybreaknvr/internal/startlog"
)

// singleInstanceName is the per-user named mutex that guarantees
// only one Daybreak NVR window can exist at a time. "Local\" scopes
// it to the current logon session.
const singleInstanceName = `Local\DaybreakNVR-SingleInstance`

const errorAlreadyExists = 183 // ERROR_ALREADY_EXISTS

var (
	kernel32        = syscall.NewLazyDLL("kernel32.dll")
	procCreateMutex = kernel32.NewProc("CreateMutexW")

	// singleInstanceHandle keeps the mutex alive for the life of
	// the process; closing it would release the single-instance
	// lock, so it is deliberately never closed.
	singleInstanceHandle syscall.Handle
)

// acquireSingleInstance tries to claim the named mutex. It reports
// true when this process is the first (only) instance. It reports
// false only when the mutex already existed — i.e. another copy is
// genuinely running. Any other failure (mutex API unavailable,
// name rejected, …) fails OPEN: it logs the problem and reports
// true, because a broken lock must never block the app itself.
func acquireSingleInstance() bool {
	name, err := syscall.UTF16PtrFromString(singleInstanceName)
	if err != nil {
		startlog.Stage("single-instance: bad mutex name (continuing): %v", err)
		return true
	}
	handle, _, callErr := procCreateMutex.Call(
		0, // default security descriptor
		0, // not initially owned — existence is the signal
		uintptr(unsafe.Pointer(name)),
	)
	if handle == 0 {
		startlog.Stage("single-instance: CreateMutexW failed (continuing): %v", callErr)
		return true
	}
	singleInstanceHandle = syscall.Handle(handle)
	if errno, ok := callErr.(syscall.Errno); ok && errno == errorAlreadyExists {
		return false
	}
	return true
}
