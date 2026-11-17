//go:build with_wireguard && with_gvisor

package outbound

import (
	"context"
	"errors"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/topology"
	"testing"
	"time"
)

func TestWireguardMissingRelayReturnsErrorWithoutBackgroundPanic(t *testing.T) {
	subs := topology.NewSubscriptionManager()
	sub := subscription.NewSubscription("s", "s", "", true, false)
	sub.SetChainConfig(true, false)
	subs.Register(sub)
	pool := topology.NewGlobalNodePool(topology.PoolConfig{SubLookup: subs.Lookup, MaxConsecutiveFailures: func() int { return 1 }})
	builder := newTestSingboxBuilder(t)
	defer builder.Close()
	mgr := NewOutboundManager(pool, builder)
	defer mgr.Close()
	mgr.SetRelayPicker(routing.NewRelaySelector(pool, nil, nil).Pick)
	raw := []byte(`{"type":"wireguard","server":"127.0.0.1","server_port":2480,"local_address":["172.16.0.2/32"],"private_key":"eCtXsJZ27+4PbhDkHnB923tkUn2Gj59wZw5wFA75MnU=","peer_public_key":"Cr8hWlKvtDt7nrvf+f0brNQQzabAqrjfBvas9pmowjo="}`)
	hash := node.HashFromRawOptions(raw)
	sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{})
	pool.AddNodeFromSub(hash, raw, sub.ID)
	mgr.EnsureNodeOutbound(hash)
	entry, _ := pool.GetEntry(hash)
	if !entry.HasOutbound() {
		t.Fatal(entry.GetLastError())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err := mgr.Fetch(ctx, hash, "http://203.0.113.1:80")
	if !errors.Is(err, routing.ErrNoAvailableRelay) {
		t.Fatalf("WireGuard async transport lost the local NoRelay error: %v", err)
	}
}
