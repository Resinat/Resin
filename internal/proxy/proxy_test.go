package proxy

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

// --- Mock infrastructure ---

type mockPool struct {
	entry *node.NodeEntry
}

func (m *mockPool) GetEntry(hash node.Hash) (*node.NodeEntry, bool) {
	if m.entry == nil {
		return nil, false
	}
	return m.entry, true
}

func (m *mockPool) RangeNodes(fn func(node.Hash, *node.NodeEntry) bool) {}

func (m *mockPool) GetPlatform(id string) (*platform.Platform, bool) {
	return nil, false
}

func (m *mockPool) GetPlatformByName(name string) (*platform.Platform, bool) {
	return nil, false
}

type mockHealthRecorder struct {
	resultCalls  atomic.Int32
	latencyCalls atomic.Int32
	lastSuccess  atomic.Int32 // 1=true, 0=false
}

func (m *mockHealthRecorder) RecordResult(hash node.Hash, success bool) {
	m.resultCalls.Add(1)
	if success {
		m.lastSuccess.Store(1)
	} else {
		m.lastSuccess.Store(0)
	}
}

func (m *mockHealthRecorder) RecordLatency(hash node.Hash, rawTarget string, latency *time.Duration) {
	m.latencyCalls.Add(1)
}

type mockEventEmitter struct {
	finishedCh chan RequestFinishedEvent
	logCh      chan RequestLogEntry
}

func newMockEventEmitter() *mockEventEmitter {
	return &mockEventEmitter{
		finishedCh: make(chan RequestFinishedEvent, 8),
		logCh:      make(chan RequestLogEntry, 8),
	}
}

func (m *mockEventEmitter) EmitRequestFinished(e RequestFinishedEvent) {
	m.finishedCh <- e
}

func (m *mockEventEmitter) EmitRequestLog(e RequestLogEntry) {
	m.logCh <- e
}

// mockOutbound implements adapter.Outbound to provide DialContext.
type mockOutbound struct {
	adapter.Outbound
	dialFunc func(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error)
}

func (m *mockOutbound) DialContext(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
	if m.dialFunc != nil {
		return m.dialFunc(ctx, network, dest)
	}
	return nil, &net.OpError{Op: "dial", Net: network, Err: &net.DNSError{Err: "mock: no dial func"}}
}

func (m *mockOutbound) Tag() string  { return "mock" }
func (m *mockOutbound) Type() string { return "mock" }

// --- Helper ---

func basicAuth(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// --- Tests ---

func TestForwardProxy_AuthRequired(t *testing.T) {
	fp := &ForwardProxy{token: "tok", events: NoOpEventEmitter{}}
	req := httptest.NewRequest("GET", "http://example.com/", nil)
	w := httptest.NewRecorder()
	fp.ServeHTTP(w, req)

	if w.Code != http.StatusProxyAuthRequired {
		t.Fatalf("expected 407, got %d", w.Code)
	}
	if w.Header().Get("X-Resin-Error") != "AUTH_REQUIRED" {
		t.Fatalf("expected AUTH_REQUIRED, got %q", w.Header().Get("X-Resin-Error"))
	}
	if w.Header().Get("Proxy-Authenticate") != `Basic realm="Resin"` {
		t.Fatalf("expected Proxy-Authenticate header")
	}
}

func TestForwardProxy_AuthRequired_EmitsNoEvents(t *testing.T) {
	emitter := newMockEventEmitter()
	fp := &ForwardProxy{token: "tok", events: emitter}
	req := httptest.NewRequest("GET", "http://example.com/", nil)
	w := httptest.NewRecorder()

	fp.ServeHTTP(w, req)

	if w.Code != http.StatusProxyAuthRequired {
		t.Fatalf("expected 407, got %d", w.Code)
	}

	select {
	case ev := <-emitter.finishedCh:
		t.Fatalf("unexpected forward finished event for auth error: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}

	select {
	case logEv := <-emitter.logCh:
		t.Fatalf("unexpected forward log event for auth error: %+v", logEv)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestForwardProxy_AuthFailed(t *testing.T) {
	fp := &ForwardProxy{token: "correct-token", events: NoOpEventEmitter{}}
	req := httptest.NewRequest("GET", "http://example.com/", nil)
	req.Header.Set("Proxy-Authorization", basicAuth("wrong-token", "plat:acct"))
	w := httptest.NewRecorder()
	fp.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
	if w.Header().Get("X-Resin-Error") != "AUTH_FAILED" {
		t.Fatalf("expected AUTH_FAILED, got %q", w.Header().Get("X-Resin-Error"))
	}
}

func TestForwardProxy_AuthFailed_EmitsNoEvents(t *testing.T) {
	emitter := newMockEventEmitter()
	fp := &ForwardProxy{token: "tok", events: emitter}
	req := httptest.NewRequest("GET", "http://example.com/", nil)
	req.Header.Set("Proxy-Authorization", basicAuth("wrong-token", "plat:acct"))
	w := httptest.NewRecorder()

	fp.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}

	select {
	case ev := <-emitter.finishedCh:
		t.Fatalf("unexpected forward finished event for auth error: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}

	select {
	case logEv := <-emitter.logCh:
		t.Fatalf("unexpected forward log event for auth error: %+v", logEv)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestForwardProxy_CONNECT_AuthRequired_EmitsNoEvents(t *testing.T) {
	emitter := newMockEventEmitter()
	fp := &ForwardProxy{token: "tok", events: emitter}
	req := httptest.NewRequest(http.MethodConnect, "http://example.com:443", nil)
	req.Host = "example.com:443"
	w := httptest.NewRecorder()

	fp.ServeHTTP(w, req)

	if w.Code != http.StatusProxyAuthRequired {
		t.Fatalf("expected 407, got %d", w.Code)
	}

	select {
	case ev := <-emitter.finishedCh:
		t.Fatalf("unexpected forward finished event for auth error: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}

	select {
	case logEv := <-emitter.logCh:
		t.Fatalf("unexpected forward log event for auth error: %+v", logEv)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestForwardProxy_Authentication_DisabledWhenProxyTokenEmpty(t *testing.T) {
	fp := &ForwardProxy{token: "", events: NoOpEventEmitter{}}

	tests := []struct {
		name string
		auth string
	}{
		{name: "missing"},
		{name: "invalid_scheme", auth: "Digest abc"},
		{name: "invalid_base64", auth: "Basic %%%"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "http://example.com/", nil)
			if tt.auth != "" {
				req.Header.Set("Proxy-Authorization", tt.auth)
			}
			plat, acct, err := fp.authenticate(req)
			if err != nil {
				t.Fatalf("unexpected auth error: %v", err)
			}
			if plat != "" || acct != "" {
				t.Fatalf("expected empty identity, got plat=%q acct=%q", plat, acct)
			}
		})
	}
}

func TestForwardProxy_Authentication_Disabled_AllowsOptionalIdentity(t *testing.T) {
	fp := &ForwardProxy{token: "", events: NoOpEventEmitter{}}
	req := httptest.NewRequest("GET", "http://example.com/", nil)
	req.Header.Set("Proxy-Authorization", basicAuth("any-token", "plat:acct"))

	plat, acct, err := fp.authenticate(req)
	if err != nil {
		t.Fatalf("unexpected auth error: %v", err)
	}
	if plat != "plat" || acct != "acct" {
		t.Fatalf("got plat=%q acct=%q, want plat=%q acct=%q", plat, acct, "plat", "acct")
	}
}

func TestForwardProxy_Authentication_Disabled_AllowsTwoFieldIdentity(t *testing.T) {
	fp := &ForwardProxy{token: "", events: NoOpEventEmitter{}}
	req := httptest.NewRequest("GET", "http://example.com/", nil)
	req.Header.Set("Proxy-Authorization", basicAuth("plat", "acct"))

	plat, acct, err := fp.authenticate(req)
	if err != nil {
		t.Fatalf("unexpected auth error: %v", err)
	}
	if plat != "plat" || acct != "acct" {
		t.Fatalf("got plat=%q acct=%q, want plat=%q acct=%q", plat, acct, "plat", "acct")
	}
}

func TestForwardProxy_Authentication_ParsePlatformAccount(t *testing.T) {
	fp := &ForwardProxy{token: "tok", events: NoOpEventEmitter{}}

	tests := []struct {
		pass     string
		wantPlat string
		wantAcct string
	}{
		{"platform:account", "platform", "account"},
		{"platform", "platform", ""},
		{"platform:account:extra", "platform", "account:extra"},
		{":account", "", "account"},
	}

	for _, tt := range tests {
		req := httptest.NewRequest("GET", "http://example.com/", nil)
		req.Header.Set("Proxy-Authorization", basicAuth("tok", tt.pass))
		plat, acct, err := fp.authenticate(req)
		if err != nil {
			t.Fatalf("pass=%q: unexpected auth error: %v", tt.pass, err)
		}
		if plat != tt.wantPlat || acct != tt.wantAcct {
			t.Fatalf("pass=%q: got plat=%q acct=%q, want plat=%q acct=%q",
				tt.pass, plat, acct, tt.wantPlat, tt.wantAcct)
		}
	}
}

func TestForwardProxy_Authentication_BasicSchemeCaseInsensitive(t *testing.T) {
	fp := &ForwardProxy{token: "tok", events: NoOpEventEmitter{}}
	credential := base64.StdEncoding.EncodeToString([]byte("tok:plat:acct"))

	tests := []string{
		"basic " + credential,
		"BASIC " + credential,
		"BaSiC " + credential,
		"  basic   " + credential + "   ",
	}

	for _, auth := range tests {
		req := httptest.NewRequest("GET", "http://example.com/", nil)
		req.Header.Set("Proxy-Authorization", auth)
		plat, acct, err := fp.authenticate(req)
		if err != nil {
			t.Fatalf("auth=%q: unexpected auth error: %v", auth, err)
		}
		if plat != "plat" || acct != "acct" {
			t.Fatalf("auth=%q: got plat=%q acct=%q, want plat=%q acct=%q", auth, plat, acct, "plat", "acct")
		}
	}
}

func TestForwardProxy_StripHopByHopHeaders(t *testing.T) {
	header := http.Header{}
	header.Set("Proxy-Authorization", "Basic xxx")
	header.Set("Proxy-Connection", "keep-alive")
	header.Set("Keep-Alive", "timeout=5")
	header.Set("Proxy-Authenticate", `Basic realm="Upstream"`)
	header.Set("Trailer", "X-Trailer")
	header.Set("Connection", "X-Custom-Header")
	header.Set("X-Custom-Header", "value")
	header.Set("X-Normal-Header", "should-remain")

	stripHopByHopHeaders(header)

	for _, h := range []string{
		"Proxy-Authorization", "Proxy-Connection", "Keep-Alive",
		"Proxy-Authenticate", "Trailer", "Connection", "X-Custom-Header",
	} {
		if header.Get(h) != "" {
			t.Fatalf("header %q should have been stripped", h)
		}
	}
	if header.Get("X-Normal-Header") != "should-remain" {
		t.Fatal("X-Normal-Header should remain")
	}
}

func TestPrepareForwardOutboundRequest_NormalizesClientCloseAndHeaders(t *testing.T) {
	req := httptest.NewRequest("GET", "http://example.com/path?q=1", nil)
	req.RequestURI = "http://example.com/path?q=1"
	req.Close = true
	req.Header.Set("Proxy-Authorization", "Basic xxx")
	req.Header.Set("Connection", "close, X-Custom-Header")
	req.Header.Set("X-Custom-Header", "value")
	req.Header.Set("X-Normal-Header", "keep")

	out := prepareForwardOutboundRequest(req)

	if out == req {
		t.Fatal("expected cloned request")
	}
	if out.RequestURI != "" {
		t.Fatalf("RequestURI should be empty for client RoundTrip, got %q", out.RequestURI)
	}
	if out.Close {
		t.Fatal("expected Close=false to preserve upstream keep-alive reuse")
	}
	if out.Header.Get("Proxy-Authorization") != "" {
		t.Fatal("Proxy-Authorization should be stripped")
	}
	if out.Header.Get("X-Custom-Header") != "" {
		t.Fatal("connection-listed header should be stripped")
	}
	if out.Header.Get("X-Normal-Header") != "keep" {
		t.Fatal("normal header should remain")
	}

	// Original request should not be mutated.
	if !req.Close {
		t.Fatal("original Close flag should remain unchanged")
	}
	if req.RequestURI == "" {
		t.Fatal("original RequestURI should remain unchanged")
	}
	if req.Header.Get("Proxy-Authorization") == "" {
		t.Fatal("original Proxy-Authorization should remain unchanged")
	}
	if req.Header.Get("X-Custom-Header") == "" {
		t.Fatal("original custom header should remain unchanged")
	}
}

func TestForwardProxy_AuthAndSetup(t *testing.T) {
	// Start upstream server.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Proxy-Authorization is stripped.
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("Proxy-Authorization leaked to upstream")
		}
		w.Header().Set("X-Upstream", "ok")
		w.WriteHeader(200)
		w.Write([]byte("upstream-response"))
	}))
	defer upstream.Close()

	// Create a mock entry with an outbound that dials direct.
	entry := node.NewNodeEntry(node.Hash{1}, nil, time.Now(), 0)
	ob := &mockOutbound{
		dialFunc: func(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
			return net.Dial("tcp", upstream.Listener.Addr().String())
		},
	}
	var obAdapter adapter.Outbound = ob
	entry.Outbound.Store(&obAdapter)

	pool := &mockPool{entry: entry}
	health := &mockHealthRecorder{}

	// Test authentication parsing and dependency wiring (not full ServeHTTP).
	fp := &ForwardProxy{
		token:  "tok",
		pool:   pool,
		health: health,
		events: NoOpEventEmitter{},
	}

	req := httptest.NewRequest("GET", upstream.URL, nil)
	req.Header.Set("Proxy-Authorization", basicAuth("tok", "plat:acct"))
	plat, acct, authErr := fp.authenticate(req)
	if authErr != nil {
		t.Fatalf("auth failed: %v", authErr)
	}
	if plat != "plat" || acct != "acct" {
		t.Fatalf("got plat=%q acct=%q", plat, acct)
	}
}

func TestForwardProxy_ClassifyConnectError_AlwaysConnectFailed(t *testing.T) {
	// Verify that classifyConnectError returns UPSTREAM_CONNECT_FAILED
	// for all non-timeout, non-canceled errors.
	genericErr := &net.AddrError{Err: "some addr error", Addr: "1.2.3.4"}
	pe := classifyConnectError(genericErr)
	if pe != ErrUpstreamConnectFailed {
		t.Fatalf("expected UPSTREAM_CONNECT_FAILED for generic error in CONNECT, got %v", pe)
	}

	// classifyUpstreamError (non-CONNECT) returns REQUEST_FAILED for the same error.
	pe2 := classifyUpstreamError(genericErr)
	if pe2 != ErrUpstreamRequestFailed {
		t.Fatalf("expected UPSTREAM_REQUEST_FAILED for generic error in HTTP, got %v", pe2)
	}

	// Dial errors also differ between the two classifiers.
	pe3 := classifyConnectError(dialErr{})
	if pe3 != ErrUpstreamConnectFailed {
		t.Fatalf("expected UPSTREAM_CONNECT_FAILED for dial error in CONNECT, got %v", pe3)
	}
	pe4 := classifyUpstreamError(dialErr{})
	if pe4 != ErrUpstreamRequestFailed {
		t.Fatalf("expected UPSTREAM_REQUEST_FAILED for dial error in HTTP, got %v", pe4)
	}
}

func TestClassifyConnectError_Timeout(t *testing.T) {
	pe := classifyConnectError(deadlineExceededErr{})
	if pe != ErrUpstreamTimeout {
		t.Fatalf("expected ErrUpstreamTimeout, got %v", pe)
	}
}

func TestClassifyConnectError_Canceled(t *testing.T) {
	pe := classifyConnectError(canceledErr{})
	if pe != nil {
		t.Fatalf("expected nil for canceled, got %v", pe)
	}
}

func TestIsValidHost(t *testing.T) {
	tests := []struct {
		host  string
		valid bool
	}{
		{"example.com", true},
		{"example.com:443", true},
		{"192.168.1.1", true},
		{"192.168.1.1:8080", true},
		{"[::1]", true},
		{"[::1]:443", true},
		{"", false},
		{"host with spaces", false},
		{":443", false},
		{"host/path", false},
		{"user@example.com", false},
		{"2001:db8::1", false},
	}
	for _, tt := range tests {
		got := isValidHost(tt.host)
		if got != tt.valid {
			t.Errorf("isValidHost(%q) = %v, want %v", tt.host, got, tt.valid)
		}
	}
}

func TestReverseParsePath_InvalidHostUserinfo(t *testing.T) {
	rp := &ReverseProxy{token: "tok", events: NoOpEventEmitter{}}
	_, err := rp.parsePath("/tok/plat:acct/https/user@example.com/path")
	if err != ErrInvalidHost {
		t.Fatalf("expected INVALID_HOST for userinfo host, got %v", err)
	}
}

func TestWriteProxyError(t *testing.T) {
	tests := []struct {
		name       string
		err        *ProxyError
		wantCode   int
		wantHeader string
	}{
		{"AuthRequired", ErrAuthRequired, 407, "AUTH_REQUIRED"},
		{"AuthFailed", ErrAuthFailed, 403, "AUTH_FAILED"},
		{"URLParseError", ErrURLParseError, 400, "URL_PARSE_ERROR"},
		{"InvalidProtocol", ErrInvalidProtocol, 400, "INVALID_PROTOCOL"},
		{"InvalidHost", ErrInvalidHost, 400, "INVALID_HOST"},
		{"PlatformNotFound", ErrPlatformNotFound, 404, "PLATFORM_NOT_FOUND"},
		{"AccountRejected", ErrAccountRejected, 403, "ACCOUNT_REJECTED"},
		{"NoAvailableNodes", ErrNoAvailableNodes, 503, "NO_AVAILABLE_NODES"},
		{"UpstreamConnectFailed", ErrUpstreamConnectFailed, 502, "UPSTREAM_CONNECT_FAILED"},
		{"UpstreamTimeout", ErrUpstreamTimeout, 504, "UPSTREAM_TIMEOUT"},
		{"UpstreamRequestFailed", ErrUpstreamRequestFailed, 502, "UPSTREAM_REQUEST_FAILED"},
		{"InternalError", ErrInternalError, 500, "INTERNAL_ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeProxyError(w, tt.err)
			if w.Code != tt.wantCode {
				t.Fatalf("code: got %d, want %d", w.Code, tt.wantCode)
			}
			if w.Header().Get("X-Resin-Error") != tt.wantHeader {
				t.Fatalf("X-Resin-Error: got %q, want %q", w.Header().Get("X-Resin-Error"), tt.wantHeader)
			}
		})
	}
}

func TestMapRouteError(t *testing.T) {
	if pe := mapRouteError(routing.ErrPlatformNotFound); pe != ErrPlatformNotFound {
		t.Fatalf("expected ErrPlatformNotFound, got %v", pe)
	}
	if pe := mapRouteError(routing.ErrNoAvailableNodes); pe != ErrNoAvailableNodes {
		t.Fatalf("expected ErrNoAvailableNodes, got %v", pe)
	}
	if pe := mapRouteError(io.ErrUnexpectedEOF); pe != ErrInternalError {
		t.Fatalf("expected ErrInternalError for unknown, got %v", pe)
	}
}

// --- Reverse proxy path parsing tests ---

func TestReverseParsePath_Valid(t *testing.T) {
	rp := &ReverseProxy{token: "tok", events: NoOpEventEmitter{}}

	parsed, err := rp.parsePath("/tok/myplat:acct/https/example.com/path/to/resource")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.PlatformName != "myplat" {
		t.Fatalf("PlatformName: got %q, want %q", parsed.PlatformName, "myplat")
	}
	if parsed.Account != "acct" {
		t.Fatalf("Account: got %q, want %q", parsed.Account, "acct")
	}
	if parsed.Protocol != "https" {
		t.Fatalf("Protocol: got %q, want %q", parsed.Protocol, "https")
	}
	if parsed.Host != "example.com" {
		t.Fatalf("Host: got %q, want %q", parsed.Host, "example.com")
	}
	if parsed.Path != "path/to/resource" {
		t.Fatalf("Path: got %q, want %q", parsed.Path, "path/to/resource")
	}
}

func TestReverseParsePath_TokenMismatch(t *testing.T) {
	rp := &ReverseProxy{token: "correct", events: NoOpEventEmitter{}}
	_, err := rp.parsePath("/wrong/plat:acct/https/example.com/")
	if err != ErrAuthFailed {
		t.Fatalf("expected AUTH_FAILED, got %v", err)
	}
}

func TestReverseParsePath_TokenIgnoredWhenProxyTokenEmpty(t *testing.T) {
	rp := &ReverseProxy{token: "", events: NoOpEventEmitter{}}
	parsed, err := rp.parsePath("/any-value/plat:acct/https/example.com/path")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.PlatformName != "plat" || parsed.Account != "acct" {
		t.Fatalf("unexpected parsed identity: plat=%q acct=%q", parsed.PlatformName, parsed.Account)
	}
}

func TestReverseParsePath_EmptyAuthSegmentAllowedWhenProxyTokenEmpty(t *testing.T) {
	rp := &ReverseProxy{token: "", events: NoOpEventEmitter{}}
	parsed, err := rp.parsePath("//plat:acct/https/example.com/path")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.PlatformName != "plat" || parsed.Account != "acct" {
		t.Fatalf("unexpected parsed identity: plat=%q acct=%q", parsed.PlatformName, parsed.Account)
	}
}

func TestReverseParsePath_TooFewSegments(t *testing.T) {
	rp := &ReverseProxy{token: "tok", events: NoOpEventEmitter{}}
	_, err := rp.parsePath("/tok/plat")
	if err != ErrURLParseError {
		t.Fatalf("expected URL_PARSE_ERROR, got %v", err)
	}
}

func TestReverseParsePath_InvalidProtocol(t *testing.T) {
	rp := &ReverseProxy{token: "tok", events: NoOpEventEmitter{}}
	_, err := rp.parsePath("/tok/plat:acct/ftp/example.com/")
	if err != ErrInvalidProtocol {
		t.Fatalf("expected INVALID_PROTOCOL, got %v", err)
	}
}

func TestReverseParsePath_InvalidHost(t *testing.T) {
	rp := &ReverseProxy{token: "tok", events: NoOpEventEmitter{}}
	var err *ProxyError

	// Host with spaces.
	_, err = rp.parsePath("/tok/plat:acct/https/host with space/path")
	if err != ErrInvalidHost {
		t.Fatalf("expected INVALID_HOST for host with space, got %v", err)
	}

	// Percent-encoded host spaces are decoded before host validation.
	_, err = rp.parsePath("/tok/plat:acct/https/host%20with%20space/path")
	if err != ErrInvalidHost {
		t.Fatalf("expected INVALID_HOST for escaped host with space, got %v", err)
	}
}

func TestReverseParsePath_EmptyPlatform(t *testing.T) {
	rp := &ReverseProxy{token: "tok", events: NoOpEventEmitter{}}
	parsed, err := rp.parsePath("/tok/:acct/https/example.com/path")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Empty platform name is valid — will fall through to default.
	if parsed.PlatformName != "" {
		t.Fatalf("expected empty platform, got %q", parsed.PlatformName)
	}
}

func TestReverseParsePath_NoAccount(t *testing.T) {
	rp := &ReverseProxy{token: "tok", events: NoOpEventEmitter{}}
	parsed, err := rp.parsePath("/tok/myplat:/https/example.com/path")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.Account != "" {
		t.Fatalf("expected empty account, got %q", parsed.Account)
	}
}

func TestReverseParsePath_PreservesEscapedPathForUpstream(t *testing.T) {
	rp := &ReverseProxy{token: "tok", events: NoOpEventEmitter{}}
	parsed, err := rp.parsePath("/tok/myplat:acct/https/example.com/v1/users/team%2Fa/profile")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.Path != "v1/users/team%2Fa/profile" {
		t.Fatalf("Path: got %q, want %q", parsed.Path, "v1/users/team%2Fa/profile")
	}

	target, perr := buildReverseTargetURL(parsed, "q=1")
	if perr != nil {
		t.Fatalf("unexpected target build error: %v", perr)
	}
	if got := target.EscapedPath(); got != "/v1/users/team%2Fa/profile" {
		t.Fatalf("EscapedPath: got %q, want %q", got, "/v1/users/team%2Fa/profile")
	}
	if got := target.Path; got != "/v1/users/team/a/profile" {
		t.Fatalf("Path: got %q, want %q", got, "/v1/users/team/a/profile")
	}
	if got := target.String(); got != "https://example.com/v1/users/team%2Fa/profile?q=1" {
		t.Fatalf("target.String(): got %q, want %q", got, "https://example.com/v1/users/team%2Fa/profile?q=1")
	}
}

func TestResolveDefaultPlatform(t *testing.T) {
	defaultPlat := &platform.Platform{
		ID:                     platform.DefaultPlatformID,
		Name:                   platform.DefaultPlatformName,
		ReverseProxyMissAction: "REJECT",
	}

	rp := &ReverseProxy{
		token: "tok",
		platLook: &mockPlatformLookup{
			platforms: map[string]*platform.Platform{
				platform.DefaultPlatformID: defaultPlat,
			},
		},
		events: NoOpEventEmitter{},
	}

	plat := rp.resolveDefaultPlatform()
	if plat == nil {
		t.Fatal("expected default platform, got nil")
	}
	if plat.ReverseProxyMissAction != "REJECT" {
		t.Fatalf("expected REJECT, got %q", plat.ReverseProxyMissAction)
	}
}

func TestEffectiveEmptyAccountBehavior_DefaultsToRandom(t *testing.T) {
	if got := effectiveEmptyAccountBehavior(nil); got != platform.ReverseProxyEmptyAccountBehaviorRandom {
		t.Fatalf("nil platform behavior: got %q, want %q", got, platform.ReverseProxyEmptyAccountBehaviorRandom)
	}
	plat := &platform.Platform{ReverseProxyEmptyAccountBehavior: "INVALID"}
	if got := effectiveEmptyAccountBehavior(plat); got != platform.ReverseProxyEmptyAccountBehaviorRandom {
		t.Fatalf("invalid platform behavior: got %q, want %q", got, platform.ReverseProxyEmptyAccountBehaviorRandom)
	}
}

func TestShouldRejectReverseProxyAccountExtractionFailure(t *testing.T) {
	plat := &platform.Platform{ReverseProxyMissAction: string(platform.ReverseProxyMissActionReject)}
	if !shouldRejectReverseProxyAccountExtractionFailure(true, plat) {
		t.Fatal("expected REJECT when extraction failed and miss action is REJECT")
	}
	plat.ReverseProxyMissAction = string(platform.ReverseProxyMissActionTreatAsEmpty)
	if shouldRejectReverseProxyAccountExtractionFailure(true, plat) {
		t.Fatal("treat-as-empty miss action should not reject")
	}
	plat.ReverseProxyMissAction = "RANDOM"
	if shouldRejectReverseProxyAccountExtractionFailure(true, plat) {
		t.Fatal("invalid miss action should not reject")
	}
	if shouldRejectReverseProxyAccountExtractionFailure(false, plat) {
		t.Fatal("no extraction failure should not reject")
	}
	if shouldRejectReverseProxyAccountExtractionFailure(true, nil) {
		t.Fatal("missing platform should not trigger account rejection")
	}
}

func TestReverseProxy_ResolveReverseProxyAccount_BehaviorRandom(t *testing.T) {
	rp := &ReverseProxy{
		matcher: BuildAccountMatcher([]model.AccountHeaderRule{
			{URLPrefix: "*", Headers: []string{"Authorization"}},
		}),
	}
	plat := &platform.Platform{
		ReverseProxyEmptyAccountBehavior: string(platform.ReverseProxyEmptyAccountBehaviorRandom),
		ReverseProxyFixedAccountHeader:   "X-Account-Id",
	}
	parsed := &parsedPath{Host: "example.com", Path: "v1/users"}
	req := httptest.NewRequest("GET", "http://example.com/", nil)
	req.Header.Set("Authorization", "acct-from-rule")
	req.Header.Set("X-Account-Id", "acct-from-fixed")

	account, behavior, extractionFailed := rp.resolveReverseProxyAccount(parsed, req, plat)
	if behavior != platform.ReverseProxyEmptyAccountBehaviorRandom {
		t.Fatalf("behavior: got %q, want %q", behavior, platform.ReverseProxyEmptyAccountBehaviorRandom)
	}
	if account != "" {
		t.Fatalf("account: got %q, want empty", account)
	}
	if extractionFailed {
		t.Fatal("random behavior should not mark extraction failure")
	}
}

func TestReverseProxy_ResolveReverseProxyAccount_BehaviorFixedHeader(t *testing.T) {
	rp := &ReverseProxy{
		matcher: BuildAccountMatcher([]model.AccountHeaderRule{
			{URLPrefix: "*", Headers: []string{"Authorization"}},
		}),
	}
	plat := &platform.Platform{
		ReverseProxyEmptyAccountBehavior: string(platform.ReverseProxyEmptyAccountBehaviorFixedHeader),
		ReverseProxyFixedAccountHeader:   "X-Missing\nX-Account-Id\nAuthorization",
	}
	parsed := &parsedPath{Host: "example.com", Path: "v1/users"}
	req := httptest.NewRequest("GET", "http://example.com/", nil)
	req.Header.Set("Authorization", "acct-from-rule")
	req.Header.Set("X-Account-Id", "acct-from-fixed")

	account, behavior, extractionFailed := rp.resolveReverseProxyAccount(parsed, req, plat)
	if behavior != platform.ReverseProxyEmptyAccountBehaviorFixedHeader {
		t.Fatalf("behavior: got %q, want %q", behavior, platform.ReverseProxyEmptyAccountBehaviorFixedHeader)
	}
	if account != "acct-from-fixed" {
		t.Fatalf("account: got %q, want %q", account, "acct-from-fixed")
	}
	if extractionFailed {
		t.Fatal("successful fixed-header extraction should not fail")
	}
}

func TestReverseProxy_ResolveReverseProxyAccount_BehaviorAccountHeaderRule(t *testing.T) {
	rp := &ReverseProxy{
		matcher: BuildAccountMatcher([]model.AccountHeaderRule{
			{URLPrefix: "example.com/v1", Headers: []string{"Authorization"}},
			{URLPrefix: "*", Headers: []string{"X-Fallback"}},
		}),
	}
	plat := &platform.Platform{
		ReverseProxyEmptyAccountBehavior: string(platform.ReverseProxyEmptyAccountBehaviorAccountHeaderRule),
		ReverseProxyFixedAccountHeader:   "X-Account-Id",
	}
	parsed := &parsedPath{Host: "example.com", Path: "v1/users"}
	req := httptest.NewRequest("GET", "http://example.com/", nil)
	req.Header.Set("Authorization", "acct-from-rule")
	req.Header.Set("X-Account-Id", "acct-from-fixed")

	account, behavior, extractionFailed := rp.resolveReverseProxyAccount(parsed, req, plat)
	if behavior != platform.ReverseProxyEmptyAccountBehaviorAccountHeaderRule {
		t.Fatalf("behavior: got %q, want %q", behavior, platform.ReverseProxyEmptyAccountBehaviorAccountHeaderRule)
	}
	if account != "acct-from-rule" {
		t.Fatalf("account: got %q, want %q", account, "acct-from-rule")
	}
	if extractionFailed {
		t.Fatal("successful rule extraction should not fail")
	}
}

func TestFixedAccountHeadersForPlatform(t *testing.T) {
	plat := &platform.Platform{
		ReverseProxyFixedAccountHeaders: []string{"Authorization", "X-Account-Id"},
	}
	got := fixedAccountHeadersForPlatform(plat)
	if len(got) != 2 || got[0] != "Authorization" || got[1] != "X-Account-Id" {
		t.Fatalf("headers: got %v, want %v", got, []string{"Authorization", "X-Account-Id"})
	}
	got[0] = "Mutated"
	if plat.ReverseProxyFixedAccountHeaders[0] != "Authorization" {
		t.Fatalf("platform headers should be immutable copy, got %v", plat.ReverseProxyFixedAccountHeaders)
	}

	plat = &platform.Platform{
		ReverseProxyFixedAccountHeader: " authorization \nX-Account-Id\nx-account-id",
	}
	got = fixedAccountHeadersForPlatform(plat)
	if len(got) != 2 || got[0] != "Authorization" || got[1] != "X-Account-Id" {
		t.Fatalf("parsed headers: got %v, want %v", got, []string{"Authorization", "X-Account-Id"})
	}

	plat = &platform.Platform{
		ReverseProxyFixedAccountHeader: "bad header",
	}
	got = fixedAccountHeadersForPlatform(plat)
	if len(got) != 0 {
		t.Fatalf("invalid raw headers should produce empty list, got %v", got)
	}
}

type mockPlatformLookup struct {
	platforms     map[string]*platform.Platform // keyed by ID
	platformNames map[string]*platform.Platform // keyed by name
}

func (m *mockPlatformLookup) GetPlatform(id string) (*platform.Platform, bool) {
	if m.platforms == nil {
		return nil, false
	}
	p, ok := m.platforms[id]
	return p, ok
}

func (m *mockPlatformLookup) GetPlatformByName(name string) (*platform.Platform, bool) {
	if m.platformNames == nil {
		return nil, false
	}
	p, ok := m.platformNames[name]
	return p, ok
}

func TestNoOpEventEmitter(t *testing.T) {
	// Verify NoOpEventEmitter satisfies the interface and doesn't panic.
	var e EventEmitter = NoOpEventEmitter{}
	e.EmitRequestFinished(RequestFinishedEvent{})
	e.EmitRequestLog(RequestLogEntry{})
}

func TestReverseProxy_AccountRejection_EmptyPlatform(t *testing.T) {
	// When PlatformName is empty and the default platform has REJECT,
	// the request should be rejected with ACCOUNT_REJECTED.
	defaultPlat := &platform.Platform{
		ID:                               platform.DefaultPlatformID,
		Name:                             platform.DefaultPlatformName,
		ReverseProxyMissAction:           "REJECT",
		ReverseProxyEmptyAccountBehavior: string(platform.ReverseProxyEmptyAccountBehaviorAccountHeaderRule),
	}

	rp := &ReverseProxy{
		token: "tok",
		platLook: &mockPlatformLookup{
			platforms: map[string]*platform.Platform{
				platform.DefaultPlatformID: defaultPlat,
			},
		},
		matcher: BuildAccountMatcher(nil),
		events:  NoOpEventEmitter{},
	}

	// Path: /tok/:/https/example.com/path — empty platform, no account.
	req := httptest.NewRequest("GET", "/tok/:/https/example.com/path", nil)
	w := httptest.NewRecorder()
	rp.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
	if w.Header().Get("X-Resin-Error") != "ACCOUNT_REJECTED" {
		t.Fatalf("expected ACCOUNT_REJECTED, got %q", w.Header().Get("X-Resin-Error"))
	}
}

func TestReverseProxy_HostValidation(t *testing.T) {
	rp := &ReverseProxy{token: "tok", events: NoOpEventEmitter{}}

	// Test via parsePath directly — host with invalid characters.
	_, err := rp.parsePath("/tok/plat:acct/https/host with space/path")
	if err != ErrInvalidHost {
		t.Fatalf("expected INVALID_HOST for host with space, got %v", err)
	}

	// Empty host in port-only form.
	_, err = rp.parsePath("/tok/plat:acct/https/:443/path")
	if err != ErrInvalidHost {
		t.Fatalf("expected INVALID_HOST for :443, got %v", err)
	}
}

func TestReverseProxy_FullPath_HostWithPort(t *testing.T) {
	rp := &ReverseProxy{token: "tok", events: NoOpEventEmitter{}}
	parsed, err := rp.parsePath("/tok/plat:acct/https/example.com:8443/api/v1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.Host != "example.com:8443" {
		t.Fatalf("Host: got %q, want %q", parsed.Host, "example.com:8443")
	}
}

// Ensure EgressIP in route result is serializable.
func TestRouteResult_EgressIP(t *testing.T) {
	ip := netip.MustParseAddr("1.2.3.4")
	result := routing.RouteResult{
		NodeHash: node.Hash{1},
		EgressIP: ip,
	}
	if result.EgressIP.String() != "1.2.3.4" {
		t.Fatalf("expected 1.2.3.4, got %q", result.EgressIP.String())
	}
}

func TestReverseParsePath_ProtocolCaseInsensitive(t *testing.T) {
	rp := &ReverseProxy{token: "tok", events: NoOpEventEmitter{}}
	parsed, err := rp.parsePath("/tok/plat:acct/HTTPS/example.com/path")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.Protocol != "https" {
		t.Fatalf("Protocol: got %q, want %q", parsed.Protocol, "https")
	}
}

func TestEventEmitterInterface(t *testing.T) {
	// Verify the RequestLogEntry fields match DESIGN.md schema.
	entry := RequestLogEntry{
		ProxyType:    ProxyTypeForward,
		ClientIP:     "127.0.0.1:12345",
		PlatformID:   "test-id",
		PlatformName: "test",
		Account:      "acct",
		TargetHost:   "example.com",
		TargetURL:    "https://example.com/path",
		NodeHash:     "abc123",
		EgressIP:     "1.2.3.4",
		DurationNs:   12345678,
		NetOK:        true,
		HTTPMethod:   "GET",
		HTTPStatus:   200,
	}
	// Just verify fields are accessible (compile-time check).
	if entry.ProxyType != ProxyTypeForward || !entry.NetOK {
		t.Fatal("unexpected field values")
	}
}

func TestAccountExtraction_InReverseProxy(t *testing.T) {
	// Test that the reverse proxy correctly uses the account matcher
	// to extract accounts from headers.
	rp := &ReverseProxy{
		token: "tok",
		platLook: &mockPlatformLookup{
			platformNames: map[string]*platform.Platform{
				"plat": {ID: "p1", Name: "plat", ReverseProxyMissAction: "TREAT_AS_EMPTY"},
			},
		},
		events: NoOpEventEmitter{},
	}

	// Test extractAccountFromHeaders directly.
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Account-Id", "acct-from-header")

	account := extractAccountFromHeaders(req, []string{"Authorization", "X-Account-Id"})
	if account != "acct-from-header" {
		t.Fatalf("expected acct-from-header, got %q", account)
	}
	_ = rp // used for context
}

func TestHealthRecorderAsyncCall(t *testing.T) {
	// Verify that async health recording doesn't block.
	h := &mockHealthRecorder{}
	hash := node.Hash{1}

	// Simulate async dispatch as done in proxy code.
	go h.RecordResult(hash, true)
	go h.RecordResult(hash, false)
	latency := 10 * time.Millisecond
	go h.RecordLatency(hash, "example.com", &latency)

	// Give goroutines time to complete.
	time.Sleep(50 * time.Millisecond)

	if h.resultCalls.Load() != 2 {
		t.Fatalf("expected 2 result calls, got %d", h.resultCalls.Load())
	}
	if h.latencyCalls.Load() != 1 {
		t.Fatalf("expected 1 latency call, got %d", h.latencyCalls.Load())
	}
}

func TestForwardProxy_HTTP_UpstreamError_Timeout(t *testing.T) {
	// Test that a timeout error from upstream produces 504 UPSTREAM_TIMEOUT.
	// We test the classification function with a real context deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(10 * time.Millisecond) // ensure deadline passes

	select {
	case <-ctx.Done():
		pe := classifyUpstreamError(ctx.Err())
		if pe != ErrUpstreamTimeout {
			t.Fatalf("expected ErrUpstreamTimeout for deadline exceeded, got %v", pe)
		}
	default:
		t.Fatal("context should have expired")
	}
}

func TestTLSLatencyConn_WriteReadFlow(t *testing.T) {
	// Create a pipe to simulate a connection.
	client, server := net.Pipe()
	defer server.Close()

	var capturedLatency atomic.Int64
	done := make(chan struct{})

	conn := newTLSLatencyConn(client, func(latency time.Duration) {
		capturedLatency.Store(int64(latency))
		close(done)
	})

	// Run server side in a goroutine: read Client Hello, then write Server Hello.
	go func() {
		buf := make([]byte, 100)
		n, _ := server.Read(buf) // reads "client hello"
		if n == 0 {
			return
		}
		// Small delay to ensure measurable latency.
		time.Sleep(5 * time.Millisecond)
		server.Write([]byte("server hello"))
	}()

	// Client side: write Client Hello (triggers state 0→1).
	_, err := conn.Write([]byte("client hello"))
	if err != nil {
		t.Fatalf("write error: %v", err)
	}

	// Client side: read Server Hello (triggers state 1→2, fires callback).
	readBuf := make([]byte, 100)
	_, err = conn.Read(readBuf)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}

	// Wait for async callback.
	select {
	case <-done:
		lat := capturedLatency.Load()
		if lat <= 0 {
			t.Fatal("expected positive latency")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("latency callback not fired")
	}

	conn.Close()
}

func TestReverseProxy_AccountExtraction_WithMatcher(t *testing.T) {
	// Integration: matcher returns headers, extractAccountFromHeaders picks first non-empty.
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Alt-Account", "alt-acct")
	// "Authorization" not set, so first non-empty is "X-Alt-Account".

	headers := []string{"Authorization", "X-Alt-Account"}
	account := extractAccountFromHeaders(req, headers)
	if account != "alt-acct" {
		t.Fatalf("expected alt-acct, got %q", account)
	}

	// Now set Authorization — it should win.
	req.Header.Set("Authorization", "primary-acct")
	account = extractAccountFromHeaders(req, headers)
	if account != "primary-acct" {
		t.Fatalf("expected primary-acct, got %q", account)
	}
}

func TestReverseParsePath_EmptyPath(t *testing.T) {
	rp := &ReverseProxy{token: "tok", events: NoOpEventEmitter{}}
	_, err := rp.parsePath("")
	if err != ErrAuthFailed {
		t.Fatalf("expected AUTH_FAILED for empty path, got %v", err)
	}
}

func TestReverseParsePath_OnlyToken(t *testing.T) {
	rp := &ReverseProxy{token: "tok", events: NoOpEventEmitter{}}
	_, err := rp.parsePath("/tok")
	if err != ErrURLParseError {
		t.Fatalf("expected URL_PARSE_ERROR for path with only token, got %v", err)
	}
}

func TestReverseProxy_ParseError_EmitsNoEvents(t *testing.T) {
	emitter := newMockEventEmitter()
	rp := &ReverseProxy{
		token: "tok",
		events: ConfigAwareEventEmitter{
			Base:                         emitter,
			RequestLogEnabled:            func() bool { return true },
			ReverseProxyLogDetailEnabled: func() bool { return true },
		},
	}
	req := httptest.NewRequest("GET", "/tok", nil)
	req.Header.Set("X-Req", "capture-me")
	w := httptest.NewRecorder()

	rp.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}

	select {
	case ev := <-emitter.finishedCh:
		t.Fatalf("unexpected reverse finished event for parse error: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}

	select {
	case logEv := <-emitter.logCh:
		t.Fatalf("unexpected reverse log event for parse error: %+v", logEv)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestReverseProxy_ParseError_DefaultNoEvents(t *testing.T) {
	emitter := newMockEventEmitter()
	rp := &ReverseProxy{token: "tok", events: emitter}
	req := httptest.NewRequest("GET", "/tok", nil)
	req.Header.Set("X-Req", "should-not-capture")
	w := httptest.NewRecorder()

	rp.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}

	select {
	case logEv := <-emitter.logCh:
		t.Fatalf("unexpected reverse log event for parse error: %+v", logEv)
	case <-time.After(100 * time.Millisecond):
	}

	select {
	case ev := <-emitter.finishedCh:
		t.Fatalf("unexpected reverse finished event for parse error: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

// Ensure that reverse.go handles the case where PlatformName is not found.
// This should be handled by the router (which returns ErrPlatformNotFound).
func TestMapRouteError_PlatformNotFound(t *testing.T) {
	pe := mapRouteError(routing.ErrPlatformNotFound)
	if pe.HTTPCode != 404 || pe.ResinError != "PLATFORM_NOT_FOUND" {
		t.Fatalf("expected 404 PLATFORM_NOT_FOUND, got %d %s", pe.HTTPCode, pe.ResinError)
	}
}

func TestMapRouteError_NoAvailableNodes(t *testing.T) {
	pe := mapRouteError(routing.ErrNoAvailableNodes)
	if pe.HTTPCode != 503 || pe.ResinError != "NO_AVAILABLE_NODES" {
		t.Fatalf("expected 503 NO_AVAILABLE_NODES, got %d %s", pe.HTTPCode, pe.ResinError)
	}
}

func TestStripHopByHopHeaders_Comprehensive(t *testing.T) {
	header := http.Header{}
	header.Set("Proxy-Authorization", "Basic xxx")
	header.Set("Proxy-Connection", "keep-alive")
	header.Set("Proxy-Authenticate", `Basic realm="Upstream"`)
	header.Set("Keep-Alive", "timeout=5")
	header.Set("TE", "trailers")
	header.Set("Trailer", "X-Trailer")
	header.Set("Transfer-Encoding", "chunked")
	header.Set("Upgrade", "websocket")
	header.Set("Connection", "X-Custom, X-Other")
	header.Set("X-Custom", "val1")
	header.Set("X-Other", "val2")
	header.Set("X-Keep", "keep-me")

	stripHopByHopHeaders(header)

	removed := []string{
		"Proxy-Authorization", "Proxy-Connection", "Proxy-Authenticate",
		"Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade",
		"Connection", "X-Custom", "X-Other",
	}
	for _, h := range removed {
		if v := header.Get(h); v != "" {
			t.Errorf("header %q should be removed, got %q", h, v)
		}
	}
	if header.Get("X-Keep") != "keep-me" {
		t.Error("X-Keep should remain")
	}
}

func TestCopyEndToEndHeaders_StripsHopByHop(t *testing.T) {
	src := http.Header{}
	src.Set("Connection", "X-Internal")
	src.Set("Proxy-Authenticate", `Basic realm="Upstream"`)
	src.Set("Transfer-Encoding", "chunked")
	src.Set("Trailer", "X-Trailer")
	src.Set("X-Internal", "should-not-forward")
	src.Set("X-Public", "ok")

	dst := http.Header{}
	copyEndToEndHeaders(dst, src)

	for _, h := range []string{
		"Connection", "Proxy-Authenticate", "Transfer-Encoding", "Trailer", "X-Internal",
	} {
		if got := dst.Get(h); got != "" {
			t.Fatalf("header %q should not be copied, got %q", h, got)
		}
	}
	if got := dst.Get("X-Public"); got != "ok" {
		t.Fatalf("expected X-Public to be copied, got %q", got)
	}

	// Source headers should remain intact.
	if got := src.Get("Connection"); got == "" {
		t.Fatal("source headers should not be mutated")
	}
}

func TestStripForwardingIdentityHeaders(t *testing.T) {
	header := http.Header{}
	header.Set("Forwarded", "for=1.2.3.4;proto=https")
	header.Set("X-Forwarded-For", "1.2.3.4")
	header.Set("X-Forwarded-Host", "origin.example.com")
	header.Set("X-Forwarded-Proto", "https")
	header.Set("X-Forwarded-Port", "443")
	header.Set("X-Forwarded-Server", "edge-1")
	header.Set("Via", "1.1 proxy")
	header.Set("X-Real-IP", "1.2.3.4")
	header.Set("X-Client-IP", "1.2.3.4")
	header.Set("True-Client-IP", "1.2.3.4")
	header.Set("CF-Connecting-IP", "1.2.3.4")
	header.Set("X-ProxyUser-Ip", "1.2.3.4")
	header.Set("X-Public", "keep-me")

	stripForwardingIdentityHeaders(header)

	for _, h := range []string{
		"Forwarded", "X-Forwarded-Host", "X-Forwarded-Proto",
		"X-Forwarded-Port", "X-Forwarded-Server", "Via",
		"X-Real-IP", "X-Client-IP", "True-Client-IP",
		"CF-Connecting-IP", "X-ProxyUser-Ip",
	} {
		if got := header.Get(h); got != "" {
			t.Fatalf("header %q should be removed, got %q", h, got)
		}
	}
	if got := header.Get("X-Public"); got != "keep-me" {
		t.Fatalf("expected X-Public to remain, got %q", got)
	}
	vals, ok := header["X-Forwarded-For"]
	if !ok || vals != nil {
		t.Fatalf("X-Forwarded-For should be present with nil value, got ok=%v vals=%v", ok, vals)
	}
}

func TestFullErrorTable(t *testing.T) {
	// Verify all error constants have correct HTTP codes and X-Resin-Error values.
	errorTable := map[*ProxyError]struct {
		code   int
		header string
	}{
		ErrAuthRequired:          {407, "AUTH_REQUIRED"},
		ErrAuthFailed:            {403, "AUTH_FAILED"},
		ErrURLParseError:         {400, "URL_PARSE_ERROR"},
		ErrInvalidProtocol:       {400, "INVALID_PROTOCOL"},
		ErrInvalidHost:           {400, "INVALID_HOST"},
		ErrPlatformNotFound:      {404, "PLATFORM_NOT_FOUND"},
		ErrAccountRejected:       {403, "ACCOUNT_REJECTED"},
		ErrNoAvailableNodes:      {503, "NO_AVAILABLE_NODES"},
		ErrUpstreamConnectFailed: {502, "UPSTREAM_CONNECT_FAILED"},
		ErrUpstreamTimeout:       {504, "UPSTREAM_TIMEOUT"},
		ErrUpstreamRequestFailed: {502, "UPSTREAM_REQUEST_FAILED"},
		ErrInternalError:         {500, "INTERNAL_ERROR"},
	}

	for pe, want := range errorTable {
		if pe.HTTPCode != want.code {
			t.Errorf("%s: HTTPCode=%d, want %d", pe.ResinError, pe.HTTPCode, want.code)
		}
		if pe.ResinError != want.header {
			t.Errorf("ResinError=%q, want %q", pe.ResinError, want.header)
		}
	}
}
