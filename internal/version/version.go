// Package version holds build information, set at link time by GoReleaser.
package version

import (
	"fmt"
	"runtime"
)

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String returns a one-line human-readable version description.
func String() string {
	return fmt.Sprintf("tijori %s (commit %s, built %s, %s %s/%s)",
		Version, Commit, Date, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
