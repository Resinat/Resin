package proxy

import (
	"net/netip"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/sagernet/sing-box/adapter"
)

type routeOverrideTestPool struct {
	entries map[node.Hash]*node.NodeEntry
}

func (p *routeOverrideTestPool) GetEntry(hash node.Hash) (*node.NodeEntry, bool) {
	entry, ok := p.entries[hash]
	return entry, ok
}

func (p *routeOverrideTestPool) GetPlatform(id string) (*platform.Platform, bool) {
	return nil, false
}

func (p *routeOverrideTestPool) GetPlatformByName(name string) (*platform.Platform, bool) {
	return nil, false
}

func (p *routeOverrideTestPool) RangePlatforms(fn func(*platform.Platform) bool) {}

func (p *routeOverrideTestPool) RangeNodes(fn func(node.Hash, *node.NodeEntry) bool) {
	for h, entry := range p.entries {
		if !fn(h, entry) {
			return
		}
	}
}

func newRouteOverrideEntry(hash node.Hash) *node.NodeEntry {
	entry := node.NewNodeEntry(hash, nil, time.Now(), 0)
	ob := &mockOutbound{}
	var wrapped adapter.Outbound = ob
	entry.Outbound.Store(&wrapped)
	entry.SetEgressIP(netip.MustParseAddr("203.0.113.20"))
	return entry
}

func TestResolveRoutedOutbound_NodeHashOverrideBypassesRouter(t *testing.T) {
	hash := node.Hash{1, 2, 3}
	pool := &routeOverrideTestPool{
		entries: map[node.Hash]*node.NodeEntry{
			hash: newRouteOverrideEntry(hash),
		},
	}

	routed, routeErr := resolveRoutedOutbound(nil, pool, "missing-platform", "acct", "example.com", routeOverride{NodeHash: hash.Hex()})
	if routeErr != nil {
		t.Fatalf("unexpected route error: %v", routeErr)
	}
	if routed.Route.NodeHash != hash {
		t.Fatalf("node hash: got %s, want %s", routed.Route.NodeHash.Hex(), hash.Hex())
	}
	if routed.Route.PlatformName != "" || routed.Route.LeaseCreated {
		t.Fatalf("override should not create platform lease context: %+v", routed.Route)
	}
}

func TestResolveRoutedOutbound_NodeHashOverrideRejectsUnavailableNode(t *testing.T) {
	hash := node.Hash{4, 5, 6}
	pool := &routeOverrideTestPool{
		entries: map[node.Hash]*node.NodeEntry{
			hash: node.NewNodeEntry(hash, nil, time.Now(), 0),
		},
	}
	router := routing.NewRouter(routing.RouterConfig{Pool: pool})

	_, routeErr := resolveRoutedOutbound(router, pool, "", "", "example.com", routeOverride{NodeHash: hash.Hex()})
	if routeErr != ErrNoAvailableNodes {
		t.Fatalf("route error: got %v, want %v", routeErr, ErrNoAvailableNodes)
	}
}
