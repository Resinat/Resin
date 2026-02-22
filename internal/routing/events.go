package routing

import (
	"net/netip"

	"github.com/Resinat/Resin/internal/node"
)

type LeaseEventType int

const (
	LeaseCreate LeaseEventType = iota
	LeaseTouch
	LeaseReplace
	LeaseRemove
	LeaseExpire
)

type LeaseEvent struct {
	Type        LeaseEventType
	PlatformID  string
	Account     string
	NodeHash    node.Hash
	EgressIP    netip.Addr
	CreatedAtNs int64 // set on remove/expire for lifetime calculation
}

// LeaseEventFunc is invoked synchronously by routing/lease maintenance code.
// Keep handlers lightweight and non-blocking; push heavy work to async queues.
type LeaseEventFunc func(event LeaseEvent)
