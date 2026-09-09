package routing

import "sync"

// PlatformRoutingState encapsulates the routing state for a single platform.
// This struct is stored in the Router's state map.
type PlatformRoutingState struct {
	Leases       *LeaseTable
	IPLoadStats  *IPLoadStats
	allocationMu sync.Mutex
}

// NewPlatformRoutingState creates a new state instance.
func NewPlatformRoutingState() *PlatformRoutingState {
	stats := NewIPLoadStats()
	return &PlatformRoutingState{
		Leases:      NewLeaseTable(stats),
		IPLoadStats: stats,
	}
}
