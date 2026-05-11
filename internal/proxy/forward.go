package proxy

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"

	"github.com/Resinat/Resin/internal/config"
	"github.com/Resinat/Resin/internal/netutil"
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/routing"
)

// ForwardProxyConfig holds dependencies for the forward proxy.
type ForwardProxyConfig struct {
	ProxyToken        string
	AuthVersion       string
	Router            *routing.Router
	Pool              outbound.PoolAccessor
	Health            HealthRecorder
	Events            EventEmitter
	MetricsSink       MetricsEventSink
	OutboundTransport OutboundTransportConfig
	TransportPool     *OutboundTransportPool
}

// ForwardProxy implements an HTTP forward proxy with Proxy-Authorization
// authentication, HTTP request forwarding, and CONNECT tunneling.
type ForwardProxy struct {
	token             string
	authVersion       config.AuthVersion
	router            *routing.Router
	pool              outbound.PoolAccessor
	health            HealthRecorder
	events            EventEmitter
	metricsSink       MetricsEventSink
	transportConfig   OutboundTransportConfig
	transportPool     *OutboundTransportPool
	transportPoolOnce sync.Once
}

// NewForwardProxy creates a new forward proxy handler.
func NewForwardProxy(cfg ForwardProxyConfig) *ForwardProxy {
	ev := cfg.Events
	if ev == nil {
		ev = NoOpEventEmitter{}
	}
	transportCfg := normalizeOutboundTransportConfig(cfg.OutboundTransport)
	transportPool := cfg.TransportPool
	if transportPool == nil {
		transportPool = NewOutboundTransportPool(transportCfg)
	}
	authVersion := config.NormalizeAuthVersion(cfg.AuthVersion)
	if authVersion == "" {
		authVersion = config.AuthVersionLegacyV0
	}
	return &ForwardProxy{
		token:           cfg.ProxyToken,
		authVersion:     authVersion,
		router:          cfg.Router,
		pool:            cfg.Pool,
		health:          cfg.Health,
		events:          ev,
		metricsSink:     cfg.MetricsSink,
		transportConfig: transportCfg,
		transportPool:   transportPool,
	}
}

func (p *ForwardProxy) outboundHTTPTransport(routed routedOutbound) *http.Transport {
	p.transportPoolOnce.Do(func() {
		if p.transportPool == nil {
			p.transportPool = NewOutboundTransportPool(p.transportConfig)
		}
	})
	return p.transportPool.Get(routed.Route.NodeHash, routed.Outbound, p.metricsSink)
}

func (p *ForwardProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleCONNECT(w, r)
	} else {
		p.handleHTTP(w, r)
	}
}

func (p *ForwardProxy) effectiveAuthVersion() config.AuthVersion {
	if p == nil {
		return config.AuthVersionLegacyV0
	}
	if p.authVersion == config.AuthVersionV1 {
		return config.AuthVersionV1
	}
	return config.AuthVersionLegacyV0
}

// authenticate parses Proxy-Authorization and returns (platformName, account, error).
func (p *ForwardProxy) authenticate(r *http.Request) (string, string, *ProxyError) {
	if p.effectiveAuthVersion() == config.AuthVersionV1 {
		return p.authenticateV1(r)
	}
	return p.authenticateLegacy(r)
}

func (p *ForwardProxy) authenticateLegacy(r *http.Request) (string, string, *ProxyError) {
	auth := r.Header.Get("Proxy-Authorization")

	// Empty configured proxy token means auth is intentionally disabled.
	// In this mode, Proxy-Authorization is optional; when present and parseable,
	// we still extract Platform:Account identity.
	// Accepted credential formats in Basic payload:
	// 1) "platform:account" (two fields)
	// 2) "token:platform:account" (legacy three-field shape)
	if p.token == "" {
		platName, account, ok := parseProxyAuthorizationIdentityWhenAuthDisabledLegacy(auth)
		if !ok {
			return "", "", nil
		}
		return platName, account, nil
	}

	user, pass, ok := parseProxyAuthorizationLegacy(auth)
	if !ok {
		return "", "", ErrAuthRequired
	}
	if user != p.token {
		return "", "", ErrAuthFailed
	}

	platName, account := parseLegacyPlatformAccountIdentity(pass)
	return platName, account, nil
}

// parseProxyAuthorizationLegacy parses legacy Basic payload:
// "PROXY_TOKEN:Platform:Account".
//
// This parser is intentionally legacy-only and must not be reused by V1 code.
func parseProxyAuthorizationLegacy(auth string) (user string, pass string, ok bool) {
	credential, ok := parseProxyAuthorizationCredentialLegacy(auth)
	if !ok {
		return "", "", false
	}

	// Legacy format: user:pass where user=PROXY_TOKEN, pass=Platform:Account.
	// Split on first ":" to get user and pass.
	colonIdx := strings.IndexByte(credential, ':')
	if colonIdx < 0 {
		return "", "", false
	}
	user = credential[:colonIdx]
	pass = credential[colonIdx+1:]
	return user, pass, true
}

func (p *ForwardProxy) authenticateV1(r *http.Request) (string, string, *ProxyError) {
	auth := r.Header.Get("Proxy-Authorization")
	if p.token == "" {
		credential, ok := parseProxyAuthorizationCredentialV1(auth)
		if !ok {
			return "", "", nil
		}
		platName, account := parseForwardCredentialV1WhenAuthDisabled(credential)
		return platName, account, nil
	}

	credential, ok := parseProxyAuthorizationCredentialV1(auth)
	if !ok {
		return "", "", ErrAuthRequired
	}
	token, platName, account := parseForwardCredentialV1(credential)
	if token != p.token {
		return "", "", ErrAuthFailed
	}
	return platName, account, nil
}

func parseProxyAuthorizationIdentityWhenAuthDisabledLegacy(auth string) (platName string, account string, ok bool) {
	credential, ok := parseProxyAuthorizationCredentialLegacy(auth)
	if !ok {
		return "", "", false
	}
	platName, account = parseLegacyAuthDisabledIdentityCredential(credential)
	return platName, account, true
}

// parseProxyAuthorizationCredentialLegacy decodes Basic credential for
// LEGACY_V0 forward-auth flows.
//
// This function intentionally duplicates V1 decoding logic so legacy and V1
// parsing paths remain structurally isolated for future legacy removal.
func parseProxyAuthorizationCredentialLegacy(auth string) (string, bool) {
	if auth == "" {
		return "", false
	}

	// Expect "<scheme> <base64>"; scheme is case-insensitive per RFC.
	authFields := strings.Fields(auth)
	if len(authFields) != 2 || !strings.EqualFold(authFields[0], "Basic") {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(authFields[1])
	if err != nil {
		return "", false
	}
	return string(decoded), true
}

// parseProxyAuthorizationCredentialV1 decodes Basic credential for V1
// forward-auth flows.
//
// This function intentionally duplicates legacy decoding logic so V1 remains
// independent from LEGACY_V0 parser implementation.
func parseProxyAuthorizationCredentialV1(auth string) (string, bool) {
	if auth == "" {
		return "", false
	}

	// Expect "<scheme> <base64>"; scheme is case-insensitive per RFC.
	authFields := strings.Fields(auth)
	if len(authFields) != 2 || !strings.EqualFold(authFields[0], "Basic") {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(authFields[1])
	if err != nil {
		return "", false
	}
	return string(decoded), true
}

// hop-by-hop headers that must not be forwarded to the next hop.
var hopByHopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"TE",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// stripHopByHopHeaders removes hop-by-hop headers from a header map,
// including any headers listed in the Connection header.
func stripHopByHopHeaders(header http.Header) {
	if header == nil {
		return
	}
	// Remove custom headers listed in Connection.
	for _, connHeaders := range header.Values("Connection") {
		for _, h := range strings.Split(connHeaders, ",") {
			if h = strings.TrimSpace(h); h != "" {
				header.Del(h)
			}
		}
	}
	for _, h := range hopByHopHeaders {
		header.Del(h)
	}
}

// copyEndToEndHeaders copies only end-to-end headers from src to dst and
// returns the canonical wire-format header length after filtering.
func copyEndToEndHeaders(dst, src http.Header) int64 {
	if dst == nil || src == nil {
		return 0
	}
	headers := src.Clone()
	stripHopByHopHeaders(headers)
	totalLen := headerWireLen(headers)
	for k, vv := range headers {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
	return totalLen
}

// prepareForwardOutboundRequest clones an inbound forward-proxy request into a
// client request suitable for http.Transport.RoundTrip.
func prepareForwardOutboundRequest(in *http.Request) *http.Request {
	req := in.Clone(in.Context())
	req.RequestURI = ""
	// Do not propagate client-side close semantics to upstream transport reuse.
	req.Close = false
	stripHopByHopHeaders(req.Header)
	return req
}

func (p *ForwardProxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	platName, account, authErr := p.authenticate(r)
	if authErr != nil {
		writeProxyError(w, authErr)
		return
	}

	lifecycle := newRequestLifecycle(p.events, r, ProxyTypeForward, false)
	lifecycle.setTarget(r.Host, r.URL.String())
	defer lifecycle.finish()
	lifecycle.setAccount(account)

	routed, routeErr := resolveRoutedOutbound(p.router, p.pool, platName, account, r.Host)
	if routeErr != nil {
		lifecycle.setProxyError(routeErr)
		lifecycle.setHTTPStatus(routeErr.HTTPCode)
		writeProxyError(w, routeErr)
		return
	}
	lifecycle.setRouteResult(routed.Route)
	go p.health.RecordLatency(routed.Route.NodeHash, netutil.ExtractDomain(r.Host), nil)

	transport := p.outboundHTTPTransport(routed)
	outReq := prepareForwardOutboundRequest(r)
	upstreamTrace := newUpstreamRequestTrace()
	outReq = outReq.WithContext(httptrace.WithClientTrace(outReq.Context(), upstreamTrace.clientTrace()))
	pendingEgressHeaderBytes := headerWireLen(outReq.Header)
	var egressBodyCounter *countingReadCloser
	if outReq.Body != nil && outReq.Body != http.NoBody {
		egressBodyCounter = newCountingReadCloser(outReq.Body)
		outReq.Body = egressBodyCounter
	}

	// Forward the request.
	resp, err := transport.RoundTrip(outReq)
	if upstreamTrace.shouldCommitEgress() {
		lifecycle.addEgressBytes(pendingEgressHeaderBytes)
		if egressBodyCounter != nil {
			lifecycle.addEgressBytes(egressBodyCounter.Total())
		}
	}
	if err != nil {
		proxyErr := classifyUpstreamError(err)
		if proxyErr == nil {
			// context.Canceled — skip health recording, close silently.
			// Request ended due to client-side cancellation before upstream
			// response; treat as net-ok in request log semantics.
			lifecycle.setNetOK(true)
			return
		}
		lifecycle.setProxyError(proxyErr)
		lifecycle.setUpstreamError("forward_roundtrip", err)
		lifecycle.setHTTPStatus(proxyErr.HTTPCode)
		go p.health.RecordResult(routed.Route.NodeHash, false)
		writeProxyError(w, proxyErr)
		return
	}
	defer resp.Body.Close()

	lifecycle.setHTTPStatus(resp.StatusCode)
	lifecycle.setNetOK(true)

	// Copy end-to-end response headers and body.
	lifecycle.addIngressBytes(copyEndToEndHeaders(w.Header(), resp.Header))
	w.WriteHeader(resp.StatusCode)
	copiedBytes, copyErr := io.Copy(w, resp.Body)
	lifecycle.addIngressBytes(copiedBytes)
	if copyErr != nil {
		if shouldRecordForwardCopyFailure(r, copyErr) {
			lifecycle.setProxyError(ErrUpstreamRequestFailed)
			lifecycle.setUpstreamError("forward_upstream_to_client_copy", copyErr)
			lifecycle.setNetOK(false)
			go p.health.RecordResult(routed.Route.NodeHash, false)
		}
		return
	}

	// Full body transfer succeeded — count as network success even for 5xx HTTP.
	go p.health.RecordResult(routed.Route.NodeHash, true)
}

func (p *ForwardProxy) handleCONNECT(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	platName, account, authErr := p.authenticate(r)
	if authErr != nil {
		writeProxyError(w, authErr)
		return
	}

	lifecycle := newRequestLifecycle(p.events, r, ProxyTypeForward, true)
	lifecycle.setTarget(target, "")
	defer lifecycle.finish()
	lifecycle.setAccount(account)

	prepare := prepareConnectTunnel(
		r.Context(),
		tunnelDeps{
			router:      p.router,
			pool:        p.pool,
			health:      p.health,
			metricsSink: p.metricsSink,
		},
		platName,
		account,
		target,
	)
	if prepare.route.PlatformID != "" {
		lifecycle.setRouteResult(prepare.route)
	}
	if prepare.session == nil {
		if prepare.proxyErr != nil {
			lifecycle.setProxyError(prepare.proxyErr)
			if prepare.upstreamStage != "" {
				lifecycle.setUpstreamError(prepare.upstreamStage, prepare.upstreamErr)
			}
			lifecycle.setHTTPStatus(prepare.proxyErr.HTTPCode)
			writeProxyError(w, prepare.proxyErr)
		} else if prepare.canceled {
			lifecycle.setNetOK(true)
		}
		return
	}

	// Hijack the client connection.
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		prepare.session.upstreamConn.Close()
		lifecycle.setProxyError(ErrUpstreamRequestFailed)
		lifecycle.setUpstreamError("connect_hijack", errors.New("response writer does not support hijacking"))
		lifecycle.setHTTPStatus(ErrUpstreamRequestFailed.HTTPCode)
		prepare.session.recordResult(false)
		writeProxyError(w, ErrUpstreamRequestFailed)
		return
	}

	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		prepare.session.upstreamConn.Close()
		lifecycle.setProxyError(ErrUpstreamRequestFailed)
		lifecycle.setUpstreamError("connect_hijack", err)
		prepare.session.recordResult(false)
		return
	}

	// Write the raw CONNECT success line with proper reason phrase.
	if _, err := clientBuf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		prepare.session.upstreamConn.Close()
		clientConn.Close()
		lifecycle.setProxyError(ErrUpstreamRequestFailed)
		lifecycle.setUpstreamError("connect_client_response_write", err)
		lifecycle.setNetOK(false)
		return
	}
	if err := clientBuf.Flush(); err != nil {
		prepare.session.upstreamConn.Close()
		clientConn.Close()
		lifecycle.setProxyError(ErrUpstreamRequestFailed)
		lifecycle.setUpstreamError("connect_client_response_flush", err)
		lifecycle.setNetOK(false)
		return
	}
	lifecycle.setHTTPStatus(http.StatusOK)
	relay := pumpPreparedTunnel(clientConn, clientBuf.Reader, prepare.session, tunnelPumpOptions{
		requireBidirectionalTraffic: true,
	})
	lifecycle.addIngressBytes(relay.ingressBytes)
	lifecycle.addEgressBytes(relay.egressBytes)
	if relay.proxyErr != nil {
		lifecycle.setProxyError(relay.proxyErr)
		lifecycle.setUpstreamError(relay.upstreamStage, relay.upstreamErr)
	}
	lifecycle.setNetOK(relay.netOK)
	prepare.session.recordResult(relay.netOK)
}

// shouldRecordForwardCopyFailure decides whether an HTTP response body copy
// error should be treated as an upstream/node failure.
func shouldRecordForwardCopyFailure(r *http.Request, copyErr error) bool {
	if copyErr == nil {
		return false
	}
	// Client-side cancellation while streaming should not penalise node health.
	if r != nil && errors.Is(r.Context().Err(), context.Canceled) {
		return false
	}
	return classifyUpstreamError(copyErr) != nil
}
