package node

import (
	"net/netip"
	"sync/atomic"
	"time"
)

type RoutingRole uint8

const (
	RoleOrdinary RoutingRole = iota
	RoleChain
	RoleRelay
)

// Versions are process-unique, including when a deleted hash is re-added.
var nextRoutingVersion atomic.Uint64

func (e *NodeEntry) RoutingRole() RoutingRole {
	return RoutingRole(e.routingState.Load() & 3)
}

func (e *NodeEntry) RoutingVersion() uint64 { return e.routingState.Load() >> 2 }

// RestoreRoutingMode is only used before bootstrap workers start. Cached
// health is tagged with its mode so a changed subscription invalidates it.
func (e *NodeEntry) RestoreRoutingMode(chain bool) {
	role := RoleOrdinary
	if chain {
		role = RoleChain
	}
	e.routingState.Store(e.RoutingVersion()<<2 | uint64(role))
}

// UpdateRoutingRole requires the owning pool's per-node atomic update.
func (e *NodeEntry) UpdateRoutingRole(role RoutingRole) (bool, []string) {

	previous := e.routingState.Load()
	oldRole := RoutingRole(previous & 3)
	if oldRole == role {
		return false, nil
	}
	version := previous >> 2
	changed := (oldRole == RoleChain) != (role == RoleChain)
	var domains []string
	if changed {
		version = nextRoutingVersion.Add(1)
		e.SetLastError("")
		e.FailureCount.Store(0)
		e.CircuitOpenSince.Store(time.Now().UnixNano())
		e.egressIP.Store(nil)
		e.LastEgressUpdate.Store(0)
		e.LastLatencyProbeAttempt.Store(0)
		e.LastAuthorityLatencyProbeAttempt.Store(0)
		e.LastEgressUpdateAttempt.Store(0)
		if e.LatencyTable != nil {
			domains = e.LatencyTable.Clear()
		}
	}
	e.routingState.Store(version<<2 | uint64(role))
	return changed, domains
}

// Feedback is applied to one specific published mode. Nil samples record an
// attempt; Result=nil means that no health conclusion can be drawn.
type Feedback struct {
	Result     *bool
	Target     string
	Latency    *time.Duration
	Egress     bool
	IP         *netip.Addr
	Region     *string
	LastError  *string
	PlatformID string
}
