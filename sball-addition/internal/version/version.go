// Package version holds build metadata for sball-addition.
//
// Version/Commit/Date are overridable at build time:
//
//	go build -ldflags "-X sball-addition/internal/version.Version=1.2.3"
package version

import (
	"fmt"
	"runtime"
)

// Build metadata. Overridden with -ldflags at build time.
var (
	// Version is the semantic version of sball-addition.
	Version = "1.0.0"
	// Commit is the short git commit the binary was built from.
	Commit = "unknown"
	// BuildDate is the build timestamp (RFC3339, UTC) supplied by the build system.
	BuildDate = "unknown"
)

// Name is the program name used in log lines, help output and pid files.
const Name = "sball-addition"

// String returns a one line version description.
func String() string {
	return fmt.Sprintf("%s %s (commit %s, built %s, %s/%s, %s)",
		Name, Version, Commit, BuildDate, runtime.GOOS, runtime.GOARCH, runtime.Version())
}
