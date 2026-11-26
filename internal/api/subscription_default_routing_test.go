package api

import (
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/service"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/testutil"
)

func TestSubscriptionDefaultRoutingContract(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	for _, source := range []string{"remote", "local"} {
		t.Run(source, func(t *testing.T) {
			for _, restricted := range []any{nil, true, false} {
				input := map[string]any{"name": source, "source_type": source}
				if source == "remote" {
					input["url"] = "https://example.com/sub"
				} else {
					input["content"] = "socks5://127.0.0.1:1080"
				}
				if restricted != nil {
					input["default_routing_disabled"] = restricted
				}
				created := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", input, true)
				if created.Code != http.StatusCreated {
					t.Fatalf("create: %d %s", created.Code, created.Body.String())
				}
				body := decodeJSONMap(t, created)
				want := restricted == true
				if body["default_routing_disabled"] != want {
					t.Fatalf("created policy = %v, want %v", body["default_routing_disabled"], want)
				}
				id := body["id"].(string)
				path := "/api/v1/subscriptions/" + id
				for _, value := range []bool{true, false} {
					patched := doJSONRequest(t, srv, http.MethodPatch, path, map[string]any{"default_routing_disabled": value}, true)
					if patched.Code != http.StatusOK {
						t.Fatalf("patch: %d %s", patched.Code, patched.Body.String())
					}
					body = decodeJSONMap(t, doJSONRequest(t, srv, http.MethodGet, path, nil, true))
					if body["default_routing_disabled"] != value || cp.SubMgr.Lookup(id).DefaultRoutingDisabled() != value {
						t.Fatalf("round trip policy = %v, want %v", body["default_routing_disabled"], value)
					}
					rows, err := cp.Engine.ListSubscriptions()
					if err != nil {
						t.Fatal(err)
					}
					found := false
					for _, row := range rows {
						if row.ID == id {
							found = true
							if row.DefaultRoutingDisabled != value {
								t.Fatalf("persisted policy = %v, want %v", row.DefaultRoutingDisabled, value)
							}
							break
						}
					}
					if !found {
						t.Fatalf("subscription %s missing from persistence", id)
					}
				}
				for _, invalid := range []any{nil, "true", 1} {
					patched := doJSONRequest(t, srv, http.MethodPatch, path, map[string]any{"default_routing_disabled": invalid}, true)
					if patched.Code != http.StatusBadRequest {
						t.Fatalf("invalid policy %v: %d %s", invalid, patched.Code, patched.Body.String())
					}
				}
			}
		})
	}
	created := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name": "invalid", "url": "https://example.com/sub", "default_routing_disabled": "true",
	}, true)
	if created.Code != http.StatusBadRequest {
		t.Fatalf("invalid create: %d %s", created.Code, created.Body.String())
	}
}

func TestSubscriptionDefaultRouting_ViewsPreviewAndStickyRoutes(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	created := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name": "Restricted", "url": "https://example.com/sub",
	}, true)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	id := decodeJSONMap(t, created)["id"].(string)
	sub := cp.SubMgr.Lookup(id)
	raw := []byte(`{"type":"http","server":"127.0.0.1","server_port":1080}`)
	hash := node.HashFromRawOptions(raw)
	sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{Tags: []string{"us", "eu"}})
	cp.Pool.AddNodeFromSub(hash, raw, id)
	entry, ok := cp.Pool.GetEntry(hash)
	if !ok {
		t.Fatal("node missing")
	}
	outbound := testutil.NewNoopOutbound()
	entry.Outbound.Store(&outbound)
	entry.SetEgressIP(netip.MustParseAddr("203.0.113.1"))
	entry.CircuitOpenSince.Store(0)
	entry.LatencyTable.LoadEntry("example.com", node.DomainLatencyStats{Ewma: time.Millisecond, LastUpdated: time.Now()})

	type filterCase struct {
		name       string
		rules      []string
		ordinary   bool
		restricted bool
		id         string
	}
	filters := []filterCase{
		{name: "Empty", ordinary: true},
		{name: "Explicit", rules: []string{"^Restricted/us$"}, ordinary: true, restricted: true},
		{name: "Exclusion", rules: []string{"!expired"}, ordinary: true},
		{name: "Miss", rules: []string{"jp"}},
		{name: "Wildcard", rules: []string{".*"}, ordinary: true, restricted: true},
		{name: "Blank", rules: []string{""}, ordinary: true},
		{name: "BlankAndMiss", rules: []string{"", "jp"}, ordinary: true},
		{name: "Veto", rules: []string{"us", "!eu"}},
	}
	for i := range filters {
		filter := &filters[i]
		platform, err := cp.CreatePlatform(service.CreatePlatformRequest{Name: &filter.name, RegexFilters: filter.rules})
		if err != nil {
			t.Fatal(err)
		}
		filter.id = platform.ID
	}
	check := func(restricted bool) {
		t.Helper()
		for _, filter := range filters {
			want := filter.ordinary
			if restricted {
				want = filter.restricted
			}
			platform, ok := cp.Pool.GetPlatform(filter.id)
			if !ok || platform.View().Contains(hash) != want {
				t.Fatalf("%s restricted=%v: view should contain node=%v", filter.name, restricted, want)
			}
			for _, request := range []service.PreviewFilterRequest{
				{PlatformID: &filter.id},
				{PlatformSpec: &service.PlatformSpecFilter{RegexFilters: filter.rules}},
			} {
				preview, err := cp.PreviewFilter(request)
				if err != nil || (len(preview) == 1) != want {
					t.Fatalf("%s restricted=%v: preview=%v error=%v want node=%v", filter.name, restricted, preview, err, want)
				}
			}
			for _, account := range []string{"", "sticky-account"} {
				result, err := cp.Router.RouteRequest(filter.name, account, "example.com:443")
				if (err == nil) != want || (want && result.NodeHash != hash) {
					t.Fatalf("%s restricted=%v account=%q: route=%v error=%v want node=%v", filter.name, restricted, account, result, err, want)
				}
			}
		}
	}
	check(false)
	for _, value := range []bool{true, false, true} {
		patched := doJSONRequest(t, srv, http.MethodPatch, "/api/v1/subscriptions/"+id, map[string]any{"default_routing_disabled": value}, true)
		if patched.Code != http.StatusOK {
			t.Fatalf("patch: %d %s", patched.Code, patched.Body.String())
		}
		body := decodeJSONMap(t, patched)
		if body["enabled"] != true || body["healthy_node_count"] != float64(1) || cp.Pool.IsNodeDisabled(hash) {
			t.Fatalf("routing policy must preserve enabled and healthy state: %v", body)
		}
		check(value)
	}

	// The shared node becomes default-routable as soon as another live holder
	// allows it. Disabled, evicted, and removed holders cannot grant permission.
	ordinary := subscription.NewSubscription("ordinary", "Ordinary", "https://example.com/ordinary", true, false)
	ordinary.ManagedNodes().StoreNode(hash, subscription.ManagedNode{Tags: []string{"alias"}})
	cp.SubMgr.Register(ordinary)
	cp.Pool.AddNodeFromSub(hash, raw, ordinary.ID)
	check(false)
	ordinary.SetEnabled(false)
	cp.Pool.RebuildAllPlatforms()
	check(true)
	ordinary.SetEnabled(true)
	ordinary.ManagedNodes().StoreNode(hash, subscription.ManagedNode{Tags: []string{"alias"}, Evicted: true})
	cp.Pool.RebuildAllPlatforms()
	check(true)
	ordinary.ManagedNodes().Delete(hash)
	cp.Pool.RebuildAllPlatforms()
	check(true)
}
