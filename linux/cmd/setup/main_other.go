//go:build !windows

package main

import "fmt"

func main() {
	fmt.Println("Daybreak NVR setup must be built for Windows (GOOS=windows).")
}
