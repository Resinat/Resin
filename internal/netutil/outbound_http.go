package netutil

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"time"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

const defaultOutboundUserAgent = "Resin/1.0"

// OutboundHTTPOptions controls outbound-backed HTTP execution behavior.
type OutboundHTTPOptions struct {
	// RequireStatusOK enforces HTTP 200 status; otherwise any status is accepted.
	RequireStatusOK bool
	// UserAgent overrides the request User-Agent when non-empty.
	UserAgent string
}

// HTTPGetViaOutbound executes an HTTP GET through the provided outbound.
// Timeout and cancellation are controlled solely by ctx.
func HTTPGetViaOutbound(
	ctx context.Context,
	outbound adapter.Outbound,
	url string,
	opts OutboundHTTPOptions,
) ([]byte, time.Duration, error) {
	if outbound == nil {
		return nil, 0, fmt.Errorf("outbound fetch: outbound is nil")
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return outbound.DialContext(ctx, network, M.ParseSocksaddr(addr))
		},
		DisableKeepAlives: true,
		ForceAttemptHTTP2: true,
	}

	client := &http.Client{
		Transport: transport,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}

	userAgent := opts.UserAgent
	if userAgent == "" {
		userAgent = defaultOutboundUserAgent
	}
	req.Header.Set("User-Agent", userAgent)

	var start time.Time
	var latency time.Duration
	trace := &httptrace.ClientTrace{
		TLSHandshakeStart: func() { start = time.Now() },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err == nil {
				latency = time.Since(start)
			}
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(ctx, trace))

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if opts.RequireStatusOK && resp.StatusCode != http.StatusOK {
		return nil, latency, fmt.Errorf("outbound fetch: unexpected status %d from %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, latency, err
	}

	return body, latency, nil
}
