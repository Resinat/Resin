// Package service defines service-layer types used by API handlers.
package service

import (
	"time"
)

// SystemInfo contains version and runtime information.
type SystemInfo struct {
	Version   string    `json:"version"`
	GitCommit string    `json:"git_commit"`
	BuildTime string    `json:"build_time"`
	StartedAt time.Time `json:"started_at"`
}
