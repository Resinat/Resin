package proxy

import (
	"context"
	"errors"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/routing"
)

// Feedback always identifies the exact outbound mode used by the request.
type HealthRecorder interface {
	ApplyFeedback(node.Hash, uint64, node.Feedback) bool
}

func recordPassiveResultAsync(health HealthRecorder, route routing.RouteResult, success bool, relay node.RelayInfo) {
	if health != nil {
		go health.ApplyFeedback(route.NodeHash, route.NodeVersion, node.Feedback{Result: &success, PlatformID: route.PlatformID, Relay: relay})
	}
}
func recordPassiveFailureAsync(health HealthRecorder, route routing.RouteResult, err error, relay node.RelayInfo) {
	if health != nil && !errors.Is(err, routing.ErrNoAvailableRelay) && !errors.Is(err, context.Canceled) {
		failed := false
		go health.ApplyFeedback(route.NodeHash, route.NodeVersion, node.Feedback{Result: &failed, Err: err, PlatformID: route.PlatformID, Relay: relay})
	}
}
func recordLatencyForVersion(health HealthRecorder, hash node.Hash, version uint64, domain string, latency *time.Duration) {
	if health != nil {
		health.ApplyFeedback(hash, version, node.Feedback{Target: domain, Latency: latency})
	}
}
