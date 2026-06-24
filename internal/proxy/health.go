package proxy

import (
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/routing"
)

// HealthRecorder abstracts passive health feedback reporting.
// topology.GlobalNodePool satisfies this interface.
type HealthRecorder interface {
	RecordResult(hash node.Hash, success bool)
	RecordLatency(hash node.Hash, rawTarget string, latency *time.Duration)
}

type passiveHealthRecorder interface {
	RecordPassiveResult(platformID string, hash node.Hash, success bool)
}

func recordPassiveResultAsync(health HealthRecorder, route routing.RouteResult, success bool) {
	if health == nil {
		return
	}
	if recorder, ok := health.(passiveHealthRecorder); ok {
		go recorder.RecordPassiveResult(route.PlatformID, route.NodeHash, success)
		return
	}
	go health.RecordResult(route.NodeHash, success)
}
