package routing_test

import (
	"errors"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/testutil"
	"github.com/Resinat/Resin/internal/topology"
	"github.com/sagernet/sing-box/adapter"
)

type tcpRelayOutbound struct{ adapter.Outbound }

func (*tcpRelayOutbound) Network() []string { return []string{"tcp"} }

func TestRelaySelectionRegionLatencyAndCompatibility(t *testing.T) {
	subs := topology.NewSubscriptionManager()
	sub := subscription.NewSubscription("relay", "relay", "", true, false)
	sub.SetChainConfig(false, true)
	subs.Register(sub)
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup: subs.Lookup, MaxLatencyTableEntries: 8, MaxConsecutiveFailures: func() int { return 1 },
	})
	add := func(name, region string, latency time.Duration) node.Hash {
		raw := []byte(`{"type":"http","server":"` + name + `"}`)
		hash := node.HashFromRawOptions(raw)
		sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{})
		pool.AddNodeFromSub(hash, raw, sub.ID)
		e, _ := pool.GetEntry(hash)
		var ob adapter.Outbound = &tcpRelayOutbound{testutil.NewNoopOutbound()}
		e.Outbound.Store(&ob)
		pool.RecordResult(hash, true)
		pool.UpdateNodeEgressIP(hash, nil, &region)
		pool.RecordLatency(hash, "speed.example", &latency)
		return hash
	}
	landing := add("landing", "us", time.Second)
	// The landing is not itself an eligible relay.
	e, _ := pool.GetEntry(landing)
	e.UpdateRoutingRole(node.RoleChain)
	pool.NotifyNodeDirty(landing)
	slow := add("us-slow", "us", 100*time.Millisecond)
	add("jp-fast", "jp", time.Millisecond)
	selector := routing.NewRelaySelector(pool, func() []string { return []string{"speed.example"} }, func() time.Duration { return time.Hour })
	for i := 0; i < 20; i++ {
		h, err := selector.Pick(landing, "tcp")
		if err != nil || h != slow {
			t.Fatalf("same region must precede foreign latency: %s %v", h, err)
		}
	}
	fast := add("us-fast", "us", 2*time.Millisecond)
	fastPicks := 0
	for i := 0; i < 200; i++ {
		h, err := selector.Pick(landing, "tcp")
		if err != nil {
			t.Fatal(err)
		}
		if h == fast {
			fastPicks++
		}
		if h != fast && h != slow {
			t.Fatal("foreign relay selected while local candidates exist")
		}
	}
	if fastPicks < 185 {
		t.Fatalf("P2C did not prefer lower latency: %d/200", fastPicks)
	}
	if _, err := selector.Pick(landing, "udp"); !errors.Is(err, routing.ErrNoAvailableRelay) {
		t.Fatalf("TCP-only candidates accepted for UDP: %v", err)
	}
	pool.RecordResult(slow, false)
	pool.RecordResult(fast, false)
	h, err := selector.Pick(landing, "tcp")
	if err != nil || pool.NodeRegion(h) != "jp" {
		t.Fatalf("global fallback: %s %v", h, err)
	}
}
