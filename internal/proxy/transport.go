package proxy

import (
	"context"
	"net"
	"net/http"
	"net/http/httptrace"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/puzpuzpuz/xsync/v4"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

type OutboundTransportConfig struct {
	MaxIdleConns        int
	MaxIdleConnsPerHost int
	IdleConnTimeout     time.Duration
}

const (
	defaultTransportMaxIdleConns        = 1024
	defaultTransportMaxIdleConnsPerHost = 64
	defaultTransportIdleConnTimeout     = 90 * time.Second
)

func normalizeOutboundTransportConfig(cfg OutboundTransportConfig) OutboundTransportConfig {
	if cfg.MaxIdleConns <= 0 {
		cfg.MaxIdleConns = defaultTransportMaxIdleConns
	}
	if cfg.MaxIdleConnsPerHost <= 0 {
		cfg.MaxIdleConnsPerHost = defaultTransportMaxIdleConnsPerHost
	}
	if cfg.IdleConnTimeout <= 0 {
		cfg.IdleConnTimeout = defaultTransportIdleConnTimeout
	}
	return cfg
}

// OutboundTransportPool manages reusable outbound HTTP transports keyed by node hash.
// A single instance should be shared by forward/reverse proxies so keep-alive pools
// are reused and can be evicted on node removal.
type OutboundTransportPool struct {
	config     OutboundTransportConfig
	transports *xsync.Map[node.Hash, outboundTransportEntry]
}

type outboundTransportEntry struct {
	version   uint64
	transport *http.Transport
}

// Protocols may make their own HTTP requests while dialing. Keep their trace
// callbacks separate from the outer request while preserving other context data.
type outboundDialContext struct {
	context.Context
}

func (c outboundDialContext) Value(key any) any {
	value := c.Context.Value(key)
	if _, ok := value.(*httptrace.ClientTrace); ok {
		return nil
	}
	return value
}

func newOutboundTransportPool() *OutboundTransportPool {
	return NewOutboundTransportPool(OutboundTransportConfig{})
}

func newOutboundTransportPoolWithConfig(cfg OutboundTransportConfig) *OutboundTransportPool {
	return NewOutboundTransportPool(cfg)
}

// NewOutboundTransportPool creates a transport pool with normalized settings.
func NewOutboundTransportPool(cfg OutboundTransportConfig) *OutboundTransportPool {
	return &OutboundTransportPool{
		config:     normalizeOutboundTransportConfig(cfg),
		transports: xsync.NewMap[node.Hash, outboundTransportEntry](),
	}
}

// Get returns a reusable transport for the given node hash.
func (p *OutboundTransportPool) Get(
	hash node.Hash,
	ob adapter.Outbound,
	sink MetricsEventSink,
) (*http.Transport, *ProxyError) {
	current, cached := p.transports.Load(hash)
	version := outboundRoutingVersion(ob)
	// Preserve the lock-free cache hit for the common unchanged-mode path.
	if cached && current.version >= version {
		if current.version != version {
			return nil, ErrNoAvailableNodes
		}
		return current.transport, nil
	}
	var retired *http.Transport
	entry, _ := p.transports.Compute(hash, func(current outboundTransportEntry, loaded bool) (outboundTransportEntry, xsync.ComputeOp) {
		if loaded && current.version >= version {
			return current, xsync.CancelOp
		}
		if loaded {
			retired = current.transport
		}
		return outboundTransportEntry{version: version, transport: p.newReusableOutboundTransport(ob, sink)}, xsync.UpdateOp
	})
	if retired != nil {
		retired.CloseIdleConnections()
	}
	if entry.version != version {
		return nil, ErrNoAvailableNodes
	}
	return entry.transport, nil
}

func outboundRoutingVersion(ob adapter.Outbound) uint64 {
	if versioned, ok := ob.(interface{ RoutingVersion() uint64 }); ok {
		return versioned.RoutingVersion()
	}
	return 0
}

// Evict closes idle connections for one node transport and removes it from pool.
func (p *OutboundTransportPool) Evict(hash node.Hash) {
	entry, ok := p.transports.LoadAndDelete(hash)
	if !ok || entry.transport == nil {
		return
	}
	entry.transport.CloseIdleConnections()
}

// CloseAll closes idle connections and clears all pooled transports.
func (p *OutboundTransportPool) CloseAll() {
	p.transports.Range(func(_ node.Hash, entry outboundTransportEntry) bool {
		if entry.transport != nil {
			entry.transport.CloseIdleConnections()
		}
		return true
	})
	p.transports.Clear()
}

func (p *OutboundTransportPool) newReusableOutboundTransport(ob adapter.Outbound, sink MetricsEventSink) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := ob.DialContext(outboundDialContext{ctx}, network, M.ParseSocksaddr(addr))
			if err != nil {
				return nil, err
			}
			if sink != nil {
				sink.OnConnectionLifecycle(ConnectionOutbound, ConnectionOpen)
				conn = newCountingConn(conn, sink)
			}
			return conn, nil
		},
		DisableKeepAlives:   false,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        p.config.MaxIdleConns,
		MaxIdleConnsPerHost: p.config.MaxIdleConnsPerHost,
		IdleConnTimeout:     p.config.IdleConnTimeout,
	}
}

func newDirectHTTPTransport(cfg OutboundTransportConfig, sink MetricsEventSink) *http.Transport {
	cfg = normalizeOutboundTransportConfig(cfg)
	dialer := &net.Dialer{}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			if sink != nil {
				sink.OnConnectionLifecycle(ConnectionOutbound, ConnectionOpen)
				conn = newCountingConn(conn, sink)
			}
			return conn, nil
		},
		DisableKeepAlives:   false,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        cfg.MaxIdleConns,
		MaxIdleConnsPerHost: cfg.MaxIdleConnsPerHost,
		IdleConnTimeout:     cfg.IdleConnTimeout,
	}
}
