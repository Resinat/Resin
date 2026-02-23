package state

import (
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/config"
	"github.com/Resinat/Resin/internal/model"
)

// helper: create a state.db in a temp dir, init DDL, return StateRepo + cleanup.
func newTestStateRepo(t *testing.T) *StateRepo {
	t.Helper()
	dir := t.TempDir()
	db, err := OpenDB(dir + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := InitDB(db, CreateStateDDL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return newStateRepo(db)
}

// --- system_config ---

func TestStateRepo_SystemConfig_RoundTrip(t *testing.T) {
	repo := newTestStateRepo(t)

	// Initially empty.
	cfg, ver, err := repo.GetSystemConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil || ver != 0 {
		t.Fatalf("expected nil config and version 0, got %v, %d", cfg, ver)
	}

	// Save.
	c := config.NewDefaultRuntimeConfig()
	c.UserAgent = "test-agent"
	now := time.Now().UnixNano()
	if err := repo.SaveSystemConfig(c, 1, now); err != nil {
		t.Fatal(err)
	}

	// Read back.
	cfg, ver, err = repo.GetSystemConfig()
	if err != nil {
		t.Fatal(err)
	}
	if ver != 1 {
		t.Fatalf("expected version 1, got %d", ver)
	}
	if cfg.UserAgent != "test-agent" {
		t.Fatalf("expected user_agent test-agent, got %s", cfg.UserAgent)
	}

	// Upsert (idempotent, bump version).
	c.UserAgent = "updated-agent"
	if err := repo.SaveSystemConfig(c, 2, now+1); err != nil {
		t.Fatal(err)
	}
	cfg, ver, err = repo.GetSystemConfig()
	if err != nil {
		t.Fatal(err)
	}
	if ver != 2 || cfg.UserAgent != "updated-agent" {
		t.Fatalf("expected version 2 + updated-agent, got %d + %s", ver, cfg.UserAgent)
	}
}

// --- platforms ---

func TestStateRepo_Platforms_CRUD(t *testing.T) {
	repo := newTestStateRepo(t)
	now := time.Now().UnixNano()

	p := model.Platform{
		ID: "plat-1", Name: "Default", StickyTTLNs: 1000,
		RegexFilters: []string{}, RegionFilters: []string{},
		ReverseProxyMissAction: "RANDOM", AllocationPolicy: "BALANCED",
		UpdatedAtNs: now,
	}
	if err := repo.UpsertPlatform(p); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetPlatform("plat-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Default" {
		t.Fatalf("unexpected get result: %+v", got)
	}

	// List.
	list, err := repo.ListPlatforms()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "Default" {
		t.Fatalf("unexpected list: %+v", list)
	}

	// Idempotent upsert (update same ID).
	p.Name = "Default-Renamed"
	if err := repo.UpsertPlatform(p); err != nil {
		t.Fatal(err)
	}
	list, err = repo.ListPlatforms()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "Default-Renamed" {
		t.Fatalf("expected renamed platform, got %+v", list)
	}

	// Delete.
	if err := repo.DeletePlatform("plat-1"); err != nil {
		t.Fatal(err)
	}
	list, err = repo.ListPlatforms()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty list after delete, got %+v", list)
	}
	if _, err := repo.GetPlatform("plat-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestStateRepo_Platform_NameUniqueViolation(t *testing.T) {
	repo := newTestStateRepo(t)
	now := time.Now().UnixNano()

	p1 := model.Platform{
		ID: "plat-1", Name: "SameName", StickyTTLNs: 1000,
		RegexFilters: []string{}, RegionFilters: []string{},
		ReverseProxyMissAction: "RANDOM", AllocationPolicy: "BALANCED",
		UpdatedAtNs: now,
	}
	if err := repo.UpsertPlatform(p1); err != nil {
		t.Fatal(err)
	}

	// Different ID, same name → should fail with ErrConflict.
	p2 := p1
	p2.ID = "plat-2"
	err := repo.UpsertPlatform(p2)
	if err == nil {
		t.Fatal("expected ErrConflict for same name with different ID")
	}
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}

	// Original should still exist untouched.
	list, _ := repo.ListPlatforms()
	if len(list) != 1 || list[0].ID != "plat-1" {
		t.Fatalf("expected original plat-1 to survive, got %+v", list)
	}
}

func TestStateRepo_Platform_ValidationRejectsInvalidRegex(t *testing.T) {
	repo := newTestStateRepo(t)
	now := time.Now().UnixNano()

	base := model.Platform{
		ID: "plat-1", Name: "Test", StickyTTLNs: 1000,
		RegexFilters: []string{}, RegionFilters: []string{},
		ReverseProxyMissAction: "RANDOM", AllocationPolicy: "BALANCED",
		UpdatedAtNs: now,
	}

	// Uncompilable regex.
	bad := base
	bad.RegexFilters = []string{"(unclosed"}
	if err := repo.UpsertPlatform(bad); err == nil {
		t.Fatal("expected error for uncompilable regex")
	}

	// Invalid region_filters.
	bad = base
	bad.RegionFilters = []string{""}
	if err := repo.UpsertPlatform(bad); err == nil {
		t.Fatal("expected error for invalid region_filters")
	}

	// Valid config should still succeed.
	base.RegexFilters = []string{"^ss$", "vmess"}
	base.RegionFilters = []string{"us", "jp"}
	if err := repo.UpsertPlatform(base); err != nil {
		t.Fatalf("valid platform rejected: %v", err)
	}

	// DB should have exactly 1 platform.
	list, _ := repo.ListPlatforms()
	if len(list) != 1 {
		t.Fatalf("expected 1 platform, got %d", len(list))
	}
}

// --- subscriptions ---

func TestStateRepo_Subscriptions_CRUD(t *testing.T) {
	repo := newTestStateRepo(t)
	now := time.Now().UnixNano()

	s := model.Subscription{
		ID: "sub-1", Name: "MySub", URL: "https://example.com/sub",
		UpdateIntervalNs: int64(30 * time.Second), Enabled: true,
		Ephemeral: false, EphemeralNodeEvictDelayNs: int64(72 * time.Hour), CreatedAtNs: now, UpdatedAtNs: now,
	}
	if err := repo.UpsertSubscription(s); err != nil {
		t.Fatal(err)
	}

	list, err := repo.ListSubscriptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].URL != "https://example.com/sub" {
		t.Fatalf("unexpected list: %+v", list)
	}

	// Update.
	s.URL = "https://example.com/sub-v2"
	if err := repo.UpsertSubscription(s); err != nil {
		t.Fatal(err)
	}
	list, _ = repo.ListSubscriptions()
	if list[0].URL != "https://example.com/sub-v2" {
		t.Fatalf("expected updated URL, got %s", list[0].URL)
	}

	// Delete.
	if err := repo.DeleteSubscription("sub-1"); err != nil {
		t.Fatal(err)
	}
	list, _ = repo.ListSubscriptions()
	if len(list) != 0 {
		t.Fatal("expected empty after delete")
	}
}

func TestStateRepo_Subscription_CreatedAtNsPreserved(t *testing.T) {
	repo := newTestStateRepo(t)
	originalCreatedAt := int64(1000000)

	s := model.Subscription{
		ID: "sub-1", Name: "MySub", URL: "https://example.com",
		UpdateIntervalNs: int64(30 * time.Second), Enabled: true,
		Ephemeral: false, EphemeralNodeEvictDelayNs: int64(72 * time.Hour),
		CreatedAtNs: originalCreatedAt, UpdatedAtNs: originalCreatedAt,
	}
	if err := repo.UpsertSubscription(s); err != nil {
		t.Fatal(err)
	}

	// Upsert again with a DIFFERENT created_at_ns — it should be ignored.
	s.CreatedAtNs = int64(9999999)
	s.URL = "https://example.com/v2"
	s.UpdatedAtNs = int64(2000000)
	if err := repo.UpsertSubscription(s); err != nil {
		t.Fatal(err)
	}

	list, err := repo.ListSubscriptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(list))
	}
	if list[0].CreatedAtNs != originalCreatedAt {
		t.Fatalf("created_at_ns was overwritten: expected %d, got %d", originalCreatedAt, list[0].CreatedAtNs)
	}
	if list[0].URL != "https://example.com/v2" {
		t.Fatalf("URL should have been updated, got %s", list[0].URL)
	}
	if list[0].UpdatedAtNs != int64(2000000) {
		t.Fatalf("updated_at_ns should have been updated, got %d", list[0].UpdatedAtNs)
	}
}

// --- account_header_rules ---

func TestStateRepo_AccountHeaderRules_CRUD(t *testing.T) {
	repo := newTestStateRepo(t)
	now := time.Now().UnixNano()

	r := model.AccountHeaderRule{
		URLPrefix: "api.example.com/v1", Headers: []string{"Authorization"}, UpdatedAtNs: now,
	}
	if _, err := repo.UpsertAccountHeaderRuleWithCreated(r); err != nil {
		t.Fatal(err)
	}

	list, err := repo.ListAccountHeaderRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || len(list[0].Headers) != 1 || list[0].Headers[0] != "Authorization" {
		t.Fatalf("unexpected list: %+v", list)
	}

	// Update.
	r.Headers = []string{"x-api-key"}
	if _, err := repo.UpsertAccountHeaderRuleWithCreated(r); err != nil {
		t.Fatal(err)
	}
	list, _ = repo.ListAccountHeaderRules()
	if len(list[0].Headers) != 1 || list[0].Headers[0] != "x-api-key" {
		t.Fatalf("expected updated headers, got %v", list[0].Headers)
	}

	// Delete.
	if err := repo.DeleteAccountHeaderRule("api.example.com/v1"); err != nil {
		t.Fatal(err)
	}
	list, _ = repo.ListAccountHeaderRules()
	if len(list) != 0 {
		t.Fatal("expected empty after delete")
	}
}

func TestStateRepo_AccountHeaderRules_UpsertCreatedFlag(t *testing.T) {
	repo := newTestStateRepo(t)
	now := time.Now().UnixNano()

	r := model.AccountHeaderRule{
		URLPrefix:   "api.example.com/v1",
		Headers:     []string{"Authorization"},
		UpdatedAtNs: now,
	}
	created, err := repo.UpsertAccountHeaderRuleWithCreated(r)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected first upsert to report created=true")
	}

	r.Headers = []string{"x-api-key"}
	r.UpdatedAtNs = now + 1
	created, err = repo.UpsertAccountHeaderRuleWithCreated(r)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("expected second upsert to report created=false")
	}
}

func TestStateRepo_EnsureAccountHeaderRule_InsertsOnlyWhenMissing(t *testing.T) {
	repo := newTestStateRepo(t)
	now := time.Now().UnixNano()

	created, err := repo.EnsureAccountHeaderRule(model.AccountHeaderRule{
		URLPrefix:   "*",
		Headers:     []string{"Authorization", "x-api-key"},
		UpdatedAtNs: now,
	})
	if err != nil {
		t.Fatalf("EnsureAccountHeaderRule first call: %v", err)
	}
	if !created {
		t.Fatal("expected first ensure call to create row")
	}

	created, err = repo.EnsureAccountHeaderRule(model.AccountHeaderRule{
		URLPrefix:   "*",
		Headers:     []string{"X-Should-Not-Overwrite"},
		UpdatedAtNs: now + 1,
	})
	if err != nil {
		t.Fatalf("EnsureAccountHeaderRule second call: %v", err)
	}
	if created {
		t.Fatal("expected second ensure call to skip existing row")
	}

	list, err := repo.ListAccountHeaderRules()
	if err != nil {
		t.Fatalf("ListAccountHeaderRules: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected exactly one rule, got %d", len(list))
	}
	if list[0].URLPrefix != "*" {
		t.Fatalf("url_prefix = %q, want %q", list[0].URLPrefix, "*")
	}
	if !reflect.DeepEqual(list[0].Headers, []string{"Authorization", "x-api-key"}) {
		t.Fatalf("headers = %v, want %v", list[0].Headers, []string{"Authorization", "x-api-key"})
	}
}

// --- concurrent writes ---

func TestStateRepo_ConcurrentWrites(t *testing.T) {
	repo := newTestStateRepo(t)
	now := time.Now().UnixNano()

	// Run 20 concurrent platform upserts on different IDs.
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func(i int) {
			p := model.Platform{
				ID: "plat-" + itoa(i), Name: "Platform-" + itoa(i),
				StickyTTLNs: 1000, RegexFilters: []string{}, RegionFilters: []string{},
				ReverseProxyMissAction: "RANDOM", AllocationPolicy: "BALANCED",
				UpdatedAtNs: now,
			}
			errs <- repo.UpsertPlatform(p)
		}(i)
	}

	for i := 0; i < 20; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent upsert failed: %v", err)
		}
	}

	list, _ := repo.ListPlatforms()
	if len(list) != 20 {
		t.Fatalf("expected 20 platforms, got %d", len(list))
	}
}

func itoa(i int) string {
	return strconv.Itoa(i)
}
