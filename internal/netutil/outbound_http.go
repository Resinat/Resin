package netutil

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

const defaultOutboundUserAgent = "Resin/1.0"

type ConnLifecycleOp uint8

const (
	ConnLifecycleOpen ConnLifecycleOp = iota
	ConnLifecycleClose
)

// OutboundHTTPOptions controls outbound-backed HTTP execution behavior.
type OutboundHTTPOptions struct {
	// RequireStatusOK enforces HTTP 200 status; otherwise any status is accepted.
	RequireStatusOK bool
	// UserAgent overrides the request User-Agent when non-empty.
	UserAgent string
	// OnConnLifecycle is called with open/close lifecycle events to track connection
	// lifecycle for metrics. Set by probe callers to count outbound connections;
	// left nil for download callers (GeoIP, subscription) to exclude from stats.
	OnConnLifecycle func(op ConnLifecycleOp)
}

// OutboundHTTPResult retains the actual connection's relay on success and failure.
type OutboundHTTPResult struct {
	Body    []byte
	Latency time.Duration
	Relay   node.RelayInfo
}

// HTTPGetViaOutbound executes an HTTP GET through the provided outbound.
// Timeout and cancellation are controlled solely by ctx.
func HTTPGetViaOutbound(
	ctx context.Context,
	outbound adapter.Outbound,
	url string,
	opts OutboundHTTPOptions,
) (result OutboundHTTPResult, err error) {
	if outbound == nil {
		return result, fmt.Errorf("outbound fetch: outbound is nil")
	}
	var relay atomic.Pointer[node.RelayInfo]
	var latency atomic.Int64
	defer func() {
		result.Latency = time.Duration(latency.Load())
		if info := relay.Load(); info != nil {
			result.Relay = *info
			err = info.WrapError(err)
		}
	}()

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := outbound.DialContext(WithoutHTTPTrace(ctx), network, M.ParseSocksaddr(addr))
			if err != nil {
				return nil, err
			}
			if opts.OnConnLifecycle != nil {
				opts.OnConnLifecycle(ConnLifecycleOpen)
				return &connCloseHook{Conn: conn, onClose: func() { opts.OnConnLifecycle(ConnLifecycleClose) }}, nil
			}
			return conn, nil
		},
		DisableKeepAlives: true,
		ForceAttemptHTTP2: true,
	}

	client := &http.Client{
		Transport: transport,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return result, err
	}

	userAgent := opts.UserAgent
	if userAgent == "" {
		userAgent = defaultOutboundUserAgent
	}
	req.Header.Set("User-Agent", userAgent)

	var start atomic.Pointer[time.Time]
	trace := &httptrace.ClientTrace{
		GetConn: func(string) { relay.Store(nil) },
		GotConn: func(conn httptrace.GotConnInfo) {
			info := node.RelayInfoFromConn(conn.Conn)
			relay.Store(&info)
		},
		TLSHandshakeStart: func() { now := time.Now(); start.Store(&now) },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if began := start.Load(); err == nil && began != nil {
				latency.Store(int64(time.Since(*began)))
			}
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(ctx, trace))

	resp, err := client.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()

	if opts.RequireStatusOK && resp.StatusCode != http.StatusOK {
		return result, fmt.Errorf("outbound fetch: unexpected status %d from %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return result, err
	}

	result.Body = body
	return result, nil
}

// connCloseHook wraps a net.Conn and calls onClose exactly once on Close.
type connCloseHook struct {
	net.Conn
	onClose   func()
	closeOnce sync.Once
	closeErr  error
}

func (c *connCloseHook) Upstream() any { return c.Conn }

func (c *connCloseHook) Close() error {
	c.closeOnce.Do(func() {
		if c.onClose != nil {
			c.onClose()
		}
		c.closeErr = c.Conn.Close()
	})
	return c.closeErr
}
