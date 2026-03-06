package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	"github.com/Resinat/Resin/internal/config"
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
	AuthVersion       string
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
// Identity segment format depends on auth version:
// V1: /PROXY_TOKEN/Platform.Account/protocol/host/path?query
// LEGACY_V0: /PROXY_TOKEN/Platform:Account/protocol/host/path?query
type ReverseProxy struct {
	token             string
	authVersion       config.AuthVersion
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
	authVersion := config.NormalizeAuthVersion(cfg.AuthVersion)
	if authVersion == "" {
		authVersion = config.AuthVersionLegacyV0
	}
	return &ReverseProxy{
		token:           cfg.ProxyToken,
		authVersion:     authVersion,
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

func (p *ReverseProxy) effectiveAuthVersion() config.AuthVersion {
	if p == nil {
		return config.AuthVersionLegacyV0
	}
	if p.authVersion == config.AuthVersionV1 {
		return config.AuthVersionV1
	}
	return config.AuthVersionLegacyV0
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
	// Internal account override header must not leak to upstream services.
	"X-Resin-Account",
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

// decodePathSegmentV1 decodes one escaped URL path segment for V1 parsing.
//
// This function intentionally duplicates legacy decoding logic to keep V1 and
// LEGACY_V0 parsing paths structurally independent.
func decodePathSegmentV1(segment string) (string, *ProxyError) {
	decoded, err := url.PathUnescape(segment)
	if err != nil {
		return "", ErrURLParseError
	}
	return decoded, nil
}

// decodePathSegmentLegacy decodes one escaped URL path segment for LEGACY_V0
// parsing.
//
// This function intentionally duplicates V1 decoding logic so legacy code can
// be removed without touching V1 parser implementations.
func decodePathSegmentLegacy(segment string) (string, *ProxyError) {
	decoded, err := url.PathUnescape(segment)
	if err != nil {
		return "", ErrURLParseError
	}
	return decoded, nil
}

// parsePath dispatches to a version-specific parser.
//
// New and legacy parsers intentionally remain isolated (including duplicated
// parsing steps) so LEGACY_V0 can be removed cleanly without touching V1 code.
func (p *ReverseProxy) parsePath(rawPath string) (*parsedPath, *ProxyError) {
	if p.effectiveAuthVersion() == config.AuthVersionV1 {
		return p.parsePathV1(rawPath)
	}
	return p.parsePathLegacy(rawPath)
}

// parsePathV1 parses reverse proxy path using V1 identity rules.
//
// rawPath must be r.URL.EscapedPath (not r.URL.Path) to preserve escaped
// delimiters in trailing path segments.
func (p *ReverseProxy) parsePathV1(rawPath string) (*parsedPath, *ProxyError) {
	path := strings.TrimPrefix(rawPath, "/")
	if path == "" {
		return nil, ErrAuthFailed
	}

	segments := strings.SplitN(path, "/", 5) // token, identity, protocol, host, rest
	token, perr := decodePathSegmentV1(segments[0])
	if perr != nil {
		return nil, perr
	}
	if p.token != "" && token != p.token {
		return nil, ErrAuthFailed
	}
	if len(segments) < 4 {
		return nil, ErrURLParseError
	}

	identity, perr := decodePathSegmentV1(segments[1])
	if perr != nil {
		return nil, perr
	}
	platName, account := parseV1PlatformAccountIdentity(identity)

	protocolSeg, perr := decodePathSegmentV1(segments[2])
	if perr != nil {
		return nil, perr
	}
	protocol := strings.ToLower(protocolSeg)
	if protocol != "http" && protocol != "https" {
		return nil, ErrInvalidProtocol
	}

	host, perr := decodePathSegmentV1(segments[3])
	if perr != nil {
		return nil, perr
	}
	if host == "" {
		return nil, ErrInvalidHost
	}
	if !isValidHost(host) {
		return nil, ErrInvalidHost
	}

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

// parsePathLegacy parses reverse proxy path using LEGACY_V0 identity rules.
//
// This parser is intentionally independent from V1 parser code, including
// repeated parsing steps, to keep legacy removal low-risk.
func (p *ReverseProxy) parsePathLegacy(rawPath string) (*parsedPath, *ProxyError) {
	path := strings.TrimPrefix(rawPath, "/")
	if path == "" {
		return nil, ErrAuthFailed
	}

	segments := strings.SplitN(path, "/", 5) // token, identity, protocol, host, rest
	token, perr := decodePathSegmentLegacy(segments[0])
	if perr != nil {
		return nil, perr
	}
	if p.token != "" && token != p.token {
		return nil, ErrAuthFailed
	}
	if len(segments) < 4 {
		return nil, ErrURLParseError
	}

	identity, perr := decodePathSegmentLegacy(segments[1])
	if perr != nil {
		return nil, perr
	}
	if !strings.Contains(identity, ":") {
		return nil, ErrURLParseError
	}
	platName, account := parseLegacyPlatformAccountIdentity(identity)

	protocolSeg, perr := decodePathSegmentLegacy(segments[2])
	if perr != nil {
		return nil, perr
	}
	protocol := strings.ToLower(protocolSeg)
	if protocol != "http" && protocol != "https" {
		return nil, ErrInvalidProtocol
	}

	host, perr := decodePathSegmentLegacy(segments[3])
	if perr != nil {
		return nil, perr
	}
	if host == "" {
		return nil, ErrInvalidHost
	}
	if !isValidHost(host) {
		return nil, ErrInvalidHost
	}

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
	var finalAttempt int
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
	defer func() {
		lifecycle.setRetryAttempts(finalAttempt)
		lifecycle.finish()
	}()

	// Resolve account in three phases:
	// 1) Use path account directly when present.
	// 2) If extraction fails, apply miss-action (REJECT or treat-as-empty).
	// 3) Continue routing with the resulting account (possibly empty).
	behaviorPlatform := p.resolvePlatformForAccountBehavior(parsed.PlatformName)
	account, _, extractionFailed := p.resolveReverseProxyAccount(parsed, r, behaviorPlatform)
	lifecycle.setAccount(account)

	if shouldRejectReverseProxyAccountExtractionFailure(extractionFailed, behaviorPlatform) {
		lifecycle.setProxyError(ErrAccountRejected)
		lifecycle.setHTTPStatus(ErrAccountRejected.HTTPCode)
		writeProxyError(w, ErrAccountRejected)
		return
	}

	target, targetErr := buildReverseTargetURL(parsed, r.URL.RawQuery)
	if targetErr != nil {
		lifecycle.setProxyError(targetErr)
		lifecycle.setHTTPStatus(targetErr.HTTPCode)
		writeProxyError(w, targetErr)
		return
	}
	lifecycle.setTarget(parsed.Host, target.String())

	maxRetries := platformMaxRetries(p.platLook, parsed.PlatformName)

	// Buffer request body for potential retry.
	var bodyBytes []byte
	if maxRetries > 0 && r.Body != nil && r.Body != http.NoBody {
		raw, readErr := io.ReadAll(r.Body)
		r.Body.Close()
		if readErr != nil {
			lifecycle.setProxyError(ErrInternalError)
			lifecycle.setHTTPStatus(ErrInternalError.HTTPCode)
			writeProxyError(w, ErrInternalError)
			return
		}
		bodyBytes = raw
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		r.ContentLength = int64(len(bodyBytes))
	}

	domain := netutil.ExtractDomain(parsed.Host)

	for attempt := 0; ; attempt++ {
		finalAttempt = attempt
		routed, routeErr := resolveRoutedOutbound(p.router, p.pool, parsed.PlatformName, account, parsed.Host)
		if routeErr != nil {
			lifecycle.setProxyError(routeErr)
			lifecycle.setHTTPStatus(routeErr.HTTPCode)
			writeProxyError(w, routeErr)
			return
		}
		lifecycle.setRouteResult(routed.Route)

		nodeHashRaw := routed.Route.NodeHash
		go p.health.RecordLatency(nodeHashRaw, domain, nil)

		transport := p.outboundHTTPTransport(routed)

		var retryNeeded bool
		proxy := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				req.URL = target
				req.Host = parsed.Host
				stripForwardingIdentityHeaders(req.Header)
				lifecycle.addEgressBytes(headerWireLen(req.Header))

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
					lifecycle.setNetOK(true)
					return
				}
				go p.health.RecordResult(nodeHashRaw, false)

				if attempt < maxRetries && isRetryableUpstreamError(proxyErr) {
					detail := summarizeUpstreamError(err)
					lifecycle.addRetryDetail(routed.Route.NodeHash.Hex(), routed.Route.NodeTag, detail.Kind, detail.Message)
					retryNeeded = true
					return
				}

				lifecycle.setProxyError(proxyErr)
				lifecycle.setUpstreamError("reverse_roundtrip", err)
				lifecycle.setNetOK(false)
				lifecycle.setHTTPStatus(proxyErr.HTTPCode)
				writeProxyError(rw, proxyErr)
			},
			ModifyResponse: func(resp *http.Response) error {
				lifecycle.setHTTPStatus(resp.StatusCode)
				lifecycle.addIngressBytes(headerWireLen(resp.Header))
				if resp.StatusCode == http.StatusSwitchingProtocols {
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
				lifecycle.setNetOK(true)
				go p.health.RecordResult(nodeHashRaw, true)
				return nil
			},
		}

		proxy.ServeHTTP(w, r)

		if !retryNeeded {
			break
		}
		// Reset request body for next attempt.
		if bodyBytes != nil {
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			r.ContentLength = int64(len(bodyBytes))
		}
	}
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

// resolveDefaultPlatform looks up the default platform for reverse-proxy
// miss-action checks when PlatformName is empty.
func (p *ReverseProxy) resolveDefaultPlatform() *platform.Platform {
	if plat, ok := p.platLook.GetPlatform(platform.DefaultPlatformID); ok {
		return plat
	}
	return nil
}

func (p *ReverseProxy) resolvePlatformForAccountBehavior(platformName string) *platform.Platform {
	if p == nil || p.platLook == nil {
		return nil
	}
	if platformName != "" {
		if plat, ok := p.platLook.GetPlatformByName(platformName); ok {
			return plat
		}
		return nil
	}
	return p.resolveDefaultPlatform()
}

func effectiveEmptyAccountBehavior(plat *platform.Platform) platform.ReverseProxyEmptyAccountBehavior {
	if plat == nil {
		return platform.ReverseProxyEmptyAccountBehaviorRandom
	}
	behavior := platform.ReverseProxyEmptyAccountBehavior(plat.ReverseProxyEmptyAccountBehavior)
	if behavior.IsValid() {
		return behavior
	}
	return platform.ReverseProxyEmptyAccountBehaviorRandom
}

func (p *ReverseProxy) resolveReverseProxyAccount(
	parsed *parsedPath,
	r *http.Request,
	plat *platform.Platform,
) (string, platform.ReverseProxyEmptyAccountBehavior, bool) {
	behavior := effectiveEmptyAccountBehavior(plat)
	if r != nil {
		if headerAccount := r.Header.Get("X-Resin-Account"); headerAccount != "" {
			return headerAccount, behavior, false
		}
	}

	account := ""
	if parsed != nil {
		account = parsed.Account
	}
	if account != "" {
		return account, behavior, false
	}
	if r == nil {
		return account, behavior, behaviorRequiresAccountExtraction(behavior)
	}

	switch behavior {
	case platform.ReverseProxyEmptyAccountBehaviorRandom:
		return account, behavior, false
	case platform.ReverseProxyEmptyAccountBehaviorFixedHeader:
		headers := fixedAccountHeadersForPlatform(plat)
		if len(headers) == 0 {
			return account, behavior, true
		}
		account = extractAccountFromHeaders(r, headers)
		return account, behavior, account == ""
	case platform.ReverseProxyEmptyAccountBehaviorAccountHeaderRule:
		if p == nil || p.matcher == nil || parsed == nil {
			return account, behavior, true
		}
		headers := p.matcher.Match(parsed.Host, parsed.Path)
		if len(headers) == 0 {
			return account, behavior, true
		}
		account = extractAccountFromHeaders(r, headers)
		return account, behavior, account == ""
	}
	return account, behavior, false
}

func behaviorRequiresAccountExtraction(behavior platform.ReverseProxyEmptyAccountBehavior) bool {
	switch behavior {
	case platform.ReverseProxyEmptyAccountBehaviorFixedHeader, platform.ReverseProxyEmptyAccountBehaviorAccountHeaderRule:
		return true
	default:
		return false
	}
}

func shouldRejectReverseProxyAccountExtractionFailure(
	extractionFailed bool,
	plat *platform.Platform,
) bool {
	if !extractionFailed || plat == nil {
		return false
	}
	return platform.NormalizeReverseProxyMissAction(plat.ReverseProxyMissAction) == platform.ReverseProxyMissActionReject
}

func fixedAccountHeadersForPlatform(plat *platform.Platform) []string {
	if plat == nil {
		return nil
	}
	if len(plat.ReverseProxyFixedAccountHeaders) > 0 {
		return append([]string(nil), plat.ReverseProxyFixedAccountHeaders...)
	}
	if plat.ReverseProxyFixedAccountHeader == "" {
		return nil
	}
	_, headers, err := platform.NormalizeFixedAccountHeaders(plat.ReverseProxyFixedAccountHeader)
	if err != nil {
		return nil
	}
	return headers
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
