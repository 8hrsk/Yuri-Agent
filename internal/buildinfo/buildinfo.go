// Package buildinfo contains release metadata injected by the build pipeline.
package buildinfo

var (
	// Version is a SemVer without the leading v. Release builds override it
	// through -ldflags; the default keeps local builds compatible with the
	// current plugin API baseline.
	Version = "0.7.0"
	Commit  = "unknown"
	Date    = "unknown"
)
