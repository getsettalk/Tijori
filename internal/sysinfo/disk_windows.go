//go:build windows

package sysinfo

// DiskFree is not implemented on Windows and returns -1.
func DiskFree(path string) int64 { return -1 }
