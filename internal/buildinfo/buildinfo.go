// Package buildinfo holds identifiers stamped into release binaries at link time.
//
// Override the defaults at link time with -ldflags "-X <module>/internal/buildinfo.Version=v1.2.3"
// (scripts/build.sh and scripts/build.ps1 do this for release builds).
package buildinfo

// Version, Commit and Date are set via -ldflags -X during release builds.
var (
	// Version is the release version (a git tag, or "dev" for local builds).
	Version = "dev"
	// Commit is the short git commit hash the binary was built from.
	Commit = "unknown"
	// Date is the UTC build timestamp in RFC 3339 form.
	Date = "unknown"
)
