// Package buildinfo holds version information injected at build time via ldflags.
package buildinfo

// Set via -ldflags at build time:
//
//	go build -ldflags "-X github.com/resin-proxy/resin/internal/buildinfo.Version=1.0.0 ..."
var (
	Version   = "dev"
	GitCommit = "unknown"
	BuildTime = "unknown"
)
