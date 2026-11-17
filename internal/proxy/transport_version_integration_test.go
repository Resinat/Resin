package proxy

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"regexp"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	resinoutbound "github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/topology"
)

type versionCONNECTProxy struct {
	server *httptest.Server
	hits   atomic.Int32
}

func newVersionCONNECTProxy(t *testing.T) *versionCONNECTProxy {
	p := new(versionCONNECTProxy)
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up, err := (&net.Dialer{Timeout: time.Second}).DialContext(r.Context(), "tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer up.Close()
		down, br, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer down.Close()
		down.SetDeadline(time.Now().Add(3 * time.Second))
		up.SetDeadline(time.Now().Add(3 * time.Second))
		p.hits.Add(1)
		io.WriteString(down, "HTTP/1.1 200 Connection Established\r\n\r\n")
		done := make(chan struct{})
		go func() { io.Copy(up, br); up.Close(); close(done) }()
		io.Copy(down, up)
		down.Close()
		<-done
	}))
	t.Cleanup(p.server.Close)
	return p
}
func (p *versionCONNECTProxy) raw() json.RawMessage {
	host, port, _ := net.SplitHostPort(p.server.Listener.Addr().String())
	number, _ := strconv.Atoi(port)
	raw, _ := json.Marshal(map[string]any{"type": "http", "server": host, "server_port": number})
	return raw
}

func TestTransportPoolKeepsRealOutboundMode(t *testing.T) {
	for _, initialChain := range []bool{false, true} {
		name := "ordinary-to-chain"
		if initialChain {
			name = "chain-to-ordinary"
		}
		t.Run(name, func(t *testing.T) {
			relay, landing := newVersionCONNECTProxy(t), newVersionCONNECTProxy(t)
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
			defer target.Close()
			subs := topology.NewSubscriptionManager()
			landSub := subscription.NewSubscription("landing", "landing", "", true, false)
			landSub.SetChainConfig(initialChain, false)
			subs.Register(landSub)
			relaySub := subscription.NewSubscription("relay", "relay", "", true, false)
			relaySub.SetChainConfig(false, true)
			subs.Register(relaySub)
			pool := topology.NewGlobalNodePool(topology.PoolConfig{SubLookup: subs.Lookup, MaxLatencyTableEntries: 8, MaxConsecutiveFailures: func() int { return 1 }})
			pool.RegisterPlatform(platform.NewPlatform("p", "p", []*regexp.Regexp{regexp.MustCompile("^landing/")}, nil))
			builder, err := resinoutbound.NewSingboxBuilderWithConfig(resinoutbound.SingboxBuilderConfig{DNSUpstreams: []string{"local"}})
			if err != nil {
				t.Fatal(err)
			}
			defer builder.Close()
			mgr := resinoutbound.NewOutboundManager(pool, builder)
			defer mgr.Close()
			selector := routing.NewRelaySelector(pool, nil, nil)
			mgr.SetRelayPicker(selector.Pick)
			add := func(sub *subscription.Subscription, raw json.RawMessage) node.Hash {
				h := node.HashFromRawOptions(raw)
				sub.ManagedNodes().StoreNode(h, subscription.ManagedNode{Tags: []string{"a"}})
				pool.AddNodeFromSub(h, raw, sub.ID)
				mgr.EnsureNodeOutbound(h)
				return h
			}
			healthy := func(h node.Hash) {
				e, _ := pool.GetEntry(h)
				ok := true
				ip := netip.MustParseAddr("203.0.113.1")
				latency := time.Millisecond
				pool.ApplyFeedback(h, e.RoutingVersion(), node.Feedback{Result: &ok, Egress: true, IP: &ip, Target: "example.com", Latency: &latency})
			}
			relayHash := add(relaySub, relay.raw())
			healthy(relayHash)
			h := add(landSub, landing.raw())
			healthy(h)
			router := routing.NewRouter(routing.RouterConfig{Pool: pool, Authorities: func() []string { return nil }, P2CWindow: func() time.Duration { return time.Minute }})
			a, routeErr := resolveRoutedOutbound(router, pool, "p", "", "example.com:80")
			if routeErr != nil {
				t.Fatal(routeErr)
			}
			transports := NewOutboundTransportPool(OutboundTransportConfig{})
			defer transports.CloseAll()
			// Pause A after route selection; B switches to the other mode.
			landSub.SetChainConfig(!initialChain, false)
			pool.ReconcileNodeRole(h)
			mgr.EnsureNodeOutbound(h)
			healthy(h)
			b, routeErr := resolveRoutedOutbound(router, pool, "p", "", "example.com:80")
			if routeErr != nil {
				t.Fatal(routeErr)
			}
			current := mustGetTransport(t, transports, h, b.Outbound, nil)
			// A resumes with the old route. Reject it before either native can dial.
			stale, staleErr := transports.Get(h, a.Outbound, nil)
			if stale != nil || staleErr != ErrNoAvailableNodes {
				t.Fatalf("old route silently switched mode: %v %v", stale, staleErr)
			}
			if relay.hits.Load() != 0 || landing.hits.Load() != 0 {
				t.Fatal("stale request reached a proxy")
			}
			if mustGetTransport(t, transports, h, b.Outbound, nil) != current {
				t.Fatal("stale request replaced current cache")
			}
			req, _ := http.NewRequest("GET", target.URL, nil)
			resp, roundErr := current.RoundTrip(req)
			if roundErr != nil {
				t.Fatal(roundErr)
			}
			data, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil || resp.StatusCode != http.StatusOK || string(data) != "ok" {
				t.Fatalf("current mode request: %d %q %v", resp.StatusCode, data, readErr)
			}
			expectedRelay := int32(1)
			if initialChain {
				expectedRelay = 0
			}
			if relay.hits.Load() != expectedRelay || landing.hits.Load() != 1 {
				t.Fatalf("wrong current mode path: relay=%d landing=%d", relay.hits.Load(), landing.hits.Load())
			}
		})
	}
}
