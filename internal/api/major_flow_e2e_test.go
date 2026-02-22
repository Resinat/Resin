package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resin-proxy/resin/internal/config"
	"github.com/resin-proxy/resin/internal/geoip"
	"github.com/resin-proxy/resin/internal/model"
	"github.com/resin-proxy/resin/internal/netutil"
	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/outbound"
	"github.com/resin-proxy/resin/internal/proxy"
	"github.com/resin-proxy/resin/internal/requestlog"
	"github.com/resin-proxy/resin/internal/routing"
	"github.com/resin-proxy/resin/internal/service"
	"github.com/resin-proxy/resin/internal/state"
	"github.com/resin-proxy/resin/internal/testutil"
	"github.com/resin-proxy/resin/internal/topology"
)

type requestLogOnlyEmitter struct {
	svc *requestlog.Service
}

func (e requestLogOnlyEmitter) EmitRequestFinished(proxy.RequestFinishedEvent) {}
func (e requestLogOnlyEmitter) EmitRequestLog(entry proxy.RequestLogEntry) {
	if e.svc != nil {
		e.svc.EmitRequestLog(entry)
	}
}

type majorFlowHarness struct {
	apiServer   *Server
	cp          *service.ControlPlaneService
	forwardURL  string
	reverseURL  string
	requestlogs *requestlog.Service
}

func newMajorFlowHarness(t *testing.T, subscriptionUserAgent string) *majorFlowHarness {
	t.Helper()

	root := t.TempDir()
	engine, closer, err := state.PersistenceBootstrap(
		filepath.Join(root, "state"),
		filepath.Join(root, "cache"),
	)
	if err != nil {
		t.Fatalf("PersistenceBootstrap: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	runtimeCfg := &atomic.Pointer[config.RuntimeConfig]{}
	cfg := config.NewDefaultRuntimeConfig()
	cfg.RequestLogEnabled = true
	cfg.UserAgent = subscriptionUserAgent
	runtimeCfg.Store(cfg)

	subMgr := topology.NewSubscriptionManager()
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              func(netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 32,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})
	outboundMgr := outbound.NewOutboundManager(pool, &testutil.StubOutboundBuilder{})

	// Mirror production bootstrap semantics: node enters pool -> outbound prepared ->
	// dynamic properties (egress + latency) become routable.
	pool.SetOnNodeAdded(func(hash node.Hash) {
		outboundMgr.EnsureNodeOutbound(hash)
		ip := netip.MustParseAddr("203.0.113.55")
		pool.UpdateNodeEgressIP(hash, &ip, nil)
		latency := 25 * time.Millisecond
		pool.RecordLatency(hash, "example.com", &latency)
	})
	pool.SetOnNodeRemoved(func(_ node.Hash, entry *node.NodeEntry) {
		outboundMgr.RemoveNodeOutbound(entry)
	})

	router := routing.NewRouter(routing.RouterConfig{
		Pool:            pool,
		Authorities:     func() []string { return []string{"example.com"} },
		P2CWindow:       func() time.Duration { return 10 * time.Minute },
		NodeTagResolver: pool.ResolveNodeDisplayTag,
	})

	scheduler := topology.NewSubscriptionScheduler(topology.SchedulerConfig{
		SubManager: subMgr,
		Pool:       pool,
		Downloader: netutil.NewDirectDownloader(
			func() time.Duration { return 2 * time.Second },
			func() string { return subscriptionUserAgent },
		),
	})

	geoSvc := geoip.NewService(geoip.ServiceConfig{
		CacheDir: filepath.Join(root, "geoip"),
		OpenDB:   geoip.NoOpOpen,
	})

	reqRepo := requestlog.NewRepo(filepath.Join(root, "request-logs"), 64*1024*1024, 2)
	if err := reqRepo.Open(); err != nil {
		t.Fatalf("requestlog repo open: %v", err)
	}
	t.Cleanup(func() { _ = reqRepo.Close() })

	reqSvc := requestlog.NewService(requestlog.ServiceConfig{
		Repo:          reqRepo,
		QueueSize:     256,
		FlushBatch:    16,
		FlushInterval: 20 * time.Millisecond,
	})
	reqSvc.Start()
	t.Cleanup(reqSvc.Stop)

	cp := &service.ControlPlaneService{
		Engine:         engine,
		Pool:           pool,
		SubMgr:         subMgr,
		Scheduler:      scheduler,
		Router:         router,
		GeoIP:          geoSvc,
		MatcherRuntime: proxy.NewAccountMatcherRuntime(nil),
		RuntimeCfg:     runtimeCfg,
		EnvCfg: &config.EnvConfig{
			DefaultPlatformStickyTTL:              30 * time.Minute,
			DefaultPlatformRegexFilters:           []string{},
			DefaultPlatformRegionFilters:          []string{},
			DefaultPlatformReverseProxyMissAction: "RANDOM",
			DefaultPlatformAllocationPolicy:       "BALANCED",
		},
	}

	systemInfo := service.SystemInfo{
		Version:   "1.0.0-test",
		GitCommit: "abc123",
		BuildTime: "2026-01-01T00:00:00Z",
		StartedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	apiSrv := NewServer(0, testAdminToken, systemInfo, runtimeCfg, cp.EnvCfg, cp, 1<<20, reqRepo, nil)

	emitter := proxy.ConfigAwareEventEmitter{
		Base: requestLogOnlyEmitter{svc: reqSvc},
		RequestLogEnabled: func() bool {
			snap := runtimeCfg.Load()
			return snap != nil && snap.RequestLogEnabled
		},
		ReverseProxyLogDetailEnabled: func() bool { return false },
	}

	forward := proxy.NewForwardProxy(proxy.ForwardProxyConfig{
		ProxyToken: "tok",
		Router:     router,
		Pool:       pool,
		Health:     pool,
		Events:     emitter,
	})
	reverse := proxy.NewReverseProxy(proxy.ReverseProxyConfig{
		ProxyToken:     "tok",
		Router:         router,
		Pool:           pool,
		PlatformLookup: pool,
		Health:         pool,
		Events:         emitter,
	})

	forwardSrv := httptest.NewServer(forward)
	reverseSrv := httptest.NewServer(reverse)
	t.Cleanup(forwardSrv.Close)
	t.Cleanup(reverseSrv.Close)

	return &majorFlowHarness{
		apiServer:   apiSrv,
		cp:          cp,
		forwardURL:  forwardSrv.URL,
		reverseURL:  reverseSrv.URL,
		requestlogs: reqSvc,
	}
}

func TestMajorFlow_E2E_LocalProxyAndSubscriptionProvider(t *testing.T) {
	const (
		platformName = "plat-e2e"
		account      = "acct-e2e"
		subUA        = "resin-major-e2e"
	)
	h := newMajorFlowHarness(t, subUA)

	const rawOutbound = `{"type":"shadowsocks","tag":"major-node","server":"1.1.1.1","server_port":443,"method":"aes-256-gcm","password":"secret"}`
	subPayload := `{"outbounds":[` + rawOutbound + `]}`

	var subHits atomic.Int32
	subSource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		subHits.Add(1)
		if got := r.URL.Path; got != "/sub" {
			t.Fatalf("subscription path: got %q, want %q", got, "/sub")
		}
		if got := r.Header.Get("User-Agent"); got != subUA {
			t.Fatalf("subscription user-agent: got %q, want %q", got, subUA)
		}
		_, _ = w.Write([]byte(subPayload))
	}))
	defer subSource.Close()

	createPlatformRec := doJSONRequest(t, h.apiServer, http.MethodPost, "/api/v1/platforms", map[string]any{
		"name": platformName,
	}, true)
	if createPlatformRec.Code != http.StatusCreated {
		t.Fatalf("create platform status: got %d, want %d, body=%s", createPlatformRec.Code, http.StatusCreated, createPlatformRec.Body.String())
	}
	platformBody := decodeJSONMap(t, createPlatformRec)
	platformID, _ := platformBody["id"].(string)
	if platformID == "" {
		t.Fatalf("create platform missing id: body=%s", createPlatformRec.Body.String())
	}

	createSubRec := doJSONRequest(t, h.apiServer, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name": "sub-major",
		"url":  subSource.URL + "/sub",
	}, true)
	if createSubRec.Code != http.StatusCreated {
		t.Fatalf("create subscription status: got %d, want %d, body=%s", createSubRec.Code, http.StatusCreated, createSubRec.Body.String())
	}
	subBody := decodeJSONMap(t, createSubRec)
	subID, _ := subBody["id"].(string)
	if subID == "" {
		t.Fatalf("create subscription missing id: body=%s", createSubRec.Body.String())
	}

	refreshSubRec := doJSONRequest(
		t,
		h.apiServer,
		http.MethodPost,
		"/api/v1/subscriptions/"+subID+"/actions/refresh",
		nil,
		true,
	)
	if refreshSubRec.Code != http.StatusOK {
		t.Fatalf("refresh subscription status: got %d, want %d, body=%s", refreshSubRec.Code, http.StatusOK, refreshSubRec.Body.String())
	}
	if subHits.Load() == 0 {
		t.Fatal("subscription source should be requested during refresh")
	}

	nodesRec := doJSONRequest(t, h.apiServer, http.MethodGet, "/api/v1/nodes?subscription_id="+subID, nil, true)
	if nodesRec.Code != http.StatusOK {
		t.Fatalf("list nodes status: got %d, want %d, body=%s", nodesRec.Code, http.StatusOK, nodesRec.Body.String())
	}
	nodesBody := decodeJSONMap(t, nodesRec)
	nodes, ok := nodesBody["items"].([]any)
	if !ok {
		t.Fatalf("nodes.items type: got %T", nodesBody["items"])
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node after refresh, got %d, body=%s", len(nodes), nodesRec.Body.String())
	}
	nodeItem, ok := nodes[0].(map[string]any)
	if !ok {
		t.Fatalf("node item type: got %T", nodes[0])
	}
	wantHash := node.HashFromRawOptions([]byte(rawOutbound)).Hex()
	gotHash, _ := nodeItem["node_hash"].(string)
	if gotHash != wantHash {
		t.Fatalf("node hash: got %q, want %q", gotHash, wantHash)
	}
	if hasOutbound, _ := nodeItem["has_outbound"].(bool); !hasOutbound {
		t.Fatalf("node should have outbound after refresh callback, node=%v", nodeItem)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/forward/ping":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("forward-major-ok"))
		case "/reverse/ping":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("reverse-major-ok"))
		default:
			t.Fatalf("unexpected upstream path: %q", r.URL.Path)
		}
	}))
	defer upstream.Close()

	proxyURL, err := url.Parse(h.forwardURL)
	if err != nil {
		t.Fatalf("parse forward proxy url: %v", err)
	}
	proxyURL.User = url.UserPassword("tok", platformName+":"+account)
	forwardClient := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}
	t.Cleanup(forwardClient.CloseIdleConnections)

	forwardResp, err := forwardClient.Get(upstream.URL + "/forward/ping?q=1")
	if err != nil {
		t.Fatalf("forward request via proxy failed: %v", err)
	}
	forwardBody, _ := io.ReadAll(forwardResp.Body)
	_ = forwardResp.Body.Close()
	if forwardResp.StatusCode != http.StatusCreated {
		t.Fatalf("forward status: got %d, want %d, body=%q", forwardResp.StatusCode, http.StatusCreated, string(forwardBody))
	}
	if got := string(forwardBody); got != "forward-major-ok" {
		t.Fatalf("forward body: got %q, want %q", got, "forward-major-ok")
	}

	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")
	reverseResp, err := http.Get(h.reverseURL + "/tok/" + platformName + ":" + account + "/http/" + upstreamHost + "/reverse/ping?k=v")
	if err != nil {
		t.Fatalf("reverse request via proxy failed: %v", err)
	}
	reverseBody, _ := io.ReadAll(reverseResp.Body)
	_ = reverseResp.Body.Close()
	if reverseResp.StatusCode != http.StatusAccepted {
		t.Fatalf("reverse status: got %d, want %d, body=%q", reverseResp.StatusCode, http.StatusAccepted, string(reverseBody))
	}
	if got := string(reverseBody); got != "reverse-major-ok" {
		t.Fatalf("reverse body: got %q, want %q", got, "reverse-major-ok")
	}

	lease := h.cp.Router.ReadLease(model.LeaseKey{PlatformID: platformID, Account: account})
	if lease == nil {
		t.Fatal("sticky lease should be created after proxied requests")
	}

	leasesRec := doJSONRequest(
		t,
		h.apiServer,
		http.MethodGet,
		"/api/v1/platforms/"+platformID+"/leases?account="+account,
		nil,
		true,
	)
	if leasesRec.Code != http.StatusOK {
		t.Fatalf("list leases status: got %d, want %d, body=%s", leasesRec.Code, http.StatusOK, leasesRec.Body.String())
	}
	leasesBody := decodeJSONMap(t, leasesRec)
	leaseItems, ok := leasesBody["items"].([]any)
	if !ok {
		t.Fatalf("leases.items type: got %T", leasesBody["items"])
	}
	if len(leaseItems) != 1 {
		t.Fatalf("expected 1 lease for account=%q, got %d, body=%s", account, len(leaseItems), leasesRec.Body.String())
	}

	var requestLogItems []any
	deadline := time.Now().Add(2 * time.Second)
	for {
		logsRec := doJSONRequest(
			t,
			h.apiServer,
			http.MethodGet,
			"/api/v1/request-logs?platform_id="+platformID+"&account="+account+"&limit=20",
			nil,
			true,
		)
		if logsRec.Code != http.StatusOK {
			t.Fatalf("list request logs status: got %d, want %d, body=%s", logsRec.Code, http.StatusOK, logsRec.Body.String())
		}
		logsBody := decodeJSONMap(t, logsRec)
		items, ok := logsBody["items"].([]any)
		if !ok {
			t.Fatalf("request-logs.items type: got %T", logsBody["items"])
		}
		if len(items) >= 2 {
			requestLogItems = items
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected at least 2 request logs (forward + reverse), got %d", len(items))
		}
		time.Sleep(25 * time.Millisecond)
	}

	seenForward := false
	seenReverse := false
	for _, it := range requestLogItems {
		row, ok := it.(map[string]any)
		if !ok {
			t.Fatalf("request log row type: got %T", it)
		}
		pt, ok := row["proxy_type"].(float64)
		if !ok {
			t.Fatalf("proxy_type type: got %T", row["proxy_type"])
		}
		switch int(pt) {
		case int(proxy.ProxyTypeForward):
			seenForward = true
		case int(proxy.ProxyTypeReverse):
			seenReverse = true
		}
	}
	if !seenForward || !seenReverse {
		t.Fatalf("request logs should include both forward and reverse entries, seenForward=%v seenReverse=%v", seenForward, seenReverse)
	}
}
