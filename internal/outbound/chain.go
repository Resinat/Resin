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
}

func (d *chainDialer) pick(network string) (adapter.Outbound, node.Hash, error) {
	if d.manager.pickRelay == nil {
		return nil, node.Zero, routing.ErrNoAvailableRelay
	}
	hash, err := d.manager.pickRelay(d.landing, N.NetworkName(network))
	if err != nil {
		return nil, node.Zero, err
	}
	if hash == d.landing {
		return nil, node.Zero, routing.ErrNoAvailableRelay
	}
	entry, ok := d.manager.pool.GetEntry(hash)
	if !ok {
		return nil, node.Zero, routing.ErrNoAvailableRelay
	}
	ptr := entry.Outbound.Load()
	if ptr == nil {
		return nil, node.Zero, routing.ErrNoAvailableRelay
	}
	if entry.RoutingRole() != node.RoleRelay {
		return nil, node.Zero, routing.ErrNoAvailableRelay
	}
	if n, ok := (*ptr).(*nodeOutbound); ok && n.chain {
		return nil, node.Zero, routing.ErrNoAvailableRelay
	}

	return *ptr, hash, nil
}
func (d *chainDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	ob, hash, err := d.pick(network)
	if err != nil {
		return nil, err
	}
	if N.NetworkName(network) != N.NetworkTCP {
		return ob.DialContext(ctx, network, destination)
	}
	info := RelayInfo{Hash: hash, Tag: d.manager.pool.ResolveNodeDisplayTag(hash)}
	conn, err := ob.DialContext(ctx, network, destination)
	if err != nil {
		return nil, err
	}
	return &relayConn{Conn: conn, info: info}, nil
}
func (d *chainDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	ob, _, err := d.pick("udp")
	if err != nil {
		return nil, err
	}
	return ob.ListenPacket(ctx, destination)
}
