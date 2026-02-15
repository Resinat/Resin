package probe

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"time"

	"github.com/sagernet/sing-box/adapter"
)

// DirectFetcher creates a Fetcher that performs direct HTTP requests (not
// through the node's outbound). This provides a working baseline for the
// probe loop until outbound-aware transport is integrated.
//
// timeout is a closure that returns the current ProbeTimeout from RuntimeConfig.
// The timeout is read per-request, supporting hot-reload.
//
// When outbound transport is available, replace with an OutboundFetcher that
// routes the request through the node's outbound adapter.
func DirectFetcher(timeout func() time.Duration) Fetcher {
	transport := &http.Transport{
		// Disable redirect following for trace endpoint handled below.
	}

	return func(_ adapter.Outbound, url string) ([]byte, time.Duration, error) {
		t := timeout()
		if t <= 0 {
			t = 15 * time.Second // safe default
		}

		ctx, cancel := context.WithTimeout(context.Background(), t)
		defer cancel()

		var tlsStart, tlsDone, firstByte time.Time
		trace := &httptrace.ClientTrace{
			TLSHandshakeStart:    func() { tlsStart = time.Now() },
			TLSHandshakeDone:     func(_ tls.ConnectionState, _ error) { tlsDone = time.Now() },
			GotFirstResponseByte: func() { firstByte = time.Now() },
		}

		req, err := http.NewRequestWithContext(
			httptrace.WithClientTrace(ctx, trace),
			http.MethodGet, url, nil,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("probe: create request: %w", err)
		}

		requestStart := time.Now()
		resp, err := transport.RoundTrip(req)
		if err != nil {
			return nil, 0, fmt.Errorf("probe: do request: %w", err)
		}
		requestDone := time.Now()
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, 0, fmt.Errorf("probe: read body: %w", err)
		}

		// Prefer TLS handshake latency. If there is no handshake event (for
		// example HTTP/plaintext or connection reuse), fall back to request RTT.
		latency := requestDone.Sub(requestStart)
		if !tlsStart.IsZero() && !tlsDone.IsZero() && tlsDone.After(tlsStart) {
			latency = tlsDone.Sub(tlsStart)
		} else if !firstByte.IsZero() && firstByte.After(requestStart) {
			latency = firstByte.Sub(requestStart)
		}
		if latency <= 0 {
			latency = time.Nanosecond
		}

		return body, latency, nil
	}
}
