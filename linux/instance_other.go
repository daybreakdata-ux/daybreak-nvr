//go:build !windows && !linux

package main

// acquireSingleInstance is a stub off Windows: there is no other
// instance to worry about in dev/test builds, so always report
// "first instance".
func acquireSingleInstance() bool { return true }
