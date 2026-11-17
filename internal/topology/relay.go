package topology

import (
	"github.com/sagernet/sing-box/adapter"
	"io"
	"math/rand/v2"
	"slices"
	"sync/atomic"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/puzpuzpuz/xsync/v4"
)

type relayKey struct{ region, network string }

func (p *GlobalNodePool) SetOnNodeModeChanged(fn func(node.Hash)) { p.onNodeModeChanged = fn }

func (p *GlobalNodePool) SetOnRelayAvailable(fn func()) { p.onRelayAvailable = fn }

// ReconcileNodeRole is the one aggregation point for config and binding changes.
// All still-held subscriptions contribute, including disabled subscriptions.
func (p *GlobalNodePool) ReconcileNodeRole(hash node.Hash) {
	p.reconcileNodeRole(hash, true)
}

func (p *GlobalNodePool) reconcileNodeRole(hash node.Hash, notify bool) {
	var changed bool
	var domains []string
	var retired *adapter.Outbound
	p.nodes.Compute(hash, func(entry *node.NodeEntry, ok bool) (*node.NodeEntry, xsync.ComputeOp) {
		if !ok {
			return entry, xsync.CancelOp
		}
		role := func() node.RoutingRole {
			role := node.RoleOrdinary
			if p.subLookup == nil {
				return role
			}
			for _, id := range entry.SubscriptionIDs() {
				sub := p.subLookup(id)
				if sub == nil {
					continue
				}
				managed, held := sub.ManagedNodes().LoadNode(hash)
				if !held || managed.Evicted {
					continue
				}
				proxy, relay := sub.ChainConfig()
				if relay {
					return node.RoleRelay
				}
				if proxy {
					role = node.RoleChain
				}
			}
			return role
		}()
		// Revoke the old outbound before publishing the new mode/version.
		// A probe that observes the new version can never load the old pointer.
		if (entry.RoutingRole() == node.RoleChain) != (role == node.RoleChain) {
			retired = entry.Outbound.Swap(nil)
		}
		changed, domains = entry.UpdateRoutingRole(role)
		return entry, xsync.CancelOp
	})
	if retired != nil {
		if closer, ok := (*retired).(io.Closer); ok {
			_ = closer.Close()
		}
	}

	if changed {
		if p.onNodeDynamicChanged != nil {
			p.onNodeDynamicChanged(hash)
		}
		if p.onNodeLatencyChanged != nil {
			for _, domain := range domains {
				p.onNodeLatencyChanged(hash, domain)
			}
		}
		if p.onNodeModeChanged != nil {
			p.onNodeModeChanged(hash)
		}
	}
	if notify {
		p.notifyAllPlatformsDirty(hash)
	}
}

func (p *GlobalNodePool) NodeRegion(hash node.Hash) string {
	if e, ok := p.GetEntry(hash); ok {
		return e.GetRegion(p.geoLookup)
	}
	return ""
}

func relayNetworks(e *node.NodeEntry) []string {
	if ptr := e.Outbound.Load(); ptr != nil {
		if capable, ok := (*ptr).(interface{ RelayNetworks() []string }); ok {
			return capable.RelayNetworks()
		}
		return (*ptr).Network()
	}
	return nil
}
func (p *GlobalNodePool) IsRelayEligible(hash node.Hash, network string) bool {
	e, ok := p.GetEntry(hash)
	return ok && e.RoutingRole() == node.RoleRelay && e.IsHealthy() && !p.IsNodeDisabled(hash) && slices.Contains(relayNetworks(e), network)
}
func (p *GlobalNodePool) RelayView(region, network string) platform.ReadOnlyView {
	if view, ok := p.relayViews.Load(relayKey{region, network}); ok {
		return view
	}
	return emptyRelays
}
func (p *GlobalNodePool) refreshRelay(hash node.Hash) {
	available := false
	p.relayMembership.Compute(hash, func(old []relayKey, _ bool) ([]relayKey, xsync.ComputeOp) {
		var keys []relayKey
		e, ok := p.GetEntry(hash)
		if ok && e.RoutingRole() == node.RoleRelay && e.IsHealthy() && !p.IsNodeDisabled(hash) {
			region := e.GetRegion(p.geoLookup)
			for _, network := range relayNetworks(e) {
				keys = append(keys, relayKey{"", network})
				if region != "" {
					keys = append(keys, relayKey{region, network})
				}
			}
		}
		if slices.Equal(old, keys) {
			return old, xsync.CancelOp
		}
		for _, key := range old {
			if !slices.Contains(keys, key) {
				if set, ok := p.relayViews.Load(key); ok {
					set.remove(hash)
				}
			}
		}
		for _, key := range keys {
			if !slices.Contains(old, key) {
				set, _ := p.relayViews.LoadOrCompute(key, func() (*relaySet, bool) { return new(relaySet), false })
				first := set.add(hash)
				available = available || (key.region == "" && first)
			}
		}
		if len(keys) == 0 {
			return nil, xsync.DeleteOp
		}
		return keys, xsync.UpdateOp
	})
	if available && p.onRelayAvailable != nil {
		p.onRelayAvailable()
	}
}

// Relay membership changes infrequently. Readers load an immutable slice;
// regions do not each allocate a 64-lock sharded set.
type relaySet struct{ nodes atomic.Pointer[[]node.Hash] }

var emptyRelays = new(relaySet)

func (s *relaySet) snapshot() []node.Hash {
	if p := s.nodes.Load(); p != nil {
		return *p
	}
	return nil
}
func (s *relaySet) Size() int                 { return len(s.snapshot()) }
func (s *relaySet) Contains(h node.Hash) bool { return slices.Contains(s.snapshot(), h) }
func (s *relaySet) RandomPick(rng *rand.Rand) (node.Hash, bool) {
	nodes := s.snapshot()
	if len(nodes) == 0 {
		return node.Zero, false
	}
	return nodes[rng.IntN(len(nodes))], true
}
func (s *relaySet) Range(fn func(node.Hash) bool) {
	for _, h := range s.snapshot() {
		if !fn(h) {
			return
		}
	}
}
func (s *relaySet) add(h node.Hash) bool {
	for {
		old := s.nodes.Load()
		var nodes []node.Hash
		if old != nil {
			nodes = *old
		}
		if slices.Contains(nodes, h) {
			return false
		}
		next := append(slices.Clone(nodes), h)
		if s.nodes.CompareAndSwap(old, &next) {
			return len(nodes) == 0
		}
	}
}
func (s *relaySet) remove(h node.Hash) {
	for {
		old := s.nodes.Load()
		if old == nil {
			return
		}
		i := slices.Index(*old, h)
		if i < 0 {
			return
		}
		next := slices.Delete(slices.Clone(*old), i, i+1)
		if s.nodes.CompareAndSwap(old, &next) {
			return
		}
	}
}

// PublishOutbound and mode transitions use the same map operation. A builder
// cannot refill the old mode between pointer withdrawal and version publication.
func (p *GlobalNodePool) PublishOutbound(entry *node.NodeEntry, version uint64, previous, next *adapter.Outbound) bool {
	published := false
	p.nodes.Compute(entry.Hash, func(current *node.NodeEntry, ok bool) (*node.NodeEntry, xsync.ComputeOp) {
		if ok && current == entry && current.RoutingVersion() == version {
			published = current.Outbound.CompareAndSwap(previous, next)
		}
		return current, xsync.CancelOp
	})
	return published
}
