//go:build !race

// This integration test exercises sing-box v1.12.21's real HTTP/2 transport.
// Its HTTP2Conn.Setup/Read has an upstream race; the race build covers Resin's
// deferred-error handling with a synchronized connection fixture instead.
package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/topology"
)

func TestDeferredNoRelayDoesNotTripLandingCircuit(t *testing.T) {
	subs := topology.NewSubscriptionManager()
	sub := subscription.NewSubscription("s", "s", "", true, false)
	sub.SetChainConfig(true, false)
	subs.Register(sub)
	pool := topology.NewGlobalNodePool(topology.PoolConfig{SubLookup: subs.Lookup, MaxLatencyTableEntries: 8, MaxConsecutiveFailures: func() int { return 1 }})
	raw := []byte(`{"type":"vless","server":"127.0.0.1","server_port":23456,"uuid":"00000000-0000-0000-0000-000000000001","tls":{"enabled":true,"insecure":true},"transport":{"type":"http"}}`)
	hash := node.HashFromRawOptions(raw)
	sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{})
	pool.AddNodeFromSub(hash, raw, sub.ID)
	builder, err := outbound.NewSingboxBuilderWithConfig(outbound.SingboxBuilderConfig{DNSUpstreams: []string{"local"}})
	if err != nil {
		t.Fatal(err)
	}
	defer builder.Close()
	mgr := outbound.NewOutboundManager(pool, builder)
	defer mgr.Close()
	mgr.SetRelayPicker(func(node.Hash, string) (node.Hash, error) { return node.Zero, routing.ErrNoAvailableRelay })
	mgr.EnsureNodeOutbound(hash)
	entry, _ := pool.GetEntry(hash)
	if !entry.HasOutbound() {
		t.Fatal(entry.GetLastError())
	}
	ip := netip.MustParseAddr("203.0.113.1")
	pool.UpdateNodeEgressIP(hash, &ip, nil)
	latency := time.Millisecond
	pool.RecordLatency(hash, "example.com", &latency)
	pool.RecordResult(hash, true)
	pool.RegisterPlatform(platform.NewPlatform("p", "p", nil, nil))
	pool.RebuildAllPlatforms()
	router := routing.NewRouter(routing.RouterConfig{Pool: pool, Authorities: func() []string { return nil }, P2CWindow: func() time.Duration { return time.Minute }})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	prepared := prepareConnectTunnel(ctx, tunnelDeps{router: router, pool: pool, health: pool}, "p", "", "example.com:443")
	if prepared.session == nil {
		t.Fatalf("expected deferred connection: %+v", prepared)
	}
	client, peer := net.Pipe()
	defer peer.Close()
	go io.Copy(io.Discard, peer)
	result := pumpPreparedTunnelReader(client, strings.NewReader("hello"), prepared.session, tunnelPumpOptions{requireBidirectionalTraffic: true})
	if !errors.Is(result.upstreamErr, routing.ErrNoAvailableRelay) || result.proxyErr != ErrNoAvailableRelay {
		t.Fatalf("lost NoRelay classification: %+v", result)
	}
	// Verify that this known local error does not generate async health feedback.
	recorder := &feedbackCapture{events: make(chan node.Feedback, 4)}
	route := routing.RouteResult{NodeHash: hash, NodeVersion: entry.RoutingVersion()}
	recordPassiveFailureAsync(recorder, route, result.upstreamErr, prepared.relay)
	select {
	case f := <-recorder.events:
		t.Fatalf("NoRelay produced health feedback: %+v", f)
	case <-time.After(20 * time.Millisecond):
	}
	prepared.session.recordResult(result.netOK, result.upstreamErr)
	if entry.FailureCount.Load() != 0 || entry.IsCircuitOpen() {
		t.Fatal("NoRelay tripped the landing circuit")
	}
}
