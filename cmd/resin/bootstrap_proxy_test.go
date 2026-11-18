package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/config"
	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/netutil"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/probe"
	"github.com/Resinat/Resin/internal/proxy"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/state"
)

func TestRestoredNodesServeProxyAndAcceptProbeFeedback(t *testing.T) {
	for _, role := range []string{"ordinary", "chain", "relay"} {
		t.Run(role, func(t *testing.T) {
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") }))
			defer target.Close()
			landing, jump := bootstrapCONNECTOutbound(t), bootstrapCONNECTOutbound(t)
			engine, closer, err := state.PersistenceBootstrap(t.TempDir(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer closer.Close()
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			cfg := config.NewDefaultRuntimeConfig()
			env := newDefaultPlatformEnvConfig()
			env.MaxLatencyTableEntries = 16
			env.DefaultPlatformRegexFilters = []string{"^sub/"}
			seed := func(id string, raw json.RawMessage, chain, relay bool) node.Hash {
				must(engine.UpsertSubscription(model.Subscription{ID: id, Name: id, URL: "https://example.com/sub", Enabled: true, UpdateIntervalNs: int64(time.Minute), ChainProxy: chain, ChainRelay: relay}))
				hash := node.HashFromRawOptions(raw)
				must(engine.BulkUpsertNodesStatic([]model.NodeStatic{{Hash: hash.Hex(), RawOptions: raw, CreatedAtNs: time.Now().UnixNano()}}))
				must(engine.BulkUpsertSubscriptionNodes([]model.SubscriptionNode{{SubscriptionID: id, NodeHash: hash.Hex(), Tags: []string{"node"}}}))
				must(engine.BulkUpsertNodesDynamic([]model.NodeDynamic{{Hash: hash.Hex(), ChainProxy: chain, EgressIP: "203.0.113.1", EgressRegion: "us"}}))
				must(engine.BulkUpsertNodeLatency([]model.NodeLatency{{NodeHash: hash.Hex(), Domain: "gstatic.com", ChainProxy: chain, EwmaNs: int64(time.Millisecond), LastUpdatedNs: time.Now().UnixNano()}}))
				return hash
			}
			hash := seed("sub", landing, role == "chain", role == "relay")
			seed("jump", jump, false, true)
			subs, pool := newBootstrapTestRuntime(cfg)
			must(bootstrapTopology(engine, subs, pool, env))
			builder, err := outbound.NewSingboxBuilderWithConfig(outbound.SingboxBuilderConfig{DNSUpstreams: []string{"local"}})
			must(err)
			defer builder.Close()
			manager := outbound.NewOutboundManager(pool, builder)
			defer manager.Close()
			manager.SetRelayPicker(routing.NewRelaySelector(pool, func() []string { return cfg.LatencyAuthorities }, func() time.Duration { return time.Minute }).Pick)
			must(bootstrapNodes(engine, pool, subs, manager, env, cfg.LatencyAuthorities))
			pool.RebuildAllPlatforms()
			entry, _ := pool.GetEntry(hash)
			if entry.RoutingVersion() == 0 {
				t.Fatal("restored node has a zero version")
			}
			router := routing.NewRouter(routing.RouterConfig{Pool: pool, Authorities: func() []string { return cfg.LatencyAuthorities }, P2CWindow: func() time.Duration { return time.Minute }})
			route, err := router.RouteRequest(platform.DefaultPlatformName, "", target.Listener.Addr().String())
			must(err)
			if route.NodeVersion != entry.RoutingVersion() {
				t.Fatal("route lost restored node version")
			}
			transports := proxy.NewOutboundTransportPool(proxy.OutboundTransportConfig{})
			defer transports.CloseAll()
			handler := proxy.NewForwardProxy(proxy.ForwardProxyConfig{Pool: pool, Router: router, TransportPool: transports})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target.URL, nil))
			if response.Code != http.StatusOK || response.Body.String() != "ok" {
				t.Fatalf("restored proxy: status=%d body=%s", response.Code, response.Body.String())
			}
			probes := probe.NewProbeManager(probe.ProbeConfig{Pool: pool, Fetcher: func(node.Hash, string) (netutil.OutboundHTTPResult, error) {
				return netutil.OutboundHTTPResult{Body: []byte("ip=203.0.113.99\nloc=JP\n"), Latency: 9 * time.Millisecond}, nil
			}})
			defer probes.Stop()
			_, err = probes.ProbeEgressSync(hash)
			must(err)
			if entry.GetEgressIP().String() != "203.0.113.99" {
				t.Fatal("restored node rejected successful probe feedback")
			}
		})
	}
}

// Local CONNECT servers allow the restored native HTTP and chained HTTP
// outbounds to carry a real request without public DNS or network fixtures.
func bootstrapCONNECTOutbound(t *testing.T) json.RawMessage {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		upstream, err := (&net.Dialer{Timeout: time.Second}).DialContext(r.Context(), "tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer upstream.Close()
		downstream, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer downstream.Close()
		downstream.SetDeadline(time.Now().Add(5 * time.Second))
		upstream.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.WriteString(downstream, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		done := make(chan struct{})
		go func() { io.Copy(upstream, buffered); upstream.Close(); close(done) }()
		io.Copy(downstream, upstream)
		downstream.Close()
		<-done
	}))
	t.Cleanup(server.Close)
	host, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	number, _ := strconv.Atoi(port)
	raw, err := json.Marshal(map[string]any{"type": "http", "server": host, "server_port": number})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
