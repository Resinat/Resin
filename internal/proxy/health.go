package proxy

import (
	"errors"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/routing"
	"time"
)

// Feedback always identifies the exact outbound mode used by the request.
type HealthRecorder interface {
	ApplyFeedback(node.Hash, uint64, node.Feedback) bool
}

func recordPassiveResultAsync(health HealthRecorder, route routing.RouteResult, success bool) {
	if health != nil {
		go health.ApplyFeedback(route.NodeHash, route.NodeVersion, node.Feedback{Result: &success, PlatformID: route.PlatformID})
	}
}
func recordPassiveFailureAsync(health HealthRecorder, route routing.RouteResult, err error) {
	if !errors.Is(err, routing.ErrNoAvailableRelay) {
		recordPassiveResultAsync(health, route, false)
	}
}
func recordLatencyForVersion(health HealthRecorder, hash node.Hash, version uint64, domain string, latency *time.Duration) {
	if health != nil {
		health.ApplyFeedback(hash, version, node.Feedback{Target: domain, Latency: latency})
	}
}
