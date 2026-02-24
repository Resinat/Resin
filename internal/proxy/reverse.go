package proxy

import (
	"io"
	"net/http"
	"net/http/httptrace"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	"github.com/Resinat/Resin/internal/netutil"
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/routing"
)

// PlatformLookup provides read-only access to platforms.
type PlatformLookup interface {
	GetPlatform(id string) (*platform.Platform, bool)
	GetPlatformByName(name string) (*platform.Platform, bool)
}

// ReverseProxyConfig holds dependencies for the reverse proxy.
type ReverseProxyConfig struct {
	ProxyToken        string
	Router            *routing.Router
	Pool              outbound.PoolAccessor
	PlatformLookup    PlatformLookup
	Health            HealthRecorder
	Matcher           AccountRuleMatcher
	Events            EventEmitter
	MetricsSink       MetricsEventSink
	OutboundTransport OutboundTransportConfig
	TransportPool     *OutboundTransportPool
}

// ReverseProxy implements an HTTP reverse proxy.
// Path format: /PROXY_TOKEN/Platform:Account/protocol/host/path?query
type ReverseProxy struct {
	token             string
	router            *routing.Router
	pool              outbound.PoolAccessor
	platLook          PlatformLookup
	health            HealthRecorder
	matcher           AccountRuleMatcher
	events            EventEmitter
	metricsSink       MetricsEventSink
	transportConfig   OutboundTransportConfig
	transportPool     *OutboundTransportPool
	transportPoolOnce sync.Once
}

// NewReverseProxy creates a new reverse proxy handler.
func NewReverseProxy(cfg ReverseProxyConfig) *ReverseProxy {
	ev := cfg.Events
	if ev == nil {
		ev = NoOpEventEmitter{}
	}
	transportCfg := normalizeOutboundTransportConfig(cfg.OutboundTransport)
	transportPool := cfg.TransportPool
	if transportPool == nil {
		transportPool = NewOutboundTransportPool(transportCfg)
	}
	return &ReverseProxy{
		token:           cfg.ProxyToken,
		router:          cfg.Router,
		pool:            cfg.Pool,
		platLook:        cfg.PlatformLookup,
		health:          cfg.Health,
		matcher:         cfg.Matcher,
		events:          ev,
		metricsSink:     cfg.MetricsSink,
		transportConfig: transportCfg,
		transportPool:   transportPool,
	}
}

func (p *ReverseProxy) outboundHTTPTransport(routed routedOutbound) *http.Transport {
	p.transportPoolOnce.Do(func() {
		if p.transportPool == nil {
			p.transportPool = NewOutboundTransportPool(p.transportConfig)
		}
	})
	return p.transportPool.Get(routed.Route.NodeHash, routed.Outbound, p.metricsSink)
}

// parsedPath holds the result of parsing a reverse proxy request path.
type parsedPath struct {
	PlatformName string
	Account      string
	Protocol     string
	Host         string
	// Path preserves the original escaped remaining path after host (may be
	// empty), e.g. "v1/users/team%2Fa/profile".
	Path string
}

// forwardingIdentityHeaders are commonly used to disclose proxy chain identity.
// These are stripped from outbound reverse-proxy requests.
var forwardingIdentityHeaders = []string{
	"Forwarded",
	"X-Forwarded-For",
	"X-Forwarded-Host",
	"X-Forwarded-Proto",
	"X-Forwarded-Port",
	"X-Forwarded-Server",
	"Via",
	"X-Real-IP",
	"X-Client-IP",
	"True-Client-IP",
	"CF-Connecting-IP",
	"X-ProxyUser-Ip",
}

func stripForwardingIdentityHeaders(header http.Header) {
	if header == nil {
		return
	}
	for _, h := range forwardingIdentityHeaders {
		header.Del(h)
	}
	// net/http/httputil.ReverseProxy with Director auto-populates X-Forwarded-For
	// unless the header key exists with a nil value.
	header["X-Forwarded-For"] = nil
}

func decodePathSegment(segment string) (string, *ProxyError) {
	decoded, err := url.PathUnescape(segment)
	if err != nil {
		return "", ErrURLParseError
	}
	return decoded, nil
}

// parsePath parses /PROXY_TOKEN/Platform:Account/protocol/host/path...
//
// rawPath must be the escaped URL path (r.URL.EscapedPath), not r.URL.Path.
// This preserves encoded delimiters like %2F in the trailing path.
func (p *ReverseProxy) parsePath(rawPath string) (*parsedPath, *ProxyError) {
	// Trim leading slash.
	path := strings.TrimPrefix(rawPath, "/")
	if path == "" {
		return nil, ErrAuthFailed
	}

	// Split into segments.
	segments := strings.SplitN(path, "/", 5) // token, plat:acct, protocol, host, rest

	// First segment: token.
	token, perr := decodePathSegment(segments[0])
	if perr != nil {
		return nil, perr
	}
	if p.token != "" && token != p.token {
		return nil, ErrAuthFailed
	}

	// Need at least: token, plat:acct, protocol, host (4 segments).
	if len(segments) < 4 {
		return nil, ErrURLParseError
	}

	// Second segment: Platform:Account (split on first ":").
	identity, perr := decodePathSegment(segments[1])
	if perr != nil {
		return nil, perr
	}
	if !strings.Contains(identity, ":") {
		return nil, ErrURLParseError
	}
	platName, account := parsePlatformAccount(identity)

	// Third segment: protocol.
	protocolSeg, perr := decodePathSegment(segments[2])
	if perr != nil {
		return nil, perr
	}
	protocol := strings.ToLower(protocolSeg)
	if protocol != "http" && protocol != "https" {
		return nil, ErrInvalidProtocol
	}

	// Fourth segment: host.
	host, perr := decodePathSegment(segments[3])
	if perr != nil {
		return nil, perr
	}
	if host == "" {
		return nil, ErrInvalidHost
	}
	// Validate host: must be a valid hostname or host:port.
	if !isValidHost(host) {
		return nil, ErrInvalidHost
	}

	// Remaining path.
	remainingPath := ""
	if len(segments) == 5 {
		remainingPath = segments[4]
	}

	return &parsedPath{
		PlatformName: platName,
		Account:      account,
		Protocol:     protocol,
		Host:         host,
		Path:         remainingPath,
	}, nil
}

func buildReverseTargetURL(parsed *parsedPath, rawQuery string) (*url.URL, *ProxyError) {
	targetURL := parsed.Protocol + "://" + parsed.Host
	if parsed.Path != "" {
		targetURL += "/" + parsed.Path
	}
	if rawQuery != "" {
		targetURL += "?" + rawQuery
	}
	target, err := url.Parse(targetURL)
	if err != nil {
		return nil, ErrInvalidHost
	}
	return target, nil
}

func (p *ReverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	detailCfg := reverseDetailCaptureConfig{
		Enabled:             false,
		ReqHeadersMaxBytes:  -1,
		ReqBodyMaxBytes:     -1,
		RespHeadersMaxBytes: -1,
		RespBodyMaxBytes:    -1,
	}
	if provider, ok := p.events.(interface {
		reverseDetailCaptureConfig() reverseDetailCaptureConfig
	}); ok {
		detailCfg = provider.reverseDetailCaptureConfig()
	}

	parsed, perr := p.parsePath(r.URL.EscapedPath())
	if perr != nil {
		writeProxyError(w, perr)
		return
	}

	lifecycle := newRequestLifecycle(p.events, r, ProxyTypeReverse, false)
	lifecycle.setTarget(parsed.Host, "")
	var egressBodyCounter *countingReadCloser
	var ingressBodyCounter *countingReadCloser
	var upgradedStreamCounter *countingReadWriteCloser
	if detailCfg.Enabled {
		reqHeaders, reqHeadersLen, reqHeadersTruncated := captureHeadersWithLimit(r.Header, detailCfg.ReqHeadersMaxBytes)
		lifecycle.setReqHeadersCaptured(reqHeaders, reqHeadersLen, reqHeadersTruncated)
	}
	if r.Body != nil && r.Body != http.NoBody {
		body := r.Body
		if detailCfg.Enabled {
			reqBodyCapture := newPayloadCaptureReadCloser(body, detailCfg.ReqBodyMaxBytes)
			body = reqBodyCapture
			lifecycle.setReqBodyCapture(reqBodyCapture)
		}
		egressBodyCounter = newCountingReadCloser(body)
		r.Body = egressBodyCounter
	}
	defer lifecycle.finish()

	// Resolve account from headers if not in path.
	account := parsed.Account
	if account == "" && p.matcher != nil {
		headers := p.matcher.Match(parsed.Host, parsed.Path)
		if headers != nil {
			account = extractAccountFromHeaders(r, headers)
		}
	}
	lifecycle.setAccount(account)

	// Check miss action if account still empty.
	// When PlatformName is empty, the router resolves to the default platform.
	// We must look up the *resolved* platform (possibly the default) for REJECT.
	if account == "" {
		var plat *platform.Platform
		if parsed.PlatformName != "" {
			p, ok := p.platLook.GetPlatformByName(parsed.PlatformName)
			if ok {
				plat = p
			}
		} else {
			// Empty PlatformName → router will use default platform.
			// Look up default platform for miss-action check.
			plat = p.resolveDefaultPlatform()
		}
		if plat != nil && plat.ReverseProxyMissAction == string(platform.ReverseProxyMissActionReject) {
			lifecycle.setProxyError(ErrAccountRejected)
			lifecycle.setHTTPStatus(ErrAccountRejected.HTTPCode)
			writeProxyError(w, ErrAccountRejected)
			return
		}
		// RANDOM or no platform found: proceed with empty account → random routing.
	}

	routed, routeErr := resolveRoutedOutbound(p.router, p.pool, parsed.PlatformName, account, parsed.Host)
	if routeErr != nil {
		lifecycle.setProxyError(routeErr)
		lifecycle.setHTTPStatus(routeErr.HTTPCode)
		writeProxyError(w, routeErr)
		return
	}
	lifecycle.setRouteResult(routed.Route)

	nodeHashRaw := routed.Route.NodeHash
	domain := netutil.ExtractDomain(parsed.Host)
	go p.health.RecordLatency(nodeHashRaw, domain, nil)

	target, targetErr := buildReverseTargetURL(parsed, r.URL.RawQuery)
	if targetErr != nil {
		lifecycle.setProxyError(targetErr)
		lifecycle.setHTTPStatus(targetErr.HTTPCode)
		writeProxyError(w, targetErr)
		return
	}
	lifecycle.setTarget(parsed.Host, target.String())

	transport := p.outboundHTTPTransport(routed)

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL = target
			req.Host = parsed.Host
			stripForwardingIdentityHeaders(req.Header)
			lifecycle.addEgressBytes(headerWireLen(req.Header))

			// Add httptrace for TLS latency measurement on HTTPS.
			if parsed.Protocol == "https" {
				reporter := newReverseLatencyReporter(p.health, nodeHashRaw, domain)
				reqCtx := httptrace.WithClientTrace(req.Context(), reporter.clientTrace())
				*req = *req.WithContext(reqCtx)
			}
		},
		Transport: transport,
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			proxyErr := classifyUpstreamError(err)
			if proxyErr == nil {
				// context.Canceled — no health recording, silently close.
				// Treat as net-ok for request-log semantics when canceled
				// before upstream response.
				lifecycle.setNetOK(true)
				return
			}
			lifecycle.setProxyError(proxyErr)
			lifecycle.setUpstreamError("reverse_roundtrip", err)
			lifecycle.setNetOK(false)
			lifecycle.setHTTPStatus(proxyErr.HTTPCode)
			go p.health.RecordResult(nodeHashRaw, false)
			writeProxyError(rw, proxyErr)
		},
		ModifyResponse: func(resp *http.Response) error {
			lifecycle.setHTTPStatus(resp.StatusCode)
			lifecycle.addIngressBytes(headerWireLen(resp.Header))
			if resp.StatusCode == http.StatusSwitchingProtocols {
				// 101 upgrade responses require a writable backend body
				// (io.ReadWriteCloser). Do not wrap resp.Body here; wrapping with a
				// read-only wrapper breaks websocket/h2c upgrade tunneling in
				// net/http/httputil.ReverseProxy.
				//
				// We can still account upgrade-session traffic by wrapping with a
				// read-write counter that preserves io.ReadWriteCloser semantics.
				if rwc, ok := resp.Body.(io.ReadWriteCloser); ok {
					upgradedStreamCounter = newCountingReadWriteCloser(rwc)
					resp.Body = upgradedStreamCounter
				}
				if detailCfg.Enabled {
					respHeaders, respHeadersLen, respHeadersTruncated := captureHeadersWithLimit(resp.Header, detailCfg.RespHeadersMaxBytes)
					lifecycle.setRespHeadersCaptured(respHeaders, respHeadersLen, respHeadersTruncated)
				}
				lifecycle.setNetOK(true)
				go p.health.RecordResult(nodeHashRaw, true)
				return nil
			}
			if resp.Body != nil && resp.Body != http.NoBody {
				body := resp.Body
				if detailCfg.Enabled {
					respHeaders, respHeadersLen, respHeadersTruncated := captureHeadersWithLimit(resp.Header, detailCfg.RespHeadersMaxBytes)
					lifecycle.setRespHeadersCaptured(respHeaders, respHeadersLen, respHeadersTruncated)
					respBodyCapture := newPayloadCaptureReadCloser(body, detailCfg.RespBodyMaxBytes)
					body = respBodyCapture
					lifecycle.setRespBodyCapture(respBodyCapture)
				}
				ingressBodyCounter = newCountingReadCloser(body)
				resp.Body = ingressBodyCounter
			} else if detailCfg.Enabled {
				respHeaders, respHeadersLen, respHeadersTruncated := captureHeadersWithLimit(resp.Header, detailCfg.RespHeadersMaxBytes)
				lifecycle.setRespHeadersCaptured(respHeaders, respHeadersLen, respHeadersTruncated)
			}
			// Intentional coarse-grained policy:
			// mark node success once upstream response headers arrive.
			// Further attribution for mid-body stream failures is expensive and noisy
			// (client abort vs upstream reset vs network blip), and the added
			// complexity is not worth it for the current phase.
			lifecycle.setNetOK(true)
			go p.health.RecordResult(nodeHashRaw, true)
			return nil
		},
	}

	proxy.ServeHTTP(w, r)
	if egressBodyCounter != nil {
		lifecycle.addEgressBytes(egressBodyCounter.Total())
	}
	if ingressBodyCounter != nil {
		lifecycle.addIngressBytes(ingressBodyCounter.Total())
	}
	if upgradedStreamCounter != nil {
		lifecycle.addIngressBytes(upgradedStreamCounter.TotalRead())
		lifecycle.addEgressBytes(upgradedStreamCounter.TotalWrite())
	}
}

// resolveDefaultPlatform looks up the default platform for REJECT/RANDOM
// miss-action checks when PlatformName is empty.
func (p *ReverseProxy) resolveDefaultPlatform() *platform.Platform {
	if plat, ok := p.platLook.GetPlatform(platform.DefaultPlatformID); ok {
		return plat
	}
	return nil
}

// isValidHost validates that the host segment is a reasonable hostname or host:port.
// Rejects empty hosts and hosts containing URL-unsafe characters.
func isValidHost(host string) bool {
	if host == "" {
		return false
	}
	// Reject hosts with obviously invalid characters and userinfo marker.
	if strings.ContainsAny(host, "/ \t\n\r@") {
		return false
	}
	// Unbracketed multi-colon literals are ambiguous in URL host syntax.
	// Require bracket form for IPv6 when used in host[:port].
	if strings.Count(host, ":") > 1 && !strings.HasPrefix(host, "[") {
		return false
	}

	u, err := url.Parse("http://" + host)
	if err != nil {
		return false
	}
	// Host segment must be a plain host[:port] without userinfo/path/query.
	if u.User != nil || u.Host == "" || u.Host != host {
		return false
	}
	if u.Hostname() == "" {
		return false
	}
	return true
}
