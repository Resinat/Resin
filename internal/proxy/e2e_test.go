package proxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/outbound"
	"github.com/resin-proxy/resin/internal/platform"
	"github.com/resin-proxy/resin/internal/routing"
	"github.com/resin-proxy/resin/internal/subscription"
	"github.com/resin-proxy/resin/internal/testutil"
	"github.com/resin-proxy/resin/internal/topology"
)

type proxyE2EEnv struct {
	pool   *topology.GlobalNodePool
	router *routing.Router
}

func newProxyE2EEnv(t *testing.T) *proxyE2EEnv {
	t.Helper()

	subMgr := topology.NewSubscriptionManager()
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              func(_ netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})

	plat := platform.NewPlatform("plat-id", "plat", nil, nil)
	plat.StickyTTLNs = int64(time.Hour)
	plat.ReverseProxyMissAction = "RANDOM"
	pool.RegisterPlatform(plat)

	sub := subscription.NewSubscription("sub-1", "sub-1", "https://example.com", true, false)
	subMgr.Register(sub)

	raw := json.RawMessage(`{"type":"stub","server":"127.0.0.1","server_port":1}`)
	hash := node.HashFromRawOptions(raw)
	sub.ManagedNodes().Store(hash, []string{"tag"})
	pool.AddNodeFromSub(hash, raw, sub.ID)

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("node not found in pool")
	}

	obMgr := outbound.NewOutboundManager(pool, &testutil.StubOutboundBuilder{})
	obMgr.EnsureNodeOutbound(hash)
	if !entry.HasOutbound() {
		t.Fatal("outbound should be initialized")
	}

	entry.SetEgressIP(netip.MustParseAddr("203.0.113.10"))
	if entry.LatencyTable == nil {
		t.Fatal("latency table should be initialized")
	}
	entry.LatencyTable.Update("example.com", 20*time.Millisecond, 10*time.Minute)

	pool.NotifyNodeDirty(hash)
	if !plat.View().Contains(hash) {
		t.Fatal("node should be in platform routable view")
	}

	router := routing.NewRouter(routing.RouterConfig{
		Pool:        pool,
		Authorities: func() []string { return []string{"example.com"} },
		P2CWindow:   func() time.Duration { return 10 * time.Minute },
	})

	return &proxyE2EEnv{
		pool:   pool,
		router: router,
	}
}

func TestForwardProxy_E2EHTTPSuccess(t *testing.T) {
	env := newProxyE2EEnv(t)
	emitter := newMockEventEmitter()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Proxy-Authorization"); got != "" {
			t.Fatalf("Proxy-Authorization leaked to upstream: %q", got)
		}
		if got := r.URL.Path; got != "/v1/ping" {
			t.Fatalf("unexpected path: %q", got)
		}
		if got := r.URL.RawQuery; got != "q=1" {
			t.Fatalf("unexpected query: %q", got)
		}
		w.Header().Set("X-Upstream", "ok")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("forward-e2e"))
	}))
	defer upstream.Close()

	fp := NewForwardProxy(ForwardProxyConfig{
		ProxyToken: "tok",
		Router:     env.router,
		Pool:       env.pool,
		Health:     &mockHealthRecorder{},
		Events:     emitter,
	})

	req := httptest.NewRequest(http.MethodGet, upstream.URL+"/v1/ping?q=1", nil)
	req.Header.Set("Proxy-Authorization", basicAuth("tok", "plat"))
	req.Header.Set("X-Test", "1")
	w := httptest.NewRecorder()

	fp.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want %d (body=%q, resinErr=%q)",
			w.Code, http.StatusCreated, w.Body.String(), w.Header().Get("X-Resin-Error"))
	}
	if got := w.Header().Get("X-Upstream"); got != "ok" {
		t.Fatalf("X-Upstream: got %q, want %q", got, "ok")
	}
	if got := w.Body.String(); got != "forward-e2e" {
		t.Fatalf("body: got %q, want %q", got, "forward-e2e")
	}

	select {
	case logEv := <-emitter.logCh:
		if logEv.EgressBytes <= 0 {
			t.Fatalf("EgressBytes: got %d, want > 0", logEv.EgressBytes)
		}
		if logEv.IngressBytes < int64(len("forward-e2e")) {
			t.Fatalf("IngressBytes: got %d, want >= %d", logEv.IngressBytes, len("forward-e2e"))
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected forward log event")
	}
}

func TestReverseProxy_E2ESuccess(t *testing.T) {
	env := newProxyE2EEnv(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/api/v1/items" {
			t.Fatalf("unexpected path: %q", got)
		}
		if got := r.URL.RawQuery; got != "k=v" {
			t.Fatalf("unexpected query: %q", got)
		}
		if got := r.Header.Get("X-Forwarded-Host"); got != "" {
			t.Fatalf("X-Forwarded-Host should be stripped, got %q", got)
		}
		if got := r.Header.Get("X-Real-IP"); got != "" {
			t.Fatalf("X-Real-IP should be stripped, got %q", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("reverse-e2e"))
	}))
	defer upstream.Close()

	host := strings.TrimPrefix(upstream.URL, "http://")
	path := fmt.Sprintf("/tok/plat/http/%s/api/v1/items?k=v", host)

	rp := NewReverseProxy(ReverseProxyConfig{
		ProxyToken:     "tok",
		Router:         env.router,
		Pool:           env.pool,
		PlatformLookup: env.pool,
		Health:         &mockHealthRecorder{},
		Events:         NoOpEventEmitter{},
	})

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Forwarded-Host", "should-strip")
	req.Header.Set("X-Real-IP", "1.2.3.4")
	w := httptest.NewRecorder()

	rp.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d (body=%q, resinErr=%q)",
			w.Code, http.StatusOK, w.Body.String(), w.Header().Get("X-Resin-Error"))
	}
	if got := w.Body.String(); got != "reverse-e2e" {
		t.Fatalf("body: got %q, want %q", got, "reverse-e2e")
	}
}

func TestReverseProxy_E2ECapturesDetailPayloads(t *testing.T) {
	env := newProxyE2EEnv(t)
	emitter := newMockEventEmitter()

	upstreamBody := "reverse-body-payload"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/api/v1/items" {
			t.Fatalf("unexpected path: %q", got)
		}
		w.Header().Set("X-Upstream-Header", "yes")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	host := strings.TrimPrefix(upstream.URL, "http://")
	path := fmt.Sprintf("/tok/plat/http/%s/api/v1/items", host)
	reqBody := "request-body-data"

	rp := NewReverseProxy(ReverseProxyConfig{
		ProxyToken:     "tok",
		Router:         env.router,
		Pool:           env.pool,
		PlatformLookup: env.pool,
		Health:         &mockHealthRecorder{},
		Events: ConfigAwareEventEmitter{
			Base:                         emitter,
			RequestLogEnabled:            func() bool { return true },
			ReverseProxyLogDetailEnabled: func() bool { return true },
		},
	})

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-Header", "capture")
	w := httptest.NewRecorder()

	rp.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want %d (body=%q, resinErr=%q)",
			w.Code, http.StatusCreated, w.Body.String(), w.Header().Get("X-Resin-Error"))
	}

	select {
	case logEv := <-emitter.logCh:
		if len(logEv.ReqHeaders) == 0 || logEv.ReqHeadersLen == 0 {
			t.Fatalf("expected req headers capture, got len=%d payload=%d", logEv.ReqHeadersLen, len(logEv.ReqHeaders))
		}
		if string(logEv.ReqBody) != reqBody {
			t.Fatalf("ReqBody: got %q, want %q", string(logEv.ReqBody), reqBody)
		}
		if logEv.ReqBodyLen != len(reqBody) || logEv.ReqBodyTruncated {
			t.Fatalf("ReqBody meta: len=%d truncated=%v, want len=%d truncated=false",
				logEv.ReqBodyLen, logEv.ReqBodyTruncated, len(reqBody))
		}
		if len(logEv.RespHeaders) == 0 || logEv.RespHeadersLen == 0 {
			t.Fatalf("expected resp headers capture, got len=%d payload=%d", logEv.RespHeadersLen, len(logEv.RespHeaders))
		}
		if !strings.Contains(string(logEv.RespHeaders), "X-Upstream-Header: yes") {
			t.Fatalf("RespHeaders missing upstream header, payload=%q", string(logEv.RespHeaders))
		}
		if string(logEv.RespBody) != upstreamBody {
			t.Fatalf("RespBody: got %q, want %q", string(logEv.RespBody), upstreamBody)
		}
		if logEv.RespBodyLen != len(upstreamBody) || logEv.RespBodyTruncated {
			t.Fatalf("RespBody meta: len=%d truncated=%v, want len=%d truncated=false",
				logEv.RespBodyLen, logEv.RespBodyTruncated, len(upstreamBody))
		}
		if logEv.EgressBytes < int64(len(reqBody)) {
			t.Fatalf("EgressBytes: got %d, want >= %d", logEv.EgressBytes, len(reqBody))
		}
		if logEv.IngressBytes < int64(len(upstreamBody)) {
			t.Fatalf("IngressBytes: got %d, want >= %d", logEv.IngressBytes, len(upstreamBody))
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected reverse log event")
	}
}

func TestForwardProxy_CONNECTTunnelSemantics(t *testing.T) {
	env := newProxyE2EEnv(t)
	emitter := newMockEventEmitter()

	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer targetLn.Close()

	targetDone := make(chan struct{})
	go func() {
		defer close(targetDone)
		conn, err := targetLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn) // echo
	}()

	fp := NewForwardProxy(ForwardProxyConfig{
		ProxyToken: "tok",
		Router:     env.router,
		Pool:       env.pool,
		Health:     &mockHealthRecorder{},
		Events:     emitter,
	})
	proxySrv := httptest.NewServer(fp)
	defer proxySrv.Close()

	proxyAddr := strings.TrimPrefix(proxySrv.URL, "http://")
	clientConn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer clientConn.Close()

	targetAddr := targetLn.Addr().String()
	req := fmt.Sprintf(
		"CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n",
		targetAddr,
		targetAddr,
		basicAuth("tok", "plat"),
	)
	if _, err := clientConn.Write([]byte(req)); err != nil {
		t.Fatalf("write connect request: %v", err)
	}

	reader := bufio.NewReader(clientConn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	if !strings.Contains(statusLine, "200 Connection Established") {
		t.Fatalf("unexpected CONNECT status line: %q", statusLine)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read response headers: %v", err)
		}
		if line == "\r\n" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "x-resin-error:") {
			t.Fatalf("unexpected HTTP semantic error after CONNECT success: %q", line)
		}
	}

	const payload = "ping-through-tunnel"
	if _, err := clientConn.Write([]byte(payload)); err != nil {
		t.Fatalf("write tunneled payload: %v", err)
	}
	echo := make([]byte, len(payload))
	if _, err := io.ReadFull(reader, echo); err != nil {
		t.Fatalf("read tunneled echo: %v", err)
	}
	if got := string(echo); got != payload {
		t.Fatalf("echo payload: got %q, want %q", got, payload)
	}

	_ = clientConn.Close()
	<-targetDone

	select {
	case logEv := <-emitter.logCh:
		if logEv.EgressBytes != int64(len(payload)) {
			t.Fatalf("EgressBytes: got %d, want %d", logEv.EgressBytes, len(payload))
		}
		if logEv.IngressBytes != int64(len(payload)) {
			t.Fatalf("IngressBytes: got %d, want %d", logEv.IngressBytes, len(payload))
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected CONNECT log event")
	}
}
