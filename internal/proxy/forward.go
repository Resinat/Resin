package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Resinat/Resin/internal/netutil"
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/routing"
	M "github.com/sagernet/sing/common/metadata"
)

// ForwardProxyConfig holds dependencies for the forward proxy.
type ForwardProxyConfig struct {
	ProxyToken        string
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
	return &ForwardProxy{
		token:           cfg.ProxyToken,
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

// authenticate parses Proxy-Authorization and returns (platformName, account, error).
func (p *ForwardProxy) authenticate(r *http.Request) (string, string, *ProxyError) {
	auth := r.Header.Get("Proxy-Authorization")

	// Empty configured proxy token means auth is intentionally disabled.
	// In this mode, Proxy-Authorization is optional; when present and parseable,
	// we still extract Platform:Account identity.
	// Accepted credential formats in Basic payload:
	// 1) "platform:account" (two fields)
	// 2) "token:platform:account" (legacy three-field shape)
	if p.token == "" {
		platName, account, ok := parseProxyAuthorizationIdentityWhenAuthDisabled(auth)
		if !ok {
			return "", "", nil
		}
		return platName, account, nil
	}

	user, pass, ok := parseProxyAuthorization(auth)
	if !ok {
		return "", "", ErrAuthRequired
	}
	if user != p.token {
		return "", "", ErrAuthFailed
	}

	platName, account := parsePlatformAccount(pass)
	return platName, account, nil
}

func parseProxyAuthorization(auth string) (user string, pass string, ok bool) {
	credential, ok := parseProxyAuthorizationCredential(auth)
	if !ok {
		return "", "", false
	}

	// Format: user:pass where user=PROXY_TOKEN, pass=Platform:Account
	// Split on first ":" to get user and pass.
	colonIdx := strings.IndexByte(credential, ':')
	if colonIdx < 0 {
		return "", "", false
	}
	user = credential[:colonIdx]
	pass = credential[colonIdx+1:]
	return user, pass, true
}

func parseProxyAuthorizationIdentityWhenAuthDisabled(auth string) (platName string, account string, ok bool) {
	credential, ok := parseProxyAuthorizationCredential(auth)
	if !ok {
		return "", "", false
	}

	// When auth is disabled, allow direct "platform:account" identity.
	if strings.Count(credential, ":") == 1 {
		platName, account = parsePlatformAccount(credential)
		return platName, account, true
	}

	// Backward compatible shape: "token:platform:account" -> parse from pass part.
	colonIdx := strings.IndexByte(credential, ':')
	if colonIdx < 0 {
		return "", "", false
	}
	pass := credential[colonIdx+1:]
	platName, account = parsePlatformAccount(pass)
	return platName, account, true
}

func parseProxyAuthorizationCredential(auth string) (string, bool) {
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
	lifecycle := newRequestLifecycle(p.events, r, ProxyTypeForward, false)
	lifecycle.setTarget(r.Host, r.URL.String())
	defer lifecycle.finish()

	platName, account, authErr := p.authenticate(r)
	if authErr != nil {
		lifecycle.setHTTPStatus(authErr.HTTPCode)
		writeProxyError(w, authErr)
		return
	}
	lifecycle.setAccount(account)

	routed, routeErr := resolveRoutedOutbound(p.router, p.pool, platName, account, r.Host)
	if routeErr != nil {
		lifecycle.setHTTPStatus(routeErr.HTTPCode)
		writeProxyError(w, routeErr)
		return
	}
	lifecycle.setRouteResult(routed.Route)
	go p.health.RecordLatency(routed.Route.NodeHash, netutil.ExtractDomain(r.Host), nil)

	transport := p.outboundHTTPTransport(routed)
	outReq := prepareForwardOutboundRequest(r)
	lifecycle.addEgressBytes(headerWireLen(outReq.Header))
	var egressBodyCounter *countingReadCloser
	if outReq.Body != nil && outReq.Body != http.NoBody {
		egressBodyCounter = newCountingReadCloser(outReq.Body)
		outReq.Body = egressBodyCounter
	}

	// Forward the request.
	resp, err := transport.RoundTrip(outReq)
	if egressBodyCounter != nil {
		lifecycle.addEgressBytes(egressBodyCounter.Total())
	}
	if err != nil {
		proxyErr := classifyUpstreamError(err)
		if proxyErr == nil {
			// context.Canceled — skip health recording, close silently.
			return
		}
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
	lifecycle := newRequestLifecycle(p.events, r, ProxyTypeForward, true)
	lifecycle.setTarget(target, "")
	defer lifecycle.finish()

	platName, account, authErr := p.authenticate(r)
	if authErr != nil {
		lifecycle.setHTTPStatus(authErr.HTTPCode)
		writeProxyError(w, authErr)
		return
	}
	lifecycle.setAccount(account)

	routed, routeErr := resolveRoutedOutbound(p.router, p.pool, platName, account, target)
	if routeErr != nil {
		lifecycle.setHTTPStatus(routeErr.HTTPCode)
		writeProxyError(w, routeErr)
		return
	}
	lifecycle.setRouteResult(routed.Route)

	// Wrap the dialed connection with tlsLatencyConn for passive TLS latency.
	domain := netutil.ExtractDomain(target)
	nodeHashRaw := routed.Route.NodeHash
	go p.health.RecordLatency(nodeHashRaw, domain, nil)

	rawConn, err := routed.Outbound.DialContext(r.Context(), "tcp", M.ParseSocksaddr(target))
	if err != nil {
		proxyErr := classifyConnectError(err)
		if proxyErr == nil {
			return // context.Canceled
		}
		lifecycle.setHTTPStatus(proxyErr.HTTPCode)
		go p.health.RecordResult(nodeHashRaw, false)
		writeProxyError(w, proxyErr)
		return
	}
	recordConnectResult := func(ok bool) {
		lifecycle.setNetOK(ok)
		go p.health.RecordResult(nodeHashRaw, ok)
	}

	// Wrap with counting conn for traffic/connection metrics.
	var upstreamBase net.Conn = rawConn
	if p.metricsSink != nil {
		p.metricsSink.OnConnectionLifecycle(ConnectionOutbound, ConnectionOpen)
		upstreamBase = newCountingConn(rawConn, p.metricsSink)
	}

	// Wrap with TLS latency measurement.
	upstreamConn := newTLSLatencyConn(upstreamBase, func(latency time.Duration) {
		p.health.RecordLatency(nodeHashRaw, domain, &latency)
	})

	// Hijack the client connection.
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		upstreamConn.Close()
		lifecycle.setHTTPStatus(ErrUpstreamRequestFailed.HTTPCode)
		recordConnectResult(false)
		writeProxyError(w, ErrUpstreamRequestFailed)
		return
	}

	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		upstreamConn.Close()
		recordConnectResult(false)
		return
	}

	// Write the raw CONNECT success line with proper reason phrase.
	if _, err := clientBuf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		upstreamConn.Close()
		clientConn.Close()
		recordConnectResult(false)
		return
	}
	if err := clientBuf.Flush(); err != nil {
		upstreamConn.Close()
		clientConn.Close()
		recordConnectResult(false)
		return
	}
	lifecycle.setHTTPStatus(http.StatusOK)

	// net/http may have pre-read bytes beyond the CONNECT request line/headers.
	// Drain those buffered bytes first so tunnel forwarding stays byte-transparent.
	clientToUpstream, err := makeTunnelClientReader(clientConn, clientBuf.Reader)
	if err != nil {
		upstreamConn.Close()
		clientConn.Close()
		recordConnectResult(false)
		return
	}

	// Bidirectional tunnel — no HTTP error responses after this point.
	type copyResult struct {
		n int64
	}
	egressBytesCh := make(chan copyResult, 1)
	go func() {
		defer upstreamConn.Close()
		defer clientConn.Close()
		n, _ := io.Copy(upstreamConn, clientToUpstream)
		egressBytesCh <- copyResult{n: n}
	}()
	ingressBytes, _ := io.Copy(clientConn, upstreamConn)
	lifecycle.addIngressBytes(ingressBytes)
	clientConn.Close()
	upstreamConn.Close()
	egressResult := <-egressBytesCh
	lifecycle.addEgressBytes(egressResult.n)
	recordConnectResult(ingressBytes > 0 && egressResult.n > 0)
}

// makeTunnelClientReader returns a reader for client->upstream copy that
// preserves any bytes already buffered by net/http before Hijack().
func makeTunnelClientReader(clientConn net.Conn, buffered *bufio.Reader) (io.Reader, error) {
	if buffered == nil {
		return clientConn, nil
	}
	n := buffered.Buffered()
	if n == 0 {
		return clientConn, nil
	}
	prefetched := make([]byte, n)
	if _, err := io.ReadFull(buffered, prefetched); err != nil {
		return nil, err
	}
	return io.MultiReader(bytes.NewReader(prefetched), clientConn), nil
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
