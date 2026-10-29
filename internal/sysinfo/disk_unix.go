//go:build !windows

// Package sysinfo has small OS helpers used by `tijori doctor`.
package sysinfo

import "syscall"

// DiskFree returns available bytes on the filesystem holding path, or -1.
func DiskFree(path string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return -1
	}
	return int64(uint64(st.Bavail) * uint64(st.Bsize))
}
