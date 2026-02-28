package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/netutil"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/testutil"
	"github.com/Resinat/Resin/internal/topology"
)

func newProxyE2EEnvFromSubscriptionURL(t *testing.T, subURL string) *proxyE2EEnv {
	t.Helper()

	subMgr := topology.NewSubscriptionManager()
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              func(_ netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})

	plat := platform.NewPlatform("plat-id", "plat", nil, nil)
	plat.StickyTTLNs = int64(time.Hour)
	plat.ReverseProxyMissAction = "RANDOM"
	pool.RegisterPlatform(plat)

	sub := subscription.NewSubscription("sub-1", "sub-1", subURL, true, false)
	sub.SetFetchConfig(subURL, int64(time.Hour))
	subMgr.Register(sub)

	downloader := netutil.NewDirectDownloader(
		func() time.Duration { return 2 * time.Second },
		func() string { return "resin-proxy-e2e" },
	)
	scheduler := topology.NewSubscriptionScheduler(topology.SchedulerConfig{
		SubManager: subMgr,
		Pool:       pool,
		Downloader: downloader,
	})
	scheduler.UpdateSubscription(sub)
	if errText := sub.GetLastError(); errText != "" {
		t.Fatalf("subscription refresh failed: %s", errText)
	}

	obMgr := outbound.NewOutboundManager(pool, &testutil.StubOutboundBuilder{})
	nodeCount := 0
	sub.ManagedNodes().RangeNodes(func(hash node.Hash, _ subscription.ManagedNode) bool {
		nodeCount++
		obMgr.EnsureNodeOutbound(hash)

		entry, ok := pool.GetEntry(hash)
		if !ok {
			t.Fatalf("node %s missing from pool", hash.Hex())
		}
		if !entry.HasOutbound() {
			t.Fatalf("node %s outbound not initialized", hash.Hex())
		}

		ip := netip.MustParseAddr("203.0.113.10")
		pool.UpdateNodeEgressIP(hash, &ip, nil)
		latency := 20 * time.Millisecond
		pool.RecordLatency(hash, "example.com", &latency)
		pool.RecordResult(hash, true)
		return true
	})
	if nodeCount == 0 {
		t.Fatal("subscription refresh produced no nodes")
	}

	pool.RebuildAllPlatforms()
	if plat.View().Size() == 0 {
		t.Fatal("platform routable view should not be empty after hydration")
	}

	router := routing.NewRouter(routing.RouterConfig{
		Pool:        pool,
		Authorities: func() []string { return []string{"example.com"} },
		P2CWindow:   func() time.Duration { return 10 * time.Minute },
	})

	return &proxyE2EEnv{
		pool:   pool,
		router: router,
	}
}

func TestForwardProxy_E2ELocalHTTPProxy_FromHTTPSubscription(t *testing.T) {
	const rawOutbound = `{"type":"shadowsocks","tag":"edge-a","server":"198.51.100.10","server_port":443,"method":"aes-256-gcm","password":"secret"}`
	subBody := `{"outbounds":[` + rawOutbound + `]}`

	var subHits atomic.Int32
	subSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		subHits.Add(1)
		if r.URL.Path != "/sub" {
			t.Fatalf("subscription path: got %q, want %q", r.URL.Path, "/sub")
		}
		if ua := r.Header.Get("User-Agent"); ua != "resin-proxy-e2e" {
			t.Fatalf("subscription user-agent: got %q, want %q", ua, "resin-proxy-e2e")
		}
		_, _ = w.Write([]byte(subBody))
	}))
	defer subSrv.Close()

	env := newProxyE2EEnvFromSubscriptionURL(t, subSrv.URL+"/sub")
	if subHits.Load() == 0 {
		t.Fatal("subscription endpoint was not requested")
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Proxy-Authorization"); got != "" {
			t.Fatalf("Proxy-Authorization leaked to upstream: %q", got)
		}
		if got := r.URL.Path; got != "/v1/ping" {
			t.Fatalf("unexpected upstream path: %q", got)
		}
		if got := r.URL.RawQuery; got != "q=1" {
			t.Fatalf("unexpected upstream query: %q", got)
		}
		w.Header().Set("X-Upstream", "ok")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("forward-e2e-subscription"))
	}))
	defer upstream.Close()

	fp := NewForwardProxy(ForwardProxyConfig{
		ProxyToken: "tok",
		Router:     env.router,
		Pool:       env.pool,
		Health:     &mockHealthRecorder{},
		Events:     NoOpEventEmitter{},
	})
	proxySrv := httptest.NewServer(fp)
	defer proxySrv.Close()

	proxyURL, err := url.Parse(proxySrv.URL)
	if err != nil {
		t.Fatalf("parse proxy url: %v", err)
	}
	proxyURL.User = url.UserPassword("tok", "plat")

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}
	t.Cleanup(func() { client.CloseIdleConnections() })

	resp, err := client.Get(upstream.URL + "/v1/ping?q=1")
	if err != nil {
		t.Fatalf("proxy request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status: got %d, want %d (body=%q)", resp.StatusCode, http.StatusCreated, string(body))
	}
	if got := resp.Header.Get("X-Upstream"); got != "ok" {
		t.Fatalf("X-Upstream: got %q, want %q", got, "ok")
	}
	if got := string(body); got != "forward-e2e-subscription" {
		t.Fatalf("body: got %q, want %q", got, "forward-e2e-subscription")
	}
}

func TestReverseProxy_E2EServer_FromHTTPSubscription(t *testing.T) {
	const rawOutbound = `{"type":"vmess","tag":"edge-b","server":"198.51.100.20","server_port":443,"uuid":"11111111-1111-1111-1111-111111111111"}`
	subBody := `{"outbounds":[` + rawOutbound + `]}`

	var subHits atomic.Int32
	subSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		subHits.Add(1)
		_, _ = w.Write([]byte(subBody))
	}))
	defer subSrv.Close()

	env := newProxyE2EEnvFromSubscriptionURL(t, subSrv.URL)
	if subHits.Load() == 0 {
		t.Fatal("subscription endpoint was not requested")
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/api/v1/items" {
			t.Fatalf("unexpected path: %q", got)
		}
		if got := r.URL.RawQuery; got != "k=v" {
			t.Fatalf("unexpected query: %q", got)
		}
		if got := r.Header.Get("X-Forwarded-For"); got != "" {
			t.Fatalf("X-Forwarded-For should be stripped, got %q", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("reverse-e2e-subscription"))
	}))
	defer upstream.Close()

	rp := NewReverseProxy(ReverseProxyConfig{
		ProxyToken:     "tok",
		Router:         env.router,
		Pool:           env.pool,
		PlatformLookup: env.pool,
		Health:         &mockHealthRecorder{},
		Events:         NoOpEventEmitter{},
	})
	reverseSrv := httptest.NewServer(rp)
	defer reverseSrv.Close()

	host := strings.TrimPrefix(upstream.URL, "http://")
	reqURL := reverseSrv.URL + "/tok/plat:acct/http/" + host + "/api/v1/items?k=v"

	resp, err := http.Get(reqURL)
	if err != nil {
		t.Fatalf("reverse request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want %d (body=%q)", resp.StatusCode, http.StatusOK, string(body))
	}
	if got := string(body); got != "reverse-e2e-subscription" {
		t.Fatalf("body: got %q, want %q", got, "reverse-e2e-subscription")
	}
}
