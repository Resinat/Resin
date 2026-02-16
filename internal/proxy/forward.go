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
	"time"

	"github.com/resin-proxy/resin/internal/netutil"
	"github.com/resin-proxy/resin/internal/outbound"
	"github.com/resin-proxy/resin/internal/routing"
	M "github.com/sagernet/sing/common/metadata"
)

// ForwardProxyConfig holds dependencies for the forward proxy.
type ForwardProxyConfig struct {
	ProxyToken string
	Router     *routing.Router
	Pool       outbound.PoolAccessor
	Health     HealthRecorder
	Events     EventEmitter
}

// ForwardProxy implements an HTTP forward proxy with Proxy-Authorization
// authentication, HTTP request forwarding, and CONNECT tunneling.
type ForwardProxy struct {
	token  string
	router *routing.Router
	pool   outbound.PoolAccessor
	health HealthRecorder
	events EventEmitter
}

// NewForwardProxy creates a new forward proxy handler.
func NewForwardProxy(cfg ForwardProxyConfig) *ForwardProxy {
	ev := cfg.Events
	if ev == nil {
		ev = NoOpEventEmitter{}
	}
	return &ForwardProxy{
		token:  cfg.ProxyToken,
		router: cfg.Router,
		pool:   cfg.Pool,
		health: cfg.Health,
		events: ev,
	}
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
	if auth == "" {
		return "", "", ErrAuthRequired
	}
	// Expect "<scheme> <base64>"; scheme is case-insensitive per RFC.
	authFields := strings.Fields(auth)
	if len(authFields) != 2 || !strings.EqualFold(authFields[0], "Basic") {
		return "", "", ErrAuthRequired
	}
	decoded, err := base64.StdEncoding.DecodeString(authFields[1])
	if err != nil {
		return "", "", ErrAuthRequired
	}
	// Format: user:pass where user=PROXY_TOKEN, pass=Platform:Account
	// Split on first ":" to get user and pass.
	userPass := string(decoded)
	colonIdx := strings.IndexByte(userPass, ':')
	if colonIdx < 0 {
		return "", "", ErrAuthRequired
	}
	user := userPass[:colonIdx]
	pass := userPass[colonIdx+1:]

	if user != p.token {
		return "", "", ErrAuthFailed
	}

	// Parse pass as Platform:Account (split on first ":").
	platName, account := "", ""
	if idx := strings.IndexByte(pass, ':'); idx >= 0 {
		platName = pass[:idx]
		account = pass[idx+1:]
	} else {
		platName = pass
	}
	return platName, account, nil
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

// copyEndToEndHeaders copies only end-to-end headers from src to dst.
func copyEndToEndHeaders(dst, src http.Header) {
	if dst == nil || src == nil {
		return
	}
	headers := src.Clone()
	stripHopByHopHeaders(headers)
	for k, vv := range headers {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func (p *ForwardProxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	targetHost := r.Host
	targetURL := r.URL.String()
	account := ""
	platformID := ""
	platformName := ""
	nodeHash := ""
	egressIP := ""
	httpStatus := 0
	netOK := false
	defer func() {
		duration := time.Since(startTime)
		go p.events.EmitRequestFinished(RequestFinishedEvent{
			PlatformID: platformID,
			ProxyType:  "forward",
			IsConnect:  false,
			NetOK:      netOK,
			DurationNs: duration.Nanoseconds(),
		})
		go p.events.EmitRequestLog(RequestLogEntry{
			ProxyType:    1,
			ClientIP:     r.RemoteAddr,
			PlatformID:   platformID,
			PlatformName: platformName,
			Account:      account,
			TargetHost:   targetHost,
			TargetURL:    targetURL,
			NodeHash:     nodeHash,
			EgressIP:     egressIP,
			DurationNs:   duration.Nanoseconds(),
			NetOK:        netOK,
			HTTPMethod:   r.Method,
			HTTPStatus:   httpStatus,
		})
	}()
	platName, account, authErr := p.authenticate(r)
	if authErr != nil {
		httpStatus = authErr.HTTPCode
		writeProxyError(w, authErr)
		return
	}

	result, err := p.router.RouteRequest(platName, account, r.Host)
	if err != nil {
		proxyErr := mapRouteError(err)
		httpStatus = proxyErr.HTTPCode
		writeProxyError(w, proxyErr)
		return
	}
	platformID = result.PlatformID
	platformName = result.PlatformName
	nodeHash = result.NodeHash.Hex()
	egressIP = result.EgressIP.String()

	entry, ok := p.pool.GetEntry(result.NodeHash)
	if !ok {
		httpStatus = ErrNoAvailableNodes.HTTPCode
		writeProxyError(w, ErrNoAvailableNodes)
		return
	}

	obPtr := entry.Outbound.Load()
	if obPtr == nil {
		httpStatus = ErrNoAvailableNodes.HTTPCode
		writeProxyError(w, ErrNoAvailableNodes)
		return
	}
	ob := *obPtr

	// Build a per-request transport using the node's outbound dialer.
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return ob.DialContext(ctx, network, M.ParseSocksaddr(addr))
		},
		DisableKeepAlives: true,
	}

	// Strip hop-by-hop headers (including Proxy-Authorization).
	stripHopByHopHeaders(r.Header)

	// Forward the request.
	resp, err := transport.RoundTrip(r)

	if err != nil {
		proxyErr := classifyUpstreamError(err)
		if proxyErr == nil {
			// context.Canceled — skip health recording, close silently.
			return
		}
		httpStatus = proxyErr.HTTPCode
		go p.health.RecordResult(result.NodeHash, false)
		writeProxyError(w, proxyErr)
		return
	}
	defer resp.Body.Close()

	httpStatus = resp.StatusCode
	netOK = true

	// Copy end-to-end response headers and body.
	copyEndToEndHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if _, copyErr := io.Copy(w, resp.Body); copyErr != nil {
		if shouldRecordForwardCopyFailure(r, copyErr) {
			netOK = false
			go p.health.RecordResult(result.NodeHash, false)
		}
		return
	}

	// Full body transfer succeeded — count as network success even for 5xx HTTP.
	go p.health.RecordResult(result.NodeHash, true)
}

func (p *ForwardProxy) handleCONNECT(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	target := r.Host
	account := ""
	platformID := ""
	platformName := ""
	nodeHash := ""
	egressIP := ""
	httpStatus := 0
	netOK := false
	defer func() {
		duration := time.Since(startTime)
		go p.events.EmitRequestFinished(RequestFinishedEvent{
			PlatformID: platformID,
			ProxyType:  "forward",
			IsConnect:  true,
			NetOK:      netOK,
			DurationNs: duration.Nanoseconds(),
		})
		go p.events.EmitRequestLog(RequestLogEntry{
			ProxyType:    1,
			ClientIP:     r.RemoteAddr,
			PlatformID:   platformID,
			PlatformName: platformName,
			Account:      account,
			TargetHost:   target,
			NodeHash:     nodeHash,
			EgressIP:     egressIP,
			DurationNs:   duration.Nanoseconds(),
			NetOK:        netOK,
			HTTPMethod:   "CONNECT",
			HTTPStatus:   httpStatus,
		})
	}()

	platName, account, authErr := p.authenticate(r)
	if authErr != nil {
		httpStatus = authErr.HTTPCode
		writeProxyError(w, authErr)
		return
	}

	result, err := p.router.RouteRequest(platName, account, target)
	if err != nil {
		proxyErr := mapRouteError(err)
		httpStatus = proxyErr.HTTPCode
		writeProxyError(w, proxyErr)
		return
	}
	platformID = result.PlatformID
	platformName = result.PlatformName
	nodeHash = result.NodeHash.Hex()
	egressIP = result.EgressIP.String()

	entry, ok := p.pool.GetEntry(result.NodeHash)
	if !ok {
		httpStatus = ErrNoAvailableNodes.HTTPCode
		writeProxyError(w, ErrNoAvailableNodes)
		return
	}

	obPtr := entry.Outbound.Load()
	if obPtr == nil {
		httpStatus = ErrNoAvailableNodes.HTTPCode
		writeProxyError(w, ErrNoAvailableNodes)
		return
	}
	ob := *obPtr

	// Wrap the dialed connection with tlsLatencyConn for passive TLS latency.
	domain := netutil.ExtractDomain(target)
	nodeHashRaw := result.NodeHash

	rawConn, err := ob.DialContext(r.Context(), "tcp", M.ParseSocksaddr(target))
	if err != nil {
		proxyErr := classifyConnectError(err)
		if proxyErr == nil {
			return // context.Canceled
		}
		httpStatus = proxyErr.HTTPCode
		go p.health.RecordResult(nodeHashRaw, false)
		writeProxyError(w, proxyErr)
		return
	}

	// Dial succeeded — network is healthy.
	netOK = true
	go p.health.RecordResult(nodeHashRaw, true)

	// Wrap with TLS latency measurement.
	upstreamConn := newTLSLatencyConn(rawConn, func(latency time.Duration) {
		p.health.RecordLatency(nodeHashRaw, domain, latency)
	})

	// Hijack the client connection.
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		upstreamConn.Close()
		httpStatus = ErrUpstreamRequestFailed.HTTPCode
		writeProxyError(w, ErrUpstreamRequestFailed)
		return
	}

	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		upstreamConn.Close()
		return
	}

	// Write the raw CONNECT success line with proper reason phrase.
	if _, err := clientBuf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		upstreamConn.Close()
		clientConn.Close()
		return
	}
	if err := clientBuf.Flush(); err != nil {
		upstreamConn.Close()
		clientConn.Close()
		return
	}
	httpStatus = http.StatusOK

	// net/http may have pre-read bytes beyond the CONNECT request line/headers.
	// Drain those buffered bytes first so tunnel forwarding stays byte-transparent.
	clientToUpstream, err := makeTunnelClientReader(clientConn, clientBuf.Reader)
	if err != nil {
		upstreamConn.Close()
		clientConn.Close()
		return
	}

	// Bidirectional tunnel — no HTTP error responses after this point.
	go func() {
		defer upstreamConn.Close()
		defer clientConn.Close()
		io.Copy(upstreamConn, clientToUpstream)
	}()
	io.Copy(clientConn, upstreamConn)
	clientConn.Close()
	upstreamConn.Close()
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
