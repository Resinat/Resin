package proxy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/netip"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/topology"
	M "github.com/sagernet/sing/common/metadata"
)

func TestChainRequestLogFollowsActualConnection(t *testing.T) {
	a, b, landing := newVersionCONNECTProxy(t), newVersionCONNECTProxy(t), newVersionCONNECTProxy(t)
	var dropReused, rejectTLS atomic.Bool
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dropReused.CompareAndSwap(true, false) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		io.WriteString(w, "ok")
	}))
	target.TLS = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		if rejectTLS.Load() {
			return nil, errors.New("reject retry TLS handshake")
		}
		return nil, nil
	}}
	target.StartTLS()
	defer target.Close()
	subs := topology.NewSubscriptionManager()
	landSub := subscription.NewSubscription("landing", "landing", "", true, false)
	landSub.SetChainConfig(true, false)
	subs.Register(landSub)
	relaySub := subscription.NewSubscription("relay", "relay", "", true, false)
	relaySub.SetChainConfig(false, true)
	subs.Register(relaySub)
	pool := topology.NewGlobalNodePool(topology.PoolConfig{SubLookup: subs.Lookup, MaxLatencyTableEntries: 8, MaxConsecutiveFailures: func() int { return 1 }})
	pool.RegisterPlatform(platform.NewPlatform("p", "p", []*regexp.Regexp{regexp.MustCompile("^landing/")}, nil))
	builder, err := outbound.NewSingboxBuilderWithConfig(outbound.SingboxBuilderConfig{DNSUpstreams: []string{"local"}})
	if err != nil {
		t.Fatal(err)
	}
	defer builder.Close()
	mgr := outbound.NewOutboundManager(pool, builder)
	defer mgr.Close()
	selector := routing.NewRelaySelector(pool, nil, nil)
	mgr.SetRelayPicker(selector.Pick)
	add := func(sub *subscription.Subscription, raw json.RawMessage, tag string) node.Hash {
		h := node.HashFromRawOptions(raw)
		sub.ManagedNodes().StoreNode(h, subscription.ManagedNode{Tags: []string{tag}})
		pool.AddNodeFromSub(h, raw, sub.ID)
		mgr.EnsureNodeOutbound(h)
		return h
	}
	health := func(h node.Hash, success bool) {
		e, _ := pool.GetEntry(h)
		ip, latency := netip.MustParseAddr("203.0.113.1"), time.Millisecond
		pool.ApplyFeedback(h, e.RoutingVersion(), node.Feedback{Result: &success, Egress: true, IP: &ip, Target: "example.com", Latency: &latency})
	}
	ha, hb := add(relaySub, a.raw(), "A"), add(relaySub, b.raw(), "B")
	h := add(landSub, landing.raw(), "exit")
	health(ha, true)
	health(h, true)
	router := routing.NewRouter(routing.RouterConfig{
		Pool: pool, NodeTagResolver: pool.ResolveNodeDisplayTag,
		Authorities: func() []string { return nil }, P2CWindow: func() time.Duration { return time.Minute },
	})
	events := newMockEventEmitter()
	transports := newOutboundTransportPool()
	defer transports.CloseAll()
	fp := NewForwardProxy(ForwardProxyConfig{Router: router, Pool: pool, Events: events, TransportPool: transports})
	transport := func() *http.Transport {
		routed, routeErr := resolveRoutedOutbound(router, pool, "p", "", target.Listener.Addr().String())
		if routeErr != nil {
			t.Fatal(routeErr)
		}
		tr := mustGetTransport(t, transports, h, routed.Outbound, nil)
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		return tr
	}
	tr := transport()
	request := func(code int) RequestLogEntry {
		r := httptest.NewRequest(http.MethodGet, target.URL, nil)
		r.Header.Set("Proxy-Authorization", basicAuth("p", ""))
		w := httptest.NewRecorder()
		fp.ServeHTTP(w, r)
		if w.Code != code {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		return <-events.logCh
	}
	check := func(entry RequestLogEntry, relay node.Hash, tag string) {
		t.Helper()
		if !entry.ChainProxy || entry.RelayNodeHash != relay.Hex() || entry.RelayNodeTag != tag || entry.NodeHash != h.Hex() {
			t.Fatalf("wrong chain log: %+v", entry)
		}
	}
	check(request(200), ha, "relay/A")
	health(ha, false)
	health(hb, true)
	// The healthy candidate changed, but this request uses the old TLS connection.
	check(request(200), ha, "relay/A")
	tr.CloseIdleConnections()
	check(request(200), hb, "relay/B")
	if a.hits.Load() != 1 || b.hits.Load() != 1 {
		t.Fatal("expected one connection through each relay")
	}
	// Reuse B, then retry through A. TLS fails before GotConn on the retry;
	// the previous B identity must not survive in this request's log.
	health(hb, false)
	health(ha, true)
	dropReused.Store(true)
	rejectTLS.Store(true)
	failedRetry := request(502)
	if !failedRetry.ChainProxy || failedRetry.RelayNodeHash != "" || failedRetry.RelayNodeTag != "" {
		t.Fatalf("failed retry retained previous relay: %+v", failedRetry)
	}
	if a.hits.Load() != 2 || b.hits.Load() != 1 {
		t.Fatalf("retry did not change relay: A=%d B=%d", a.hits.Load(), b.hits.Load())
	}
	rejectTLS.Store(false)
	health(ha, false)
	health(hb, true)
	// CONNECT and SOCKS5 share this tunnel preparation path.
	prepared := prepareConnectTunnel(context.Background(), tunnelDeps{router: router, pool: pool}, "p", "", target.Listener.Addr().String())
	if prepared.session == nil || !prepared.route.ChainProxy || prepared.relay.Hash != hb || prepared.relay.Tag != "relay/B" {
		t.Fatalf("tunnel lost relay: %+v", prepared)
	}
	prepared.session.upstreamConn.Close()
	tr.CloseIdleConnections()
	health(hb, false)
	missing := request(503)
	if !missing.ChainProxy || missing.RelayNodeHash != "" || missing.ResinError != "NO_AVAILABLE_RELAY" {
		t.Fatalf("missing relay treated as direct: %+v", missing)
	}
	landSub.SetChainConfig(false, false)
	pool.ReconcileNodeRole(h)
	mgr.EnsureNodeOutbound(h)
	health(h, true)
	transport()
	direct := request(200)
	if direct.ChainProxy || direct.RelayNodeHash != "" || direct.NodeHash != h.Hex() {
		t.Fatalf("ordinary outbound changed: %+v", direct)
	}
}

type logTestRelayConn struct {
	net.Conn
	info outbound.RelayInfo
}

func (c *logTestRelayConn) RelayInfo() outbound.RelayInfo { return c.info }

func TestChainLogIgnoresNestedTransportAndClearsUnknownRetry(t *testing.T) {
	events := newMockEventEmitter()
	l := newRequestLifecycleFromMetadata(events, "127.0.0.1", "GET", ProxyTypeForward, false)
	l.setRouteResult(routing.RouteResult{ChainProxy: true})
	a := outbound.RelayInfo{Hash: node.Hash{1}, Tag: "relay/A"}
	b := outbound.RelayInfo{Hash: node.Hash{2}, Tag: "relay/B"}
	// TLS and traffic accounting preserve the actual connection's identity.
	l.observeHTTPConn(tls.Client(newCountingConn(&logTestRelayConn{info: a}, newCountingConnTestSink()), &tls.Config{}))
	trace := newUpstreamRequestTrace()
	trace.onGetConn = l.clearRelayInfo
	trace.onGotConn = l.observeHTTPConn
	ctx := httptrace.WithClientTrace(context.Background(), trace.clientTrace())
	ob := &mockOutbound{dialFunc: func(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
		innerCtx := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GetConn: func(string) {}, GotConn: func(httptrace.GotConnInfo) {}})
		inner := httptrace.ContextClientTrace(innerCtx)
		inner.GetConn("node:443")
		inner.GotConn(httptrace.GotConnInfo{Conn: &logTestRelayConn{info: b}})
		return nil, errors.New("nested protocol failed")
	}}
	transport := newOutboundTransportPool().newReusableOutboundTransport(ob, nil)
	_, _ = transport.DialContext(ctx, "tcp", "example.com:443")
	l.finish()
	if got := <-events.logCh; got.RelayNodeHash != a.Hash.Hex() {
		t.Fatalf("nested callback overwrote relay: %+v", got)
	}
	// A retry on an opaque native session is explicitly unknown.
	l = newRequestLifecycleFromMetadata(events, "127.0.0.1", "GET", ProxyTypeForward, false)
	l.setRouteResult(routing.RouteResult{ChainProxy: true})
	l.observeHTTPConn(&logTestRelayConn{info: a})
	l.observeHTTPConn(nil)
	l.finish()
	if got := <-events.logCh; !got.ChainProxy || got.RelayNodeHash != "" || got.RelayNodeTag != "" {
		t.Fatalf("unknown session retained old relay: %+v", got)
	}
}
