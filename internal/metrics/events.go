// Package metrics implements the metrics collection, aggregation, and storage subsystem.
package metrics

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
type ConnectionOp int

const (
	ConnOpen  ConnectionOp = iota
	ConnClose
)

// ConnectionDirection indicates inbound vs outbound.
type ConnectionDirection int

const (
	ConnInbound  ConnectionDirection = iota
	ConnOutbound
)

// ConnectionLifecycleEvent tracks connection open/close.
type ConnectionLifecycleEvent struct {
	Op        ConnectionOp
	Direction ConnectionDirection
}

// ProbeEvent is emitted on every probe attempt.
type ProbeEvent struct {
	Kind ProbeKind
}

// LeaseMetricEvent carries lease state changes for metrics aggregation.
type LeaseMetricEvent struct {
	PlatformID  string
	Op          string // "create", "replace", "remove", "expire"
	LifetimeNs  int64  // lifetime of removed/expired leases, 0 otherwise
}
