package proxy

import (
	"bufio"
	"context"
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

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/testutil"
	"github.com/Resinat/Resin/internal/topology"
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
	pool.RecordResult(hash, true)

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

func TestForwardProxy_E2EHTTPClientCanceledBeforeResponse(t *testing.T) {
	env := newProxyE2EEnv(t)
	emitter := newMockEventEmitter()
	health := &mockHealthRecorder{}

	fp := NewForwardProxy(ForwardProxyConfig{
		ProxyToken: "tok",
		Router:     env.router,
		Pool:       env.pool,
		Health:     health,
		Events:     emitter,
	})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/v1/cancel", nil)
	req.Header.Set("Proxy-Authorization", basicAuth("tok", "plat"))
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	fp.ServeHTTP(w, req)

	select {
	case logEv := <-emitter.logCh:
		if !logEv.NetOK {
			t.Fatal("client-canceled forward HTTP should log net_ok=true")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected forward log event")
	}

	time.Sleep(50 * time.Millisecond)
	if health.resultCalls.Load() != 0 {
		t.Fatalf("client-canceled forward HTTP should not record health result, got %d calls", health.resultCalls.Load())
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
	path := fmt.Sprintf("/tok/plat:acct/http/%s/api/v1/items?k=v", host)

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

func TestReverseProxy_E2EClientCanceledBeforeResponse(t *testing.T) {
	env := newProxyE2EEnv(t)
	emitter := newMockEventEmitter()
	health := &mockHealthRecorder{}

	rp := NewReverseProxy(ReverseProxyConfig{
		ProxyToken:     "tok",
		Router:         env.router,
		Pool:           env.pool,
		PlatformLookup: env.pool,
		Health:         health,
		Events:         emitter,
	})

	req := httptest.NewRequest(http.MethodGet, "/tok/plat:acct/http/example.com/v1/cancel", nil)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	rp.ServeHTTP(w, req)

	select {
	case logEv := <-emitter.logCh:
		if !logEv.NetOK {
			t.Fatal("client-canceled reverse request should log net_ok=true")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected reverse log event")
	}

	time.Sleep(50 * time.Millisecond)
	if health.resultCalls.Load() != 0 {
		t.Fatalf("client-canceled reverse request should not record health result, got %d calls", health.resultCalls.Load())
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
	path := fmt.Sprintf("/tok/plat:acct/http/%s/api/v1/items", host)
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

func TestReverseProxy_E2EWebSocketUpgrade_WithDetailCapture(t *testing.T) {
	env := newProxyE2EEnv(t)
	emitter := newMockEventEmitter()

	tunnelPayload := strings.Repeat("c", 200*1024)
	tunnelAck := strings.Repeat("s", 180*1024)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := strings.ToLower(r.Header.Get("Upgrade")); got != "websocket" {
			t.Fatalf("Upgrade header: got %q, want %q", got, "websocket")
		}
		if got := strings.ToLower(r.Header.Get("Connection")); !strings.Contains(got, "upgrade") {
			t.Fatalf("Connection header should include upgrade, got %q", got)
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("upstream does not support hijack")
		}
		conn, brw, err := hijacker.Hijack()
		if err != nil {
			t.Fatalf("upstream hijack: %v", err)
		}
		defer conn.Close()

		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
		_, _ = brw.WriteString("Connection: Upgrade\r\n")
		_, _ = brw.WriteString("Upgrade: websocket\r\n")
		_, _ = brw.WriteString("\r\n")
		if err := brw.Flush(); err != nil {
			t.Fatalf("upstream flush upgrade response: %v", err)
		}

		buf := make([]byte, len(tunnelPayload))
		if _, err := io.ReadFull(conn, buf); err != nil {
			t.Fatalf("upstream read tunneled payload: %v", err)
		}
		if got := string(buf); got != tunnelPayload {
			t.Fatalf("upstream payload: got %q, want %q", got, tunnelPayload)
		}
		if _, err := conn.Write([]byte(tunnelAck)); err != nil {
			t.Fatalf("upstream write tunneled ack: %v", err)
		}
	}))
	defer upstream.Close()

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
	reverseSrv := httptest.NewServer(rp)
	defer reverseSrv.Close()

	reverseAddr := strings.TrimPrefix(reverseSrv.URL, "http://")
	clientConn, err := net.Dial("tcp", reverseAddr)
	if err != nil {
		t.Fatalf("dial reverse proxy: %v", err)
	}
	defer clientConn.Close()

	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")
	req := fmt.Sprintf(
		"GET /tok/plat:acct/http/%s/ws HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n",
		upstreamHost,
		reverseAddr,
	)
	if _, err := clientConn.Write([]byte(req)); err != nil {
		t.Fatalf("write upgrade request: %v", err)
	}

	reader := bufio.NewReader(clientConn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read upgrade status line: %v", err)
	}
	if !strings.Contains(statusLine, "101 Switching Protocols") {
		t.Fatalf("unexpected status line: %q", statusLine)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read upgrade headers: %v", err)
		}
		if strings.HasPrefix(strings.ToLower(line), "x-resin-error:") {
			t.Fatalf("unexpected resin error header on upgrade success: %q", line)
		}
		if line == "\r\n" {
			break
		}
	}

	if _, err := clientConn.Write([]byte(tunnelPayload)); err != nil {
		t.Fatalf("write tunneled payload: %v", err)
	}
	ack := make([]byte, len(tunnelAck))
	if _, err := io.ReadFull(reader, ack); err != nil {
		t.Fatalf("read tunneled ack: %v", err)
	}
	if got := string(ack); got != tunnelAck {
		t.Fatalf("tunneled ack: got %q, want %q", got, tunnelAck)
	}

	_ = clientConn.Close()

	select {
	case logEv := <-emitter.logCh:
		if logEv.HTTPStatus != http.StatusSwitchingProtocols {
			t.Fatalf("HTTPStatus: got %d, want %d", logEv.HTTPStatus, http.StatusSwitchingProtocols)
		}
		if !logEv.NetOK {
			t.Fatal("NetOK: got false, want true")
		}
		if len(logEv.RespHeaders) == 0 || logEv.RespHeadersLen == 0 {
			t.Fatalf("expected resp headers capture, got len=%d payload=%d", logEv.RespHeadersLen, len(logEv.RespHeaders))
		}
		if !strings.Contains(strings.ToLower(string(logEv.RespHeaders)), "upgrade: websocket") {
			t.Fatalf("RespHeaders missing upgrade header, payload=%q", string(logEv.RespHeaders))
		}
		if len(logEv.RespBody) != 0 || logEv.RespBodyLen != 0 || logEv.RespBodyTruncated {
			t.Fatalf(
				"expected empty resp body capture for upgrade, got len=%d payload=%d truncated=%v",
				logEv.RespBodyLen,
				len(logEv.RespBody),
				logEv.RespBodyTruncated,
			)
		}
		if logEv.EgressBytes < int64(len(tunnelPayload)) {
			t.Fatalf("EgressBytes: got %d, want >= %d", logEv.EgressBytes, len(tunnelPayload))
		}
		if logEv.IngressBytes < int64(len(tunnelAck)) {
			t.Fatalf("IngressBytes: got %d, want >= %d", logEv.IngressBytes, len(tunnelAck))
		}
	case <-time.After(1 * time.Second):
		t.Fatal("expected reverse log event for websocket upgrade")
	}
}

func TestForwardProxy_CONNECTTunnelSemantics(t *testing.T) {
	env := newProxyE2EEnv(t)
	emitter := newMockEventEmitter()
	health := &mockHealthRecorder{}

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
		Health:     health,
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
		if !logEv.NetOK {
			t.Fatal("CONNECT log net_ok: got false, want true")
		}
		if logEv.EgressBytes != int64(len(payload)) {
			t.Fatalf("EgressBytes: got %d, want %d", logEv.EgressBytes, len(payload))
		}
		if logEv.IngressBytes != int64(len(payload)) {
			t.Fatalf("IngressBytes: got %d, want %d", logEv.IngressBytes, len(payload))
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected CONNECT log event")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if health.resultCalls.Load() > 0 {
			if health.lastSuccess.Load() != 1 {
				t.Fatalf("RecordResult lastSuccess: got %d, want 1", health.lastSuccess.Load())
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected RecordResult call for CONNECT success")
}

func TestForwardProxy_CONNECTClientCanceledBeforeResponse(t *testing.T) {
	env := newProxyE2EEnv(t)
	emitter := newMockEventEmitter()
	health := &mockHealthRecorder{}

	fp := NewForwardProxy(ForwardProxyConfig{
		ProxyToken: "tok",
		Router:     env.router,
		Pool:       env.pool,
		Health:     health,
		Events:     emitter,
	})

	req := httptest.NewRequest(http.MethodConnect, "http://example.com:443", nil)
	req.Host = "example.com:443"
	req.Header.Set("Proxy-Authorization", basicAuth("tok", "plat"))
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	fp.ServeHTTP(w, req)

	select {
	case logEv := <-emitter.logCh:
		if !logEv.NetOK {
			t.Fatal("client-canceled CONNECT should log net_ok=true")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected CONNECT log event")
	}

	time.Sleep(50 * time.Millisecond)
	if health.resultCalls.Load() != 0 {
		t.Fatalf("client-canceled CONNECT should not record health result, got %d calls", health.resultCalls.Load())
	}
}

func TestForwardProxy_CONNECTZeroTrafficMarkedFailed(t *testing.T) {
	env := newProxyE2EEnv(t)
	emitter := newMockEventEmitter()
	health := &mockHealthRecorder{}

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
		_, _ = io.Copy(io.Discard, conn)
	}()

	fp := NewForwardProxy(ForwardProxyConfig{
		ProxyToken: "tok",
		Router:     env.router,
		Pool:       env.pool,
		Health:     health,
		Events:     emitter,
	})
	proxySrv := httptest.NewServer(fp)
	defer proxySrv.Close()

	proxyAddr := strings.TrimPrefix(proxySrv.URL, "http://")
	clientConn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}

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
	}
	_ = clientConn.Close()
	<-targetDone

	select {
	case logEv := <-emitter.logCh:
		if logEv.HTTPStatus != http.StatusOK {
			t.Fatalf("HTTPStatus: got %d, want %d", logEv.HTTPStatus, http.StatusOK)
		}
		if logEv.NetOK {
			t.Fatal("CONNECT zero-traffic log net_ok: got true, want false")
		}
		if logEv.ResinError != "UPSTREAM_REQUEST_FAILED" {
			t.Fatalf("CONNECT zero-traffic resin_error: got %q, want %q", logEv.ResinError, "UPSTREAM_REQUEST_FAILED")
		}
		if logEv.UpstreamStage != "connect_zero_traffic" {
			t.Fatalf("CONNECT zero-traffic upstream_stage: got %q, want %q", logEv.UpstreamStage, "connect_zero_traffic")
		}
		if logEv.EgressBytes != 0 || logEv.IngressBytes != 0 {
			t.Fatalf("CONNECT zero-traffic bytes: ingress=%d egress=%d, want both 0", logEv.IngressBytes, logEv.EgressBytes)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected CONNECT log event")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if health.resultCalls.Load() > 0 {
			if health.lastSuccess.Load() != 0 {
				t.Fatalf("RecordResult lastSuccess: got %d, want 0", health.lastSuccess.Load())
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected RecordResult call for CONNECT zero-traffic failure")
}

func TestForwardProxy_CONNECTHalfTrafficNotMarkedZeroTraffic(t *testing.T) {
	env := newProxyE2EEnv(t)
	emitter := newMockEventEmitter()
	health := &mockHealthRecorder{}

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
		_, _ = conn.Write([]byte("server-push"))
	}()

	fp := NewForwardProxy(ForwardProxyConfig{
		ProxyToken: "tok",
		Router:     env.router,
		Pool:       env.pool,
		Health:     health,
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
	}

	payload := make([]byte, len("server-push"))
	if _, err := io.ReadFull(reader, payload); err != nil {
		t.Fatalf("read tunnel payload: %v", err)
	}
	if string(payload) != "server-push" {
		t.Fatalf("payload: got %q, want %q", string(payload), "server-push")
	}
	_ = clientConn.Close()
	<-targetDone

	select {
	case logEv := <-emitter.logCh:
		if logEv.NetOK {
			t.Fatal("CONNECT half-traffic log net_ok: got true, want false")
		}
		if logEv.UpstreamStage != "connect_no_egress_traffic" {
			t.Fatalf("CONNECT half-traffic upstream_stage: got %q, want %q", logEv.UpstreamStage, "connect_no_egress_traffic")
		}
		if logEv.UpstreamStage == "connect_zero_traffic" {
			t.Fatal("CONNECT half-traffic must not be marked as connect_zero_traffic")
		}
		if logEv.IngressBytes == 0 || logEv.EgressBytes != 0 {
			t.Fatalf("CONNECT half-traffic bytes: ingress=%d egress=%d, want ingress>0 and egress=0", logEv.IngressBytes, logEv.EgressBytes)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected CONNECT log event")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if health.resultCalls.Load() > 0 {
			if health.lastSuccess.Load() != 0 {
				t.Fatalf("RecordResult lastSuccess: got %d, want 0", health.lastSuccess.Load())
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected RecordResult call for CONNECT half-traffic failure")
}
