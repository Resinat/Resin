package outbound

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/topology"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

type chainHTTPProxy struct {
	server  *httptest.Server
	hits    atomic.Int64
	targets sync.Map
}

func newChainHTTPProxy(t *testing.T) *chainHTTPProxy {
	t.Helper()
	p := new(chainHTTPProxy)
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", 405)
			return
		}
		upstream, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(r.Context(), "tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer upstream.Close()
		downstream, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer downstream.Close()
		downstream.SetDeadline(time.Now().Add(10 * time.Second))
		upstream.SetDeadline(time.Now().Add(10 * time.Second))
		p.hits.Add(1)
		p.targets.Store(r.Host, true)
		if _, err := io.WriteString(downstream, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		done := make(chan struct{})
		go func() { io.Copy(upstream, buffered); upstream.Close(); close(done) }()
		io.Copy(downstream, upstream)
		downstream.Close()
		<-done
	}))
	t.Cleanup(p.server.Close)
	return p
}

func (p *chainHTTPProxy) raw(t *testing.T) json.RawMessage {
	t.Helper()
	host, port, err := net.SplitHostPort(p.server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	number, _ := strconv.Atoi(port)
	raw, err := json.Marshal(map[string]any{"type": "http", "server": host, "server_port": number})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestChainProxyRealRelaySwitchAndRoleChange(t *testing.T) {
	jumpA, jumpB, landing := newChainHTTPProxy(t), newChainHTTPProxy(t), newChainHTTPProxy(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer target.Close()
	subs := topology.NewSubscriptionManager()
	landSub := subscription.NewSubscription("landing", "landing", "", true, false)
	landSub.SetChainConfig(true, false)
	jumpSub := subscription.NewSubscription("jump", "jump", "", true, false)
	jumpSub.SetChainConfig(false, true)
	subs.Register(landSub)
	subs.Register(jumpSub)
	pool := topology.NewGlobalNodePool(topology.PoolConfig{SubLookup: subs.Lookup, MaxLatencyTableEntries: 8, MaxConsecutiveFailures: func() int { return 1 }})
	builder := newTestSingboxBuilder(t)
	defer builder.Close()
	mgr := NewOutboundManager(pool, builder)
	defer mgr.Close()
	selector := routing.NewRelaySelector(pool, nil, nil)
	mgr.SetRelayPicker(selector.Pick)
	pool.SetOnNodeAdded(mgr.EnsureNodeOutbound)
	pool.SetOnNodeModeChanged(mgr.EnsureNodeOutbound)
	add := func(sub *subscription.Subscription, raw json.RawMessage) node.Hash {
		h := node.HashFromRawOptions(raw)
		sub.ManagedNodes().StoreNode(h, subscription.ManagedNode{Tags: []string{"node"}})
		pool.AddNodeFromSub(h, raw, sub.ID)
		return h
	}
	landingRaw := landing.raw(t)
	h := add(landSub, landingRaw)
	e, _ := pool.GetEntry(h)
	stable := e.Outbound.Load()
	_, _, err := mgr.Fetch(context.Background(), h, target.URL)
	if !errors.Is(err, routing.ErrNoAvailableRelay) {
		t.Fatalf("missing-relay error: %v", err)
	}
	if landing.hits.Load() != 0 {
		t.Fatal("missing relay fell back to a direct landing connection")
	}
	a, b := add(jumpSub, jumpA.raw(t)), add(jumpSub, jumpB.raw(t))
	pool.RecordResult(a, true)
	fetch := func() {
		t.Helper()
		body, _, err := mgr.Fetch(context.Background(), h, target.URL)
		if err != nil || string(body) != "ok" {
			t.Fatalf("fetch=%q err=%v", body, err)
		}
	}
	fetch()
	pool.RecordResult(a, false)
	pool.RecordResult(b, true)
	fetch()
	if jumpA.hits.Load() != 1 || jumpB.hits.Load() != 1 || landing.hits.Load() != 2 {
		t.Fatalf("hits A=%d B=%d landing=%d", jumpA.hits.Load(), jumpB.hits.Load(), landing.hits.Load())
	}
	if _, ok := jumpA.targets.Load(landing.server.Listener.Addr().String()); !ok {
		t.Fatal("relay did not connect to the landing server")
	}
	landSub.SetChainConfig(false, true)
	pool.ReconcileNodeRole(h)
	fetch()
	landSub.SetChainConfig(true, false)
	pool.ReconcileNodeRole(h)
	fetch()
	if jumpA.hits.Load() != 1 || jumpB.hits.Load() != 2 || landing.hits.Load() != 4 {
		t.Fatal("mode change did not choose the correct native outbound")
	}
	if e.Outbound.Load() == stable || string(e.RawOptions) != string(landingRaw) {
		t.Fatal("mode change failed to replace outbound or mutated node identity")
	}
	// Concurrent requests use the current immutable outbound.
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			body, _, err := mgr.Fetch(context.Background(), h, target.URL)
			if err != nil || string(body) != "ok" {
				t.Errorf("concurrent fetch: %q %v", body, err)
			}
		}()
	}
	close(start)
	wg.Wait()

}

type chainClosingBuilder struct {
	mu    sync.Mutex
	built []*closableOnly
}

func (b *chainClosingBuilder) Build(json.RawMessage) (adapter.Outbound, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ob := new(closableOnly)
	b.built = append(b.built, ob)
	return ob, nil
}
func (b *chainClosingBuilder) BuildWithDetour(raw json.RawMessage, _ adapter.Outbound) (adapter.Outbound, error) {
	return b.Build(raw)
}

func TestNodeOutboundRetiresPreviousMode(t *testing.T) {
	entry := newTestEntry(`{"type":"http","server":"landing"}`)
	pool := new(mockPool)
	pool.addEntry(entry)
	builder := new(chainClosingBuilder)
	mgr := NewOutboundManager(pool, builder)
	mgr.EnsureNodeOutbound(entry.Hash)
	stable := *entry.Outbound.Load()
	entry.UpdateRoutingRole(node.RoleChain)
	mgr.EnsureNodeOutbound(entry.Hash)
	entry.UpdateRoutingRole(node.RoleOrdinary)
	mgr.EnsureNodeOutbound(entry.Hash)
	if len(builder.built) != 3 {
		t.Fatalf("native builds=%d, want 3", len(builder.built))
	}
	for _, ob := range builder.built[:2] {
		if !ob.closed.Load() {
			t.Fatal("mode switch retained an old native instance")
		}
	}
	if builder.built[2].closed.Load() {
		t.Fatal("current mode already closed")
	}
	mgr.RemoveNodeOutbound(entry)
	for _, ob := range builder.built {
		if !ob.closed.Load() {
			t.Fatal("native mode leaked on node removal")
		}
	}
	if _, err := stable.DialContext(context.Background(), "tcp", M.ParseSocksaddr("127.0.0.1:80")); !errors.Is(err, ErrOutboundNotReady) {
		t.Fatalf("removed facade accepted dial: %v", err)
	}
}

func TestCloseWaitsForBuildAndRejectsPublication(t *testing.T) {
	entry := newTestEntry(`{"type":"http","server":"slow"}`)
	pool := new(mockPool)
	pool.addEntry(entry)
	builder := newBlockingClosableBuilder()
	mgr := NewOutboundManager(pool, builder)
	built := make(chan struct{})
	go func() { defer close(built); mgr.EnsureNodeOutbound(entry.Hash) }()
	<-builder.started
	closed := make(chan struct{})
	go func() { defer close(closed); _ = mgr.Close() }()
	deadline := time.Now().Add(time.Second)
	for !mgr.isClosed() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	select {
	case <-closed:
		t.Fatal("Close returned before its in-flight build was reclaimed")
	default:
	}
	close(builder.release)
	<-built
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close failed to drain")
	}
	if entry.HasOutbound() || !builder.firstBuilt(t).closed.Load() {
		t.Fatal("shutdown leaked/published the in-flight native")
	}
	mgr.EnsureNodeOutbound(entry.Hash)
	if entry.Outbound.Load() != nil {
		t.Fatal("closed manager accepted a new build")
	}
}

func TestModePublicationNeverExposesOlderOutbound(t *testing.T) {
	subs := topology.NewSubscriptionManager()
	sub := subscription.NewSubscription("sub", "sub", "", true, false)
	subs.Register(sub)
	pool := topology.NewGlobalNodePool(topology.PoolConfig{SubLookup: subs.Lookup, MaxLatencyTableEntries: 8, MaxConsecutiveFailures: func() int { return 1 }})
	raw := []byte(`{"type":"http","server":"landing"}`)
	hash := node.HashFromRawOptions(raw)
	mgr := NewOutboundManager(pool, new(chainClosingBuilder))
	defer mgr.Close()
	pool.SetOnNodeAdded(mgr.EnsureNodeOutbound)
	pool.SetOnNodeModeChanged(mgr.EnsureNodeOutbound)
	sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{})
	pool.AddNodeFromSub(hash, raw, sub.ID)
	entry, _ := pool.GetEntry(hash)
	stop := make(chan struct{})
	bad := make(chan struct{}, 1)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				mgr.EnsureNodeOutbound(hash)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			version := entry.RoutingVersion()
			ptr := entry.Outbound.Load()
			if ptr != nil && (*ptr).(*nodeOutbound).version < version {
				select {
				case bad <- struct{}{}:
				default:
				}
				return
			}
		}
	}()
	for i := 0; i < 1000; i++ {
		sub.SetChainConfig(i%2 == 0, false)
		pool.ReconcileNodeRole(hash)
	}
	close(stop)
	wg.Wait()
	select {
	case <-bad:
		t.Fatal("new mode was observable alongside an older outbound")
	default:
	}
}
