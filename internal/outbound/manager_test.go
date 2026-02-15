package outbound

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/testutil"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

// --- Test helpers ---

// countingBuilder counts how many times Build is called.
type countingBuilder struct {
	mu    sync.Mutex
	count int
}

func (b *countingBuilder) Build(_ json.RawMessage) (adapter.Outbound, error) {
	b.mu.Lock()
	b.count++
	b.mu.Unlock()
	// Simulate some work to increase chance of concurrent calls overlapping.
	time.Sleep(time.Millisecond)
	return testutil.NewNoopOutbound(), nil
}

func (b *countingBuilder) Count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}

// failBuilder always fails.
type failBuilder struct{}

func (b *failBuilder) Build(_ json.RawMessage) (adapter.Outbound, error) {
	return nil, errors.New("simulated build failure")
}

type closableOnly struct {
	closed atomic.Bool
}

func (c *closableOnly) Close() error {
	c.closed.Store(true)
	return nil
}

func (c *closableOnly) Type() string {
	return "closable-only"
}

func (c *closableOnly) Tag() string {
	return "closable-only"
}

func (c *closableOnly) Network() []string {
	return []string{"tcp", "udp"}
}

func (c *closableOnly) Dependencies() []string {
	return nil
}

func (c *closableOnly) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return nil, errors.New("closable-only: dial not supported")
}

func (c *closableOnly) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("closable-only: listen packet not supported")
}

type blockingClosableBuilder struct {
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	built   []*closableOnly
}

func newBlockingClosableBuilder() *blockingClosableBuilder {
	return &blockingClosableBuilder{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (b *blockingClosableBuilder) Build(_ json.RawMessage) (adapter.Outbound, error) {
	select {
	case <-b.started:
	default:
		close(b.started)
	}
	<-b.release
	ob := &closableOnly{}
	b.mu.Lock()
	b.built = append(b.built, ob)
	b.mu.Unlock()
	return ob, nil
}

func (b *blockingClosableBuilder) firstBuilt(t *testing.T) *closableOnly {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.built) == 0 {
		t.Fatal("expected at least one built outbound")
	}
	return b.built[0]
}

// mockPool implements PoolAccessor for tests.
type mockPool struct {
	entries sync.Map // node.Hash -> *node.NodeEntry
}

func (p *mockPool) GetEntry(hash node.Hash) (*node.NodeEntry, bool) {
	v, ok := p.entries.Load(hash)
	if !ok {
		return nil, false
	}
	return v.(*node.NodeEntry), true
}

func (p *mockPool) RangeNodes(fn func(node.Hash, *node.NodeEntry) bool) {
	p.entries.Range(func(key, value any) bool {
		return fn(key.(node.Hash), value.(*node.NodeEntry))
	})
}

func (p *mockPool) addEntry(entry *node.NodeEntry) {
	p.entries.Store(entry.Hash, entry)
}

func (p *mockPool) removeEntry(hash node.Hash) {
	p.entries.Delete(hash)
}

func makeHash(seed string) node.Hash {
	return node.HashFromRawOptions([]byte(seed))
}

func newTestEntry(rawOpts string) *node.NodeEntry {
	h := makeHash(rawOpts)
	return node.NewNodeEntry(h, json.RawMessage(rawOpts), time.Now(), 0)
}

// --- Tests ---

func TestEnsureNodeOutbound_Success(t *testing.T) {
	entry := newTestEntry(`{"type":"test"}`)
	pool := &mockPool{}
	pool.addEntry(entry)

	mgr := NewOutboundManager(pool, &testutil.StubOutboundBuilder{})
	mgr.EnsureNodeOutbound(entry.Hash)

	if !entry.HasOutbound() {
		t.Fatal("expected HasOutbound() == true after EnsureNodeOutbound")
	}
}

func TestEnsureNodeOutbound_BuildFailure(t *testing.T) {
	entry := newTestEntry(`{"type":"fail"}`)
	pool := &mockPool{}
	pool.addEntry(entry)

	mgr := NewOutboundManager(pool, &failBuilder{})
	mgr.EnsureNodeOutbound(entry.Hash)

	if entry.HasOutbound() {
		t.Fatal("expected HasOutbound() == false after build failure")
	}
	if entry.GetLastError() == "" {
		t.Fatal("expected GetLastError() non-empty after build failure")
	}
}

func TestEnsureNodeOutbound_Idempotent(t *testing.T) {
	entry := newTestEntry(`{"type":"idem"}`)
	pool := &mockPool{}
	pool.addEntry(entry)

	builder := &countingBuilder{}
	mgr := NewOutboundManager(pool, builder)

	// Call twice sequentially.
	mgr.EnsureNodeOutbound(entry.Hash)
	mgr.EnsureNodeOutbound(entry.Hash)

	if !entry.HasOutbound() {
		t.Fatal("expected HasOutbound() == true")
	}
	// Second call should skip Build because Outbound is already non-nil.
	if builder.Count() != 1 {
		t.Fatalf("expected Build called 1 time, got %d", builder.Count())
	}
}

func TestEnsureNodeOutbound_ConcurrentIdempotent(t *testing.T) {
	entry := newTestEntry(`{"type":"conc"}`)
	pool := &mockPool{}
	pool.addEntry(entry)

	builder := &countingBuilder{}
	mgr := NewOutboundManager(pool, builder)

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	// Reset outbound to nil to force all goroutines to race.
	entry.Outbound.Store(nil)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			mgr.EnsureNodeOutbound(entry.Hash)
		}()
	}
	wg.Wait()

	if !entry.HasOutbound() {
		t.Fatal("expected HasOutbound() == true after concurrent ensure")
	}
	// Due to the fast-path check (Load != nil returns early) and CAS,
	// Build may be called more than once if multiple goroutines pass
	// the fast-path check simultaneously, but the final stored value
	// is set by exactly one CAS winner.
	t.Logf("Build called %d times (expected 1-N due to race window)", builder.Count())
}

func TestEnsureNodeOutbound_NodeRemovedDuringBuild_DropsAndCloses(t *testing.T) {
	entry := newTestEntry(`{"type":"slow-build-remove"}`)
	pool := &mockPool{}
	pool.addEntry(entry)

	builder := newBlockingClosableBuilder()
	mgr := NewOutboundManager(pool, builder)

	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.EnsureNodeOutbound(entry.Hash)
	}()

	<-builder.started

	// Simulate removal callback order: delete from pool first, then cleanup.
	pool.removeEntry(entry.Hash)
	mgr.RemoveNodeOutbound(entry)

	close(builder.release)
	<-done

	if entry.Outbound.Load() != nil {
		t.Fatal("expected removed entry to keep outbound nil")
	}
	ob := builder.firstBuilt(t)
	if !ob.closed.Load() {
		t.Fatal("expected built outbound to be closed when node is removed during build")
	}
}

func TestRemoveNodeOutbound(t *testing.T) {
	entry := newTestEntry(`{"type":"rm"}`)
	pool := &mockPool{}
	pool.addEntry(entry)

	mgr := NewOutboundManager(pool, &testutil.StubOutboundBuilder{})
	mgr.EnsureNodeOutbound(entry.Hash)
	if !entry.HasOutbound() {
		t.Fatal("setup: expected HasOutbound() == true")
	}

	mgr.RemoveNodeOutbound(entry)
	if entry.HasOutbound() {
		t.Fatal("expected HasOutbound() == false after RemoveNodeOutbound")
	}
}

func TestRemoveNodeOutbound_NilEntry(t *testing.T) {
	pool := &mockPool{}
	mgr := NewOutboundManager(pool, &testutil.StubOutboundBuilder{})
	// Should not panic.
	mgr.RemoveNodeOutbound(nil)
}

func TestFetch_OutboundNotReady(t *testing.T) {
	entry := newTestEntry(`{"type":"notready"}`)
	pool := &mockPool{}
	pool.addEntry(entry)

	mgr := NewOutboundManager(pool, &testutil.StubOutboundBuilder{})
	// Don't call EnsureNodeOutbound — outbound remains nil.

	ctx := context.Background()
	_, _, err := mgr.Fetch(ctx, entry.Hash, "http://example.com")
	if !errors.Is(err, ErrOutboundNotReady) {
		t.Fatalf("expected ErrOutboundNotReady, got: %v", err)
	}
}

func TestFetch_NodeNotFound(t *testing.T) {
	pool := &mockPool{}
	mgr := NewOutboundManager(pool, &testutil.StubOutboundBuilder{})

	ctx := context.Background()
	_, _, err := mgr.Fetch(ctx, makeHash("nonexistent"), "http://example.com")
	if err == nil {
		t.Fatal("expected error for non-existent node")
	}
}

func TestWarmupAll(t *testing.T) {
	pool := &mockPool{}
	entries := make([]*node.NodeEntry, 5)
	for i := range entries {
		entries[i] = node.NewNodeEntry(
			makeHash("warmup"+string(rune('0'+i))),
			json.RawMessage(`{}`),
			time.Now(), 0,
		)
		pool.addEntry(entries[i])
	}

	mgr := NewOutboundManager(pool, &testutil.StubOutboundBuilder{})
	mgr.WarmupAll()

	var count atomic.Int32
	pool.RangeNodes(func(_ node.Hash, e *node.NodeEntry) bool {
		if e.HasOutbound() {
			count.Add(1)
		}
		return true
	})
	if int(count.Load()) != len(entries) {
		t.Fatalf("expected all %d entries to have outbound, got %d", len(entries), count.Load())
	}
}

func TestFetch_HTTPStatusNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	}))
	defer srv.Close()

	entry := newTestEntry(`{"type":"status"}`)
	pool := &mockPool{}
	pool.addEntry(entry)

	mgr := NewOutboundManager(pool, &testutil.StubOutboundBuilder{})
	mgr.EnsureNodeOutbound(entry.Hash)
	_, _, err := mgr.Fetch(context.Background(), entry.Hash, srv.URL)
	if err == nil {
		t.Fatal("expected non-200 status to return error")
	}
	if !strings.Contains(err.Error(), "unexpected status 404") {
		t.Fatalf("expected status error, got: %v", err)
	}
}

func TestAsFetcher_AllowsHTTPNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("probe-body"))
	}))
	defer srv.Close()

	entry := newTestEntry(`{"type":"probe-non-200"}`)
	pool := &mockPool{}
	pool.addEntry(entry)

	mgr := NewOutboundManager(pool, &testutil.StubOutboundBuilder{})
	mgr.EnsureNodeOutbound(entry.Hash)

	fetcher := mgr.AsFetcher(func() time.Duration { return 5 * time.Second })
	outbound := entry.Outbound.Load()
	body, _, err := fetcher(*outbound, srv.URL)
	if err != nil {
		t.Fatalf("expected non-200 probe response to pass through, got: %v", err)
	}
	if string(body) != "probe-body" {
		t.Fatalf("unexpected body %q", string(body))
	}
}

func TestFetch_HTTPSCertValidationEnabled(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	entry := newTestEntry(`{"type":"https-cert"}`)
	pool := &mockPool{}
	pool.addEntry(entry)

	mgr := NewOutboundManager(pool, &testutil.StubOutboundBuilder{})
	mgr.EnsureNodeOutbound(entry.Hash)

	_, _, err := mgr.Fetch(context.Background(), entry.Hash, srv.URL)
	if err == nil {
		t.Fatal("expected TLS verification error for self-signed cert")
	}
	lower := strings.ToLower(err.Error())
	if !strings.Contains(lower, "x509") && !strings.Contains(lower, "certificate") {
		t.Fatalf("expected certificate verification error, got: %v", err)
	}
}

func TestFetchWithUserAgent_UsesCustomHeader(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	entry := newTestEntry(`{"type":"ua-custom"}`)
	pool := &mockPool{}
	pool.addEntry(entry)

	mgr := NewOutboundManager(pool, &testutil.StubOutboundBuilder{})
	mgr.EnsureNodeOutbound(entry.Hash)

	const customUA = "Resin-Test-UA/42"
	_, _, err := mgr.FetchWithUserAgent(context.Background(), entry.Hash, srv.URL, customUA)
	if err != nil {
		t.Fatalf("unexpected fetch error: %v", err)
	}
	if gotUA != customUA {
		t.Fatalf("unexpected user-agent: got %q want %q", gotUA, customUA)
	}
}
