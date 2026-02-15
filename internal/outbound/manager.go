package outbound

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"time"

	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/probe"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

var ErrOutboundNotReady = errors.New("outbound not ready")

const defaultUserAgent = "Resin/1.0"

// PoolAccessor provides read-only access to the node pool.
type PoolAccessor interface {
	GetEntry(hash node.Hash) (*node.NodeEntry, bool)
	RangeNodes(fn func(node.Hash, *node.NodeEntry) bool)
}

// closeOutbound closes an outbound if it implements io.Closer.
func closeOutbound(ob adapter.Outbound) {
	if c, ok := ob.(io.Closer); ok {
		_ = c.Close()
	}
}

// OutboundManager manages outbound lifecycle and provides unified HTTP execution.
type OutboundManager struct {
	pool    PoolAccessor
	builder OutboundBuilder
}

func NewOutboundManager(pool PoolAccessor, builder OutboundBuilder) *OutboundManager {
	return &OutboundManager{pool: pool, builder: builder}
}

func (m *OutboundManager) isLiveEntry(hash node.Hash, entry *node.NodeEntry) bool {
	current, ok := m.pool.GetEntry(hash)
	return ok && current == entry
}

// EnsureNodeOutbound idempotently creates and stores an outbound for a node.
// Uses CompareAndSwap(nil, &wrapped) to guarantee only one goroutine's build
// result is stored. Losers discard their result (stage 6 adds io.Closer release).
func (m *OutboundManager) EnsureNodeOutbound(hash node.Hash) {
	entry, ok := m.pool.GetEntry(hash)
	if !ok {
		return
	}
	// Fast path: already has outbound.
	if entry.Outbound.Load() != nil {
		return
	}

	ob, err := m.builder.Build(entry.RawOptions)
	if err != nil {
		entry.SetLastError("outbound build: " + err.Error())
		return
	}

	// Build can race with node deletion/replacement. If this entry is no longer
	// the pool's live value for the hash, discard the build result.
	if !m.isLiveEntry(hash, entry) {
		closeOutbound(ob)
		return
	}

	if !entry.Outbound.CompareAndSwap(nil, &ob) {
		// Another goroutine won the race. Close the losing build result.
		closeOutbound(ob)
		return
	}

	// Close-and-clear if the node disappeared/replaced right after CAS.
	if !m.isLiveEntry(hash, entry) {
		old := entry.Outbound.Swap(nil)
		if old != nil {
			closeOutbound(*old)
		}
	}
}

// RemoveNodeOutbound clears a node's outbound reference.
// Accepts the entry directly because the node may already be deleted from the pool
// (RemoveNodeFromSub deletes before firing onNodeRemoved callback).
func (m *OutboundManager) RemoveNodeOutbound(entry *node.NodeEntry) {
	if entry == nil {
		return
	}
	old := entry.Outbound.Swap(nil)
	if old != nil {
		closeOutbound(*old)
	}
}

// WarmupAll iterates all nodes in the pool and ensures each has an outbound.
// Called once after bootstrap to avoid ErrOutboundNotReady on restart.
func (m *OutboundManager) WarmupAll() {
	m.pool.RangeNodes(func(h node.Hash, _ *node.NodeEntry) bool {
		m.EnsureNodeOutbound(h)
		return true
	})
}

// Fetch executes HTTP request using the node's outbound.
// Returns ErrOutboundNotReady if the node's outbound is not yet initialized.
// ctx controls timeout/cancellation.
func (m *OutboundManager) Fetch(ctx context.Context, hash node.Hash, url string) ([]byte, time.Duration, error) {
	return m.FetchWithUserAgent(ctx, hash, url, "")
}

// FetchWithUserAgent executes HTTP request using the node's outbound and
// applies the given User-Agent if non-empty.
func (m *OutboundManager) FetchWithUserAgent(
	ctx context.Context,
	hash node.Hash,
	url string,
	userAgent string,
) ([]byte, time.Duration, error) {
	entry, ok := m.pool.GetEntry(hash)
	if !ok {
		return nil, 0, errors.New("node not found")
	}
	outboundPtr := entry.Outbound.Load() // *adapter.Outbound
	if outboundPtr == nil {
		return nil, 0, ErrOutboundNotReady
	}
	return m.execHTTP(ctx, *outboundPtr, url, true, userAgent)
}

// AsFetcher returns a closure adapting to probe.Fetcher signature.
// timeout is read per-request, supporting hot-reload from RuntimeConfig.
func (m *OutboundManager) AsFetcher(timeout func() time.Duration) probe.Fetcher {
	return func(outbound adapter.Outbound, url string) ([]byte, time.Duration, error) {
		t := timeout()
		if t <= 0 {
			t = 15 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), t)
		defer cancel()
		// Probe fetch accepts non-200 statuses (for example, generate_204).
		return m.execHTTP(ctx, outbound, url, false, "")
	}
}

// execHTTP performs the actual HTTP request through the outbound adapter.
// Timeout is solely controlled by ctx (no hardcoded client.Timeout).
// If requireStatusOK is true, non-200 responses are treated as errors.
func (m *OutboundManager) execHTTP(
	ctx context.Context,
	outbound adapter.Outbound,
	url string,
	requireStatusOK bool,
	userAgent string,
) ([]byte, time.Duration, error) {
	if outbound == nil {
		return nil, 0, ErrOutboundNotReady
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
		// No client.Timeout — deadline comes from ctx.
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, 0, err
	}
	if userAgent == "" {
		userAgent = defaultUserAgent
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

	if requireStatusOK && resp.StatusCode != http.StatusOK {
		return nil, latency, fmt.Errorf("outbound fetch: unexpected status %d from %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, latency, err
	}

	return body, latency, nil
}
