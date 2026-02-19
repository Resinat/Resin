// Package metrics implements the metrics collection, aggregation, and storage subsystem.
package metrics

import "github.com/resin-proxy/resin/internal/proxy"

// ProbeKind represents the type of probe that was executed.
type ProbeKind string

const (
	ProbeKindEgress  ProbeKind = "egress"
	ProbeKindLatency ProbeKind = "latency"
)

// TrafficDeltaEvent reports byte counts from a countingConn flush.
type TrafficDeltaEvent struct {
	PlatformID   string
	IngressBytes int64
	EgressBytes  int64
}

// ConnectionOp is the operation type for a connection lifecycle event.
type ConnectionOp = proxy.ConnectionOp

const (
	ConnOpen  ConnectionOp = proxy.ConnectionOpen
	ConnClose ConnectionOp = proxy.ConnectionClose
)

// ConnectionDirection indicates inbound vs outbound.
type ConnectionDirection = proxy.ConnectionDirection

const (
	ConnInbound  ConnectionDirection = proxy.ConnectionInbound
	ConnOutbound ConnectionDirection = proxy.ConnectionOutbound
)

// ConnectionLifecycleEvent tracks connection open/close.
type ConnectionLifecycleEvent = proxy.ConnectionLifecycleEvent

// ProbeEvent is emitted on every probe attempt.
type ProbeEvent struct {
	Kind ProbeKind
}

// LeaseOp is the operation type for a lease lifecycle event.
type LeaseOp int

const (
	LeaseOpCreate LeaseOp = iota
	LeaseOpTouch
	LeaseOpReplace
	LeaseOpRemove
	LeaseOpExpire
)

// HasLifetimeSample reports whether this op can carry a lease lifetime sample.
func (op LeaseOp) HasLifetimeSample() bool {
	return op == LeaseOpRemove || op == LeaseOpExpire
}

// LeaseMetricEvent carries lease state changes for metrics aggregation.
type LeaseMetricEvent struct {
	PlatformID string
	Op         LeaseOp
	LifetimeNs int64 // lifetime of removed/expired leases, 0 otherwise
}
