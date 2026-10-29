// Package version holds build information, set at link time by GoReleaser.
// Binaries built with plain `go build` from a git checkout or with
// `go install ...@vX.Y.Z` fill it in from the build information Go embeds.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

func init() {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	if v := bi.Main.Version; Version == "dev" && v != "" && v != "(devel)" {
		Version = strings.TrimPrefix(v, "v")
	}
	var fromVCS, dirty bool
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if Commit == "none" && s.Value != "" {
				Commit = s.Value[:min(7, len(s.Value))]
				fromVCS = true
			}
		case "vcs.time":
			if Date == "unknown" && s.Value != "" {
				Date = s.Value
			}
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if fromVCS && dirty {
		Commit += "-dirty"
	}
}

// String returns a one-line human-readable version description.
func String() string {
	return fmt.Sprintf("tijori %s (commit %s, built %s, %s %s/%s)",
		Version, Commit, Date, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// Build describes this binary for the admin panel.
type Build struct {
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Date     string `json:"date"`
	Go       string `json:"go"`
	Platform string `json:"platform"`
}

// Info returns the build information of this binary.
func Info() Build {
	return Build{
		Version: Version, Commit: Commit, Date: Date,
		Go: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH,
	}
}
