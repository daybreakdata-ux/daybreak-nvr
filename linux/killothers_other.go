//go:build !linux

package main

// Kill-others on launch is a Linux-only feature (it scans /proc).
// Off Linux the launch sequence is just the single-instance lock,
// unchanged from before v1.7.

func killOtherNVRInstances() {}

// takeOverSingleInstance off Linux: no other instances to kill,
// so this is exactly the old acquireSingleInstance behaviour.
func takeOverSingleInstance() bool {
	return acquireSingleInstance()
}
