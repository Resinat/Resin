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
	networks   []string
	closed     atomic.Bool
	checkRelay func() error
	upstream   *chainDialer
}

func (n *nodeOutbound) RoutingVersion() uint64  { return n.version }
func (n *nodeOutbound) ChainProxy() bool        { return n.upstream != nil }
func (n *nodeOutbound) Ready() bool             { return !n.closed.Load() }
func (n *nodeOutbound) RelayNetworks() []string { return n.networks }

// BoundRelay observes the existing binding without selecting or dialing a relay.
func (n *nodeOutbound) BoundRelay() node.Hash {
	if n.closed.Load() || n.upstream == nil {
		return node.Zero
	}
	bound := n.upstream.relay.Load()
	if bound == nil {
		return node.Zero
	}
	if _, ok := n.upstream.boundOutbound(bound, bound.network); !ok {
		return node.Zero
	}
	return bound.entry.Hash
}
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
	if n.upstream == nil {
		return n.Outbound.DialContext(ctx, network, destination)
	}
	attempt := new(chainAttempt)
	ctx = context.WithValue(ctx, chainAttemptKey{}, attempt)
	conn, err := n.Outbound.DialContext(ctx, network, destination)
	if bound := attempt.binding.Load(); bound != nil {
		err = (node.RelayInfo{Binding: bound}).WrapError(err)
	}
	return conn, err
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
	if n.upstream == nil {
		return n.Outbound.ListenPacket(ctx, destination)
	}
	attempt := new(chainAttempt)
	ctx = context.WithValue(ctx, chainAttemptKey{}, attempt)
	conn, err := n.Outbound.ListenPacket(ctx, destination)
	if bound := attempt.binding.Load(); bound != nil {
		err = (node.RelayInfo{Binding: bound}).WrapError(err)
	}
	return conn, err
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

// One immutable publication identifies both the selection and its lifetime.
type relayBinding struct {
	owner             *chainDialer
	entry             *node.NodeEntry
	network           string
	landingGeneration uint64
	relayGeneration   uint64
}

func (b *relayBinding) Invalidate() { b.owner.relay.CompareAndSwap(b, nil) }

func (b *relayBinding) hash() node.Hash {
	if b != nil {
		return b.entry.Hash
	}
	return node.Zero
}

func (d *chainDialer) pick(network string) (adapter.Outbound, *relayBinding, error) {
	network = N.NetworkName(network)
	for attempt := 0; attempt < 3; attempt++ {
		bound := d.relay.Load()
		if bound != nil {
			if ob, ok := d.boundOutbound(bound, network); ok {
				return ob, bound, nil
			}
		}
		landingGeneration := d.landingGeneration()
		if d.manager.pickRelay == nil {
			return nil, nil, routing.ErrNoAvailableRelay
		}
		hash, err := d.manager.pickRelay(d.landing, network)
		if err != nil {
			return nil, nil, err
		}
		entry, _ := d.manager.pool.GetEntry(hash)
		var relayGeneration uint64
		if entry != nil {
			relayGeneration = entry.CircuitGeneration.Load()
		}
		ob, ok := d.relayOutbound(entry, network)
		if !ok {
			return nil, nil, routing.ErrNoAvailableRelay
		}
		next := &relayBinding{
			owner: d, entry: entry, network: network,
			landingGeneration: landingGeneration, relayGeneration: relayGeneration,
		}
		if landingGeneration != d.landingGeneration() {
			continue
		}
		if d.relay.CompareAndSwap(bound, next) {
			return ob, next, nil
		}
		// A concurrent dial published its choice first. Reuse that binding
		// instead of dialing our independently selected candidate.
	}
	return nil, nil, routing.ErrNoAvailableRelay
}

func (d *chainDialer) landingGeneration() uint64 {
	if entry, ok := d.manager.pool.GetEntry(d.landing); ok {
		return entry.CircuitGeneration.Load()
	}
	return 0
}

func (d *chainDialer) boundOutbound(bound *relayBinding, network string) (adapter.Outbound, bool) {
	ob, ok := d.relayOutbound(bound.entry, network)
	if !ok {
		return nil, false
	}
	if bound.landingGeneration != d.landingGeneration() || bound.relayGeneration != bound.entry.CircuitGeneration.Load() {
		return nil, false
	}
	return ob, true
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
	if n, ok := (*ptr).(*nodeOutbound); ok && n.ChainProxy() {
		return nil, false
	}
	return *ptr, true
}
func (d *chainDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	ob, bound, err := d.pick(network)
	if err != nil {
		return nil, err
	}
	hash := bound.hash()
	info := node.RelayInfo{Hash: hash, Tag: d.manager.pool.ResolveNodeDisplayTag(hash), Version: outboundVersion(ob), Binding: bound}
	if attempt, ok := ctx.Value(chainAttemptKey{}).(*chainAttempt); ok {
		attempt.binding.Store(bound)
	}
	conn, err := ob.DialContext(ctx, network, destination)
	if N.NetworkName(network) != N.NetworkTCP {
		return conn, info.WrapError(relayError(hash, outboundVersion(ob), err))
	}
	if err != nil {
		return nil, info.WrapError(relayError(hash, outboundVersion(ob), err))
	}
	return &relayConn{Conn: conn, info: info}, nil
}
func (d *chainDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	ob, bound, err := d.pick("udp")
	if err != nil {
		return nil, err
	}
	hash := bound.hash()
	info := node.RelayInfo{Hash: hash, Tag: d.manager.pool.ResolveNodeDisplayTag(hash), Version: outboundVersion(ob), Binding: bound}
	if attempt, ok := ctx.Value(chainAttemptKey{}).(*chainAttempt); ok {
		attempt.binding.Store(bound)
	}
	conn, err := ob.ListenPacket(ctx, destination)
	return conn, info.WrapError(relayError(hash, outboundVersion(ob), err))
}

type chainAttemptKey struct{}

// Only a detour dial made with this call's context supplies failure metadata.
// Reusing a hidden native session does not identify a new relay binding.
type chainAttempt struct {
	binding atomic.Pointer[relayBinding]
}
