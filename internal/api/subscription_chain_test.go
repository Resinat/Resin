package api

import (
	"net/http"
	"sync"
	"testing"
)

func TestSubscriptionChainContract(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)
	create := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name": "chain", "url": "https://example.com/sub",
	}, true)
	if create.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", create.Code, create.Body.String())
	}
	body := decodeJSONMap(t, create)
	if body["chain_proxy"] != false || body["chain_relay"] != false {
		t.Fatalf("defaults: %v", body)
	}
	path := "/api/v1/subscriptions/" + body["id"].(string)
	for _, patch := range []map[string]any{
		{"chain_proxy": true, "chain_relay": true}, {"chain_proxy": "true"}, {"chain_relay": nil},
	} {
		response := doJSONRequest(t, srv, http.MethodPatch, path, patch, true)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid patch %v: %d %s", patch, response.Code, response.Body.String())
		}
	}
	set := doJSONRequest(t, srv, http.MethodPatch, path, map[string]any{"chain_proxy": true}, true)
	if set.Code != http.StatusOK {
		t.Fatalf("set proxy: %d %s", set.Code, set.Body.String())
	}
	conflict := doJSONRequest(t, srv, http.MethodPatch, path, map[string]any{"chain_relay": true}, true)
	if conflict.Code != http.StatusBadRequest {
		t.Fatalf("merge must reject conflict: %d", conflict.Code)
	}
	switchMode := doJSONRequest(t, srv, http.MethodPatch, path, map[string]any{"chain_proxy": false, "chain_relay": true}, true)
	if switchMode.Code != http.StatusOK {
		t.Fatalf("switch: %d %s", switchMode.Code, switchMode.Body.String())
	}
	body = decodeJSONMap(t, doJSONRequest(t, srv, http.MethodGet, path, nil, true))
	if body["chain_proxy"] != false || body["chain_relay"] != true {
		t.Fatalf("round trip: %v", body)
	}
	invalid := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name": "invalid", "source_type": "local", "content": "socks5://127.0.0.1:1080", "chain_proxy": true, "chain_relay": true,
	}, true)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("create conflict: %d", invalid.Code)
	}
}

func TestSubscriptionConcurrentChainPatchesStayExclusive(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)
	created := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name": "concurrent", "url": "https://example.com/sub",
	}, true)
	path := "/api/v1/subscriptions/" + decodeJSONMap(t, created)["id"].(string)
	start := make(chan struct{})
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for _, field := range []string{"chain_proxy", "chain_relay"} {
		wg.Add(1)
		go func(field string) {
			defer wg.Done()
			<-start
			response := doJSONRequest(t, srv, http.MethodPatch, path, map[string]any{field: true}, true)
			codes <- response.Code
		}(field)
	}
	close(start)
	wg.Wait()
	close(codes)
	winners, rejected := 0, 0
	for code := range codes {
		if code == http.StatusOK {
			winners++
		}
		if code == http.StatusBadRequest {
			rejected++
		}
	}
	if winners != 1 || rejected != 1 {
		t.Fatalf("successes=%d conflicts=%d", winners, rejected)
	}
	body := decodeJSONMap(t, doJSONRequest(t, srv, http.MethodGet, path, nil, true))
	if body["chain_proxy"] == body["chain_relay"] {
		t.Fatalf("invalid final mode: %v", body)
	}
}
