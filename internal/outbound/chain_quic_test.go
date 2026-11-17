//go:build with_quic

package outbound

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/topology"
)

func TestHysteriaChainRequiresUDPRelayForTCPRequest(t *testing.T) {
	subs := topology.NewSubscriptionManager()
	landing := subscription.NewSubscription("landing", "landing", "", true, false)
	landing.SetChainConfig(true, false)
	subs.Register(landing)
	relay := subscription.NewSubscription("relay", "relay", "", true, false)
	relay.SetChainConfig(false, true)
	subs.Register(relay)
	pool := topology.NewGlobalNodePool(topology.PoolConfig{SubLookup: subs.Lookup, MaxConsecutiveFailures: func() int { return 1 }})
	builder := newTestSingboxBuilder(t)
	defer builder.Close()
	mgr := NewOutboundManager(pool, builder)
	defer mgr.Close()
	selector := routing.NewRelaySelector(pool, nil, nil)
	mgr.SetRelayPicker(selector.Pick)
	pool.SetOnNodeAdded(mgr.EnsureNodeOutbound)
	add := func(sub *subscription.Subscription, raw string) node.Hash {
		h := node.HashFromRawOptions([]byte(raw))
		sub.ManagedNodes().StoreNode(h, subscription.ManagedNode{})
		pool.AddNodeFromSub(h, []byte(raw), sub.ID)
		return h
	}
	r := add(relay, `{"type":"http","server":"127.0.0.1","server_port":12345}`)
	pool.RecordResult(r, true)
	h := add(landing, `{"type":"hysteria2","server":"127.0.0.1","server_port":23456,"password":"test","tls":{"enabled":true,"insecure":true}}`)
	e, _ := pool.GetEntry(h)
	if !e.HasOutbound() {
		t.Fatalf("build: %s", e.GetLastError())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err := mgr.Fetch(ctx, h, "http://127.0.0.1:34567")
	if !errors.Is(err, routing.ErrNoAvailableRelay) {
		t.Fatalf("TCP-only relay was not filtered from QUIC's UDP dial: %v", err)
	}
}
