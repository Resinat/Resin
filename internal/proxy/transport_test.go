package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

type noopOutbound struct {
	adapter.Outbound
}

type versionedTestOutbound struct {
	noopOutbound
	version atomic.Uint64
}

func (n *versionedTestOutbound) RoutingVersion() uint64 { return n.version.Load() }

func TestTransportPoolChangesWithOutboundMode(t *testing.T) {
	pool := newOutboundTransportPool()
	ob := new(versionedTestOutbound)
	ob.version.Store(1)
	hash := node.Hash{7}
	first := mustGetTransport(t, pool, hash, ob, nil)
	ob.version.Store(2)
	second := mustGetTransport(t, pool, hash, ob, nil)
	if first == second {
		t.Fatal("new mode reused old HTTP connections")
	}
	if mustGetTransport(t, pool, hash, ob, nil) != second {
		t.Fatal("same mode did not reuse its transport")
	}
	stale := new(versionedTestOutbound)
	stale.version.Store(1)
	if transport, err := pool.Get(hash, stale, nil); transport != nil || err != ErrNoAvailableNodes {
		t.Fatalf("stale version reused another mode: %v %v", transport, err)
	}
	if mustGetTransport(t, pool, hash, ob, nil) != second {
		t.Fatal("stale caller rolled the pool back")
	}

}

func (n *noopOutbound) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return nil, errors.New("not used in transport-pool tests")
}

func (n *noopOutbound) Tag() string  { return "noop" }
func (n *noopOutbound) Type() string { return "noop" }

func TestOutboundDialPreservesContextWithoutOuterHTTPTrace(t *testing.T) {
	type contextKey struct{}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), contextKey{}, "preserved"), time.Second)
	defer cancel()
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GetConn: func(string) {}})
	var dialContext context.Context
	ob := &mockOutbound{dialFunc: func(inner context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
		dialContext = inner
		if httptrace.ContextClientTrace(inner) != nil {
			t.Error("outer HTTP trace leaked into protocol dial")
		}
		if inner.Value(contextKey{}) != "preserved" || inner.Done() != ctx.Done() {
			t.Error("protocol dial lost request context data or cancellation")
		}
		deadline, ok := inner.Deadline()
		want, _ := ctx.Deadline()
		if !ok || !deadline.Equal(want) {
			t.Error("protocol dial lost deadline")
		}
		return nil, errors.New("dial failed")
	}}
	transport := newOutboundTransportPool().newReusableOutboundTransport(ob, nil)
	_, _ = transport.DialContext(ctx, "tcp", "example.com:443")
	cancel()
	if dialContext == nil || dialContext.Err() != context.Canceled {
		t.Fatal("dial context did not follow cancellation")
	}
	if httptrace.ContextClientTrace(ctx) == nil {
		t.Fatal("protocol dial modified the original request trace")
	}
}

func TestOutboundTransportPool_ReusesByNodeHash(t *testing.T) {
	pool := newOutboundTransportPool()
	hash := node.Hash{1}

	t1 := mustGetTransport(t, pool, hash, &noopOutbound{}, nil)
	t2 := mustGetTransport(t, pool, hash, &noopOutbound{}, nil)

	if t1 != t2 {
		t.Fatal("expected same transport instance for identical node hash")
	}
}

func TestOutboundTransportPool_SplitsByNodeHash(t *testing.T) {
	pool := newOutboundTransportPool()
	ob := &noopOutbound{}
	hash1 := node.Hash{1}
	hash2 := node.Hash{2}

	base := mustGetTransport(t, pool, hash1, ob, nil)
	byNodeHash := mustGetTransport(t, pool, hash2, ob, nil)
	if base == byNodeHash {
		t.Fatal("expected different transport for different node hash")
	}
}

func TestOutboundTransportPool_UsesKeepAliveTransport(t *testing.T) {
	pool := newOutboundTransportPool()
	ob := &noopOutbound{}
	hash := node.Hash{1}

	transport := mustGetTransport(t, pool, hash, ob, nil)
	if transport.DisableKeepAlives {
		t.Fatal("expected keep-alive enabled transport")
	}
}

func TestOutboundTransportPool_EvictRemovesNodeTransport(t *testing.T) {
	pool := newOutboundTransportPool()
	hash := node.Hash{1}
	ob := &noopOutbound{}

	t1 := mustGetTransport(t, pool, hash, ob, nil)
	pool.Evict(hash)
	t2 := mustGetTransport(t, pool, hash, ob, nil)

	if t1 == t2 {
		t.Fatal("expected a new transport after evict")
	}
}

func TestOutboundTransportPool_AppliesConfiguredLimits(t *testing.T) {
	pool := newOutboundTransportPoolWithConfig(OutboundTransportConfig{
		MaxIdleConns:        9,
		MaxIdleConnsPerHost: 3,
		IdleConnTimeout:     12 * time.Second,
	})
	ob := &noopOutbound{}
	hash := node.Hash{1}

	transport := mustGetTransport(t, pool, hash, ob, nil)
	if transport.MaxIdleConns != 9 {
		t.Fatalf("MaxIdleConns: got %d, want %d", transport.MaxIdleConns, 9)
	}
	if transport.MaxIdleConnsPerHost != 3 {
		t.Fatalf("MaxIdleConnsPerHost: got %d, want %d", transport.MaxIdleConnsPerHost, 3)
	}
	if transport.IdleConnTimeout != 12*time.Second {
		t.Fatalf("IdleConnTimeout: got %s, want %s", transport.IdleConnTimeout, 12*time.Second)
	}
}

func TestOutboundTransportPool_CloseAllClearsEntries(t *testing.T) {
	pool := newOutboundTransportPool()
	ob := &noopOutbound{}

	hashA := node.Hash{1}
	hashB := node.Hash{2}
	t1 := mustGetTransport(t, pool, hashA, ob, nil)
	_ = mustGetTransport(t, pool, hashB, ob, nil)

	pool.CloseAll()

	t2 := mustGetTransport(t, pool, hashA, ob, nil)
	if t1 == t2 {
		t.Fatal("expected a new transport after CloseAll")
	}
}

func mustGetTransport(t *testing.T, pool *OutboundTransportPool, hash node.Hash, ob adapter.Outbound, sink MetricsEventSink) *http.Transport {
	t.Helper()
	transport, err := pool.Get(hash, ob, sink)
	if err != nil {
		t.Fatal(err)
	}
	return transport
}

// Pause after Get has read the old cache entry but before it enters Compute,
// ensuring that both cache lookup paths reject newer versions for an old call.
func TestTransportPoolRejectsStaleVersionDuringCacheUpdate(t *testing.T) {
	pool := newOutboundTransportPool()
	defer pool.CloseAll()
	hash := node.Hash{8}
	initial := new(versionedTestOutbound)
	initial.version.Store(0)
	mustGetTransport(t, pool, hash, initial, nil)
	stale := &blockedVersionOutbound{entered: make(chan struct{}), release: make(chan struct{})}
	result := make(chan *ProxyError, 1)
	go func() {
		tr, err := pool.Get(hash, stale, nil)
		if tr != nil {
			result <- nil
			return
		}
		result <- err
	}()
	<-stale.entered
	current := new(versionedTestOutbound)
	current.version.Store(2)
	expected := mustGetTransport(t, pool, hash, current, nil)
	close(stale.release)
	if err := <-result; err != ErrNoAvailableNodes {
		t.Fatalf("stale caller accepted current transport: %v", err)
	}
	if mustGetTransport(t, pool, hash, current, nil) != expected {
		t.Fatal("stale caller replaced the new cache entry")
	}
}

type blockedVersionOutbound struct {
	noopOutbound
	entered, release chan struct{}
}

func (n *blockedVersionOutbound) RoutingVersion() uint64 { close(n.entered); <-n.release; return 1 }
