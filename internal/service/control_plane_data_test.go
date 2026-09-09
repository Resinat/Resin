package service

import (
	"encoding/json"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/config"
	"github.com/Resinat/Resin/internal/state"
	"github.com/Resinat/Resin/internal/topology"
)

func newDataTestControlPlane(t *testing.T) *ControlPlaneService {
	t.Helper()

	dir := t.TempDir()
	engine, closer, err := state.PersistenceBootstrap(
		filepath.Join(dir, "state"),
		filepath.Join(dir, "cache"),
	)
	if err != nil {
		t.Fatalf("PersistenceBootstrap: %v", err)
	}
	t.Cleanup(func() {
		_ = closer.Close()
	})

	subMgr := topology.NewSubscriptionManager()
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              func(netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})

	return &ControlPlaneService{
		Engine: engine,
		Pool:   pool,
		SubMgr: subMgr,
		Scheduler: topology.NewSubscriptionScheduler(topology.SchedulerConfig{
			SubManager: subMgr,
			Pool:       pool,
			Fetcher:    func(string) ([]byte, error) { return []byte("[]"), nil },
		}),
		EnvCfg: &config.EnvConfig{
			DefaultPlatformStickyTTL:              30 * time.Minute,
			DefaultPlatformRegexFilters:           []string{},
			DefaultPlatformRegionFilters:          []string{},
			DefaultPlatformReverseProxyMissAction: "TREAT_AS_EMPTY",
			DefaultPlatformAllocationPolicy:       "BALANCED",
		},
	}
}

func exportTestEntry(t *testing.T, values map[string]any) ExportEntry {
	t.Helper()

	entry := make(ExportEntry, len(values))
	for key, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s: %v", key, err)
		}
		entry[key] = data
	}
	return entry
}

func entryStringValue(t *testing.T, entry ExportEntry, key string) string {
	t.Helper()

	raw, ok := entry[key]
	if !ok {
		t.Fatalf("missing key %q in entry %v", key, entry)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	return value
}

func entryBoolValue(t *testing.T, entry ExportEntry, key string) bool {
	t.Helper()

	raw, ok := entry[key]
	if !ok {
		t.Fatalf("missing key %q in entry %v", key, entry)
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	return value
}

func TestExportData_UsesConfigFieldAllowlists(t *testing.T) {
	cp := newDataTestControlPlane(t)

	platformName := "export-platform"
	disablePassiveBreaker := true
	if _, err := cp.CreatePlatform(CreatePlatformRequest{
		Name:                          &platformName,
		PassiveCircuitBreakerDisabled: &disablePassiveBreaker,
	}); err != nil {
		t.Fatalf("CreatePlatform: %v", err)
	}

	subName := "export-subscription"
	subURL := "https://example.com/sub"
	incrementalAliveNodes := true
	if _, err := cp.CreateSubscription(CreateSubscriptionRequest{
		Name:                  &subName,
		URL:                   &subURL,
		IncrementalAliveNodes: &incrementalAliveNodes,
	}); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	payload, err := cp.ExportData()
	if err != nil {
		t.Fatalf("ExportData: %v", err)
	}
	if len(payload.Platforms) != 1 {
		t.Fatalf("exported platforms = %d, want 1", len(payload.Platforms))
	}
	if len(payload.Subscriptions) != 1 {
		t.Fatalf("exported subscriptions = %d, want 1", len(payload.Subscriptions))
	}

	platformEntry := payload.Platforms[0]
	for key := range platformPatchAllowedFields {
		if _, ok := platformEntry[key]; !ok {
			t.Fatalf("platform export missing allowed field %q", key)
		}
	}
	for _, key := range []string{"id", "routable_node_count", "updated_at"} {
		if _, ok := platformEntry[key]; ok {
			t.Fatalf("platform export includes read-only field %q", key)
		}
	}
	if !entryBoolValue(t, platformEntry, "passive_circuit_breaker_disabled") {
		t.Fatal("expected passive_circuit_breaker_disabled to round-trip true")
	}

	subEntry := payload.Subscriptions[0]
	for key := range subscriptionPatchAllowedFields {
		if _, ok := subEntry[key]; !ok {
			t.Fatalf("subscription export missing allowed field %q", key)
		}
	}
	if _, ok := subEntry["source_type"]; !ok {
		t.Fatal("subscription export missing source_type")
	}
	for _, key := range []string{"id", "node_count", "healthy_node_count", "created_at", "last_checked", "last_updated", "last_error", "usage"} {
		if _, ok := subEntry[key]; ok {
			t.Fatalf("subscription export includes read-only field %q", key)
		}
	}
	if !entryBoolValue(t, subEntry, "incremental_alive_nodes") {
		t.Fatal("expected incremental_alive_nodes to round-trip true")
	}
}

func TestImportData_OverwriteDynamicConfigFields(t *testing.T) {
	cp := newDataTestControlPlane(t)

	platformName := "overwrite-platform"
	if _, err := cp.CreatePlatform(CreatePlatformRequest{Name: &platformName}); err != nil {
		t.Fatalf("CreatePlatform: %v", err)
	}
	subName := "overwrite-subscription"
	subURL := "https://example.com/overwrite-sub"
	if _, err := cp.CreateSubscription(CreateSubscriptionRequest{Name: &subName, URL: &subURL}); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	payload := ExportPayload{
		Platforms: []ExportEntry{
			exportTestEntry(t, map[string]any{
				"name":                             platformName,
				"passive_circuit_breaker_disabled": true,
			}),
		},
		Subscriptions: []ExportEntry{
			exportTestEntry(t, map[string]any{
				"name":                    subName,
				"source_type":             "remote",
				"incremental_alive_nodes": true,
			}),
		},
	}
	result, err := cp.ImportData(payload, "overwrite")
	if err != nil {
		t.Fatalf("ImportData: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("ImportData errors = %v", result.Errors)
	}
	if result.PlatformsOverwritten != 1 || result.SubscriptionsOverwritten != 1 {
		t.Fatalf("overwrite counts = %+v, want 1 platform and 1 subscription", result)
	}

	exported, err := cp.ExportData()
	if err != nil {
		t.Fatalf("ExportData: %v", err)
	}
	if !entryBoolValue(t, exported.Platforms[0], "passive_circuit_breaker_disabled") {
		t.Fatal("expected platform passive circuit breaker setting to be overwritten")
	}
	if !entryBoolValue(t, exported.Subscriptions[0], "incremental_alive_nodes") {
		t.Fatal("expected subscription incremental mode to be overwritten")
	}
}

func TestImportData_OverwriteRejectsSubscriptionSourceTypeChange(t *testing.T) {
	cp := newDataTestControlPlane(t)

	subName := "source-mismatch"
	sourceType := "local"
	content := "1.2.3.4:8080"
	if _, err := cp.CreateSubscription(CreateSubscriptionRequest{
		Name:       &subName,
		SourceType: &sourceType,
		Content:    &content,
	}); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	result, err := cp.ImportData(ExportPayload{
		Subscriptions: []ExportEntry{
			exportTestEntry(t, map[string]any{
				"name":        subName,
				"source_type": "remote",
				"url":         "https://example.com/remote-sub",
			}),
		},
	}, "overwrite")
	if err != nil {
		t.Fatalf("ImportData: %v", err)
	}
	if result.SubscriptionsOverwritten != 0 {
		t.Fatalf("subscriptions_overwritten = %d, want 0", result.SubscriptionsOverwritten)
	}
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "source_type mismatch") {
		t.Fatalf("errors = %v, want source_type mismatch", result.Errors)
	}

	exported, err := cp.ExportData()
	if err != nil {
		t.Fatalf("ExportData: %v", err)
	}
	if got := entryStringValue(t, exported.Subscriptions[0], "source_type"); got != "local" {
		t.Fatalf("source_type = %q, want local", got)
	}
}
