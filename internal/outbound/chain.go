package outbound

import (
	"context"
	"encoding/json"
	"net"
	"sync/atomic"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/sagernet/sing-box/adapter"
	sbOutbound "github.com/sagernet/sing-box/adapter/outbound"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

const resinDetourTag = "resin-chain"

func newChainAdapter() sbOutbound.Adapter {
	return sbOutbound.NewAdapter("resin-chain", resinDetourTag, []string{"tcp", "udp"}, nil)
}

type detourBuilder interface {
	BuildWithDetour(json.RawMessage, adapter.Outbound) (adapter.Outbound, error)
}

type outboundRegistryScope struct{ local, parent service.Registry }

func (r *outboundRegistryScope) Register(key, value any) any { return r.local.Register(key, value) }
func (r *outboundRegistryScope) Get(key any) any {
	if value := r.local.Get(key); value != nil {
		return value
	}
	return r.parent.Get(key)
}

type detourManager struct {
	adapter.OutboundManager
	upstream adapter.Outbound
}

func (m *detourManager) Outbound(tag string) (adapter.Outbound, bool) {
	if tag == resinDetourTag {
		return m.upstream, true
	}
	return m.OutboundManager.Outbound(tag)
}

// A published outbound owns exactly one native instance and one immutable mode.
// Old requests retain its version so their feedback cannot affect a new mode.
type nodeOutbound struct {
	adapter.Outbound
	version    uint64
	chain      bool
	networks   []string
	closed     atomic.Bool
	checkRelay func() error
}

func (n *nodeOutbound) RoutingVersion() uint64  { return n.version }
func (n *nodeOutbound) ChainProxy() bool        { return n.chain }
func (n *nodeOutbound) Ready() bool             { return !n.closed.Load() }
func (n *nodeOutbound) RelayNetworks() []string { return n.networks }
func (n *nodeOutbound) Close() error {
	if !n.closed.Swap(true) {
		closeOutbound(n.Outbound)
	}
	return nil
}
func (n *nodeOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if n.closed.Load() {
		return nil, ErrOutboundNotReady
	}
	if n.checkRelay != nil {
		if err := n.checkRelay(); err != nil {
			return nil, err
		}
	}
	return n.Outbound.DialContext(ctx, network, destination)
}
func (n *nodeOutbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	if n.closed.Load() {
		return nil, ErrOutboundNotReady
	}
	if n.checkRelay != nil {
		if err := n.checkRelay(); err != nil {
			return nil, err
		}
	}
	return n.Outbound.ListenPacket(ctx, destination)
}

type builderFunc func(json.RawMessage) (adapter.Outbound, error)

func (f builderFunc) Build(raw json.RawMessage) (adapter.Outbound, error) { return f(raw) }

type chainDialer struct {
	sbOutbound.Adapter
	manager *OutboundManager
	landing node.Hash
	// The binding belongs to this landing outbound, not to individual dials.
	// Store node identity only; fetch its current native instance on each use.
	relay atomic.Pointer[relayBinding]
}

// Each publication has its own identity, even when a relay is selected again.
// An old CAS must not mistake a newer A binding for the earlier A binding.
type relayBinding struct {
	entry *node.NodeEntry
}

func (d *chainDialer) pick(network string) (adapter.Outbound, node.Hash, error) {
	network = N.NetworkName(network)
	for attempt := 0; attempt < 3; attempt++ {
		bound := d.relay.Load()
		if bound != nil {
			if ob, ok := d.relayOutbound(bound.entry, network); ok {
				return ob, bound.entry.Hash, nil
			}
		}
		if d.manager.pickRelay == nil {
			return nil, node.Zero, routing.ErrNoAvailableRelay
		}
		hash, err := d.manager.pickRelay(d.landing, network)
		if err != nil {
			return nil, node.Zero, err
		}
		entry, _ := d.manager.pool.GetEntry(hash)
		ob, ok := d.relayOutbound(entry, network)
		if !ok {
			return nil, node.Zero, routing.ErrNoAvailableRelay
		}
		if d.relay.CompareAndSwap(bound, &relayBinding{entry: entry}) {
			return ob, hash, nil
		}
		// A concurrent dial published its choice first. Reuse that binding
		// instead of dialing our independently selected candidate.
	}
	return nil, node.Zero, routing.ErrNoAvailableRelay
}

func (d *chainDialer) relayOutbound(entry *node.NodeEntry, network string) (adapter.Outbound, bool) {
	if entry == nil || entry.Hash == d.landing || !d.manager.pool.IsRelayEligible(entry.Hash, network) ||
		!d.manager.isLiveEntry(entry.Hash, entry) {
		return nil, false
	}
	ptr := entry.Outbound.Load()
	if ptr == nil || entry.RoutingRole() != node.RoleRelay {
		return nil, false
	}
	if n, ok := (*ptr).(*nodeOutbound); ok && n.chain {
		return nil, false
	}
	return *ptr, true
}
func (d *chainDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	ob, hash, err := d.pick(network)
	if err != nil {
		return nil, err
	}
	if N.NetworkName(network) != N.NetworkTCP {
		conn, err := ob.DialContext(ctx, network, destination)
		return conn, relayError(hash, outboundVersion(ob), err)
	}
	info := node.RelayInfo{Hash: hash, Tag: d.manager.pool.ResolveNodeDisplayTag(hash), Version: outboundVersion(ob)}
	conn, err := ob.DialContext(ctx, network, destination)
	if err != nil {
		return nil, relayError(hash, outboundVersion(ob), err)
	}
	return &relayConn{Conn: conn, info: info}, nil
}
func (d *chainDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	ob, hash, err := d.pick("udp")
	if err != nil {
		return nil, err
	}
	conn, err := ob.ListenPacket(ctx, destination)
	return conn, relayError(hash, outboundVersion(ob), err)
}
