package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/netutil"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/topology"
)

func TestAPIContract_SubscriptionRefreshAction_E2EHTTPSource(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)

	const rawOutbound = `{"type":"shadowsocks","tag":"edge-refresh","server":"1.1.1.1","server_port":443,"method":"aes-256-gcm","password":"secret"}`
	subPayload := `{"outbounds":[` + rawOutbound + `]}`

	const userAgent = "resin-api-e2e"
	var subscriptionHits atomic.Int32
	subscriptionSource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		subscriptionHits.Add(1)
		if got := r.URL.Path; got != "/sub" {
			t.Fatalf("subscription path: got %q, want %q", got, "/sub")
		}
		if got := r.Header.Get("User-Agent"); got != userAgent {
			t.Fatalf("subscription user-agent: got %q, want %q", got, userAgent)
		}
		_, _ = w.Write([]byte(subPayload))
	}))
	defer subscriptionSource.Close()

	cp.Scheduler = topology.NewSubscriptionScheduler(topology.SchedulerConfig{
		SubManager: cp.SubMgr,
		Pool:       cp.Pool,
		Downloader: netutil.NewDirectDownloader(
			func() time.Duration { return 2 * time.Second },
			func() string { return userAgent },
		),
	})

	createRec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name": "sub-e2e",
		"url":  subscriptionSource.URL + "/sub",
	}, true)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create subscription status: got %d, want %d, body=%s", createRec.Code, http.StatusCreated, createRec.Body.String())
	}
	createBody := decodeJSONMap(t, createRec)
	subID, _ := createBody["id"].(string)
	if subID == "" {
		t.Fatalf("create subscription missing id: body=%s", createRec.Body.String())
	}
	if got := createBody["node_count"]; got != float64(0) {
		t.Fatalf("create subscription node_count: got %v, want %v", got, 0)
	}
	if got := createBody["healthy_node_count"]; got != float64(0) {
		t.Fatalf("create subscription healthy_node_count: got %v, want %v", got, 0)
	}

	refreshRec := doJSONRequest(
		t,
		srv,
		http.MethodPost,
		"/api/v1/subscriptions/"+subID+"/actions/refresh",
		nil,
		true,
	)
	if refreshRec.Code != http.StatusOK {
		t.Fatalf("refresh subscription status: got %d, want %d, body=%s", refreshRec.Code, http.StatusOK, refreshRec.Body.String())
	}
	refreshBody := decodeJSONMap(t, refreshRec)
	if refreshBody["status"] != "ok" {
		t.Fatalf("refresh response status: got %v, want ok", refreshBody["status"])
	}
	if subscriptionHits.Load() == 0 {
		t.Fatal("subscription HTTP source was not requested")
	}

	getRec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/subscriptions/"+subID, nil, true)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get subscription status: got %d, want %d, body=%s", getRec.Code, http.StatusOK, getRec.Body.String())
	}
	subBody := decodeJSONMap(t, getRec)
	if got, _ := subBody["id"].(string); got != subID {
		t.Fatalf("subscription id: got %q, want %q", got, subID)
	}
	lastChecked, _ := subBody["last_checked"].(string)
	if strings.TrimSpace(lastChecked) == "" {
		t.Fatalf("last_checked should be non-empty after refresh, body=%s", getRec.Body.String())
	}
	lastUpdated, _ := subBody["last_updated"].(string)
	if strings.TrimSpace(lastUpdated) == "" {
		t.Fatalf("last_updated should be non-empty after refresh, body=%s", getRec.Body.String())
	}
	if v, ok := subBody["last_error"]; ok {
		if s, _ := v.(string); s != "" {
			t.Fatalf("last_error should be empty after successful refresh, got %q", s)
		}
	}
	if got := subBody["node_count"]; got != float64(1) {
		t.Fatalf("subscription node_count after refresh: got %v, want %v", got, 1)
	}
	if got := subBody["healthy_node_count"]; got != float64(0) {
		t.Fatalf("subscription healthy_node_count after refresh: got %v, want %v", got, 0)
	}

	nodesRec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/nodes?subscription_id="+subID, nil, true)
	if nodesRec.Code != http.StatusOK {
		t.Fatalf("list nodes by subscription status: got %d, want %d, body=%s", nodesRec.Code, http.StatusOK, nodesRec.Body.String())
	}
	nodesBody := decodeJSONMap(t, nodesRec)
	items, ok := nodesBody["items"].([]any)
	if !ok {
		t.Fatalf("items type: got %T", nodesBody["items"])
	}
	if len(items) != 1 {
		t.Fatalf("expected exactly 1 node after refresh, got %d, body=%s", len(items), nodesRec.Body.String())
	}

	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("node item type: got %T", items[0])
	}
	wantHash := node.HashFromRawOptions([]byte(rawOutbound)).Hex()
	gotHash, _ := item["node_hash"].(string)
	if gotHash != wantHash {
		t.Fatalf("node hash: got %q, want %q", gotHash, wantHash)
	}
}
