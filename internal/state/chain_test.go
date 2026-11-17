package state

import (
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/model"
)

func TestSubscriptionChainMigrationAndConstraints(t *testing.T) {
	repo := newTestStateRepo(t)
	sub := model.Subscription{ID: "chain", Name: "chain", URL: "https://example.com", UpdateIntervalNs: int64(time.Minute)}
	if err := repo.UpsertSubscription(sub); err != nil {
		t.Fatal(err)
	}
	// Recreate a real pre-feature version and upgrade its existing row.
	down, err := migrationsFS.ReadFile("migrations/state/000010_subscription_chain_proxy.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(string(down)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec("UPDATE schema_migrations SET version = 9"); err != nil {
		t.Fatal(err)
	}
	if err := MigrateStateDB(repo.db); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.ListSubscriptions()
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v error=%v", rows, err)
	}
	if rows[0].ChainProxy || rows[0].ChainRelay {
		t.Fatal("migrated flags must default to false")
	}
	sub.ChainRelay = true
	if err := repo.UpsertSubscription(sub); err != nil {
		t.Fatal(err)
	}
	rows, err = repo.ListSubscriptions()
	if err != nil || !rows[0].ChainRelay || rows[0].ChainProxy {
		t.Fatalf("persisted flags: %v %v", rows, err)
	}
	sub.ChainProxy = true
	if err := repo.UpsertSubscription(sub); err == nil {
		t.Fatal("repository accepted conflicting flags")
	}
	if _, err := repo.db.Exec("UPDATE subscriptions SET chain_proxy = 1, chain_relay = 1"); err == nil {
		t.Fatal("database accepted conflicting flags")
	}
}

func TestCachedNodeRetainsOutboundMode(t *testing.T) {
	repo := newTestCacheRepo(t)
	if err := repo.BulkUpsertNodesDynamic([]model.NodeDynamic{{Hash: "chain", ChainProxy: true}}); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.LoadAllNodesDynamic()
	if err != nil || len(rows) != 1 || !rows[0].ChainProxy {
		t.Fatalf("mode round trip: %v %v", rows, err)
	}
}

func TestCacheChainMigrationPreservesMeasurements(t *testing.T) {
	repo := newTestCacheRepo(t)
	down, err := migrationsFS.ReadFile("migrations/cache/000003_nodes_dynamic_chain_proxy.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	// Recreate version 2 with real measurements predating this feature.
	if _, err := repo.db.Exec(string(down)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(`
		UPDATE schema_migrations SET version = 2;
		INSERT INTO nodes_dynamic (hash, egress_ip) VALUES ('node', '203.0.113.1');
		INSERT INTO node_latency (node_hash, domain, ewma_ns, last_updated_ns)
		VALUES ('node', 'example.com', 7000000, 123);
	`); err != nil {
		t.Fatal(err)
	}
	if err := MigrateCacheDB(repo.db); err != nil {
		t.Fatal(err)
	}
	nodes, err := repo.LoadAllNodesDynamic()
	if err != nil || len(nodes) != 1 || nodes[0].ChainProxy || nodes[0].EgressIP != "203.0.113.1" {
		t.Fatalf("migrated node: %v, error=%v", nodes, err)
	}
	latencies, err := repo.LoadAllNodeLatency()
	if err != nil || len(latencies) != 1 || latencies[0].ChainProxy || latencies[0].EwmaNs != 7000000 {
		t.Fatalf("migrated latency: %v, error=%v", latencies, err)
	}
	var version int
	if err := repo.db.QueryRow("SELECT version FROM schema_migrations").Scan(&version); err != nil || version != 3 {
		t.Fatalf("cache version=%d, error=%v", version, err)
	}
	if _, err := repo.db.Exec(string(down)); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"nodes_dynamic", "node_latency"} {
		if exists, err := hasTableColumn(repo.db, table, "chain_proxy"); err != nil || exists {
			t.Fatalf("rollback %s.chain_proxy: exists=%v, error=%v", table, exists, err)
		}
	}
	var latency int64
	if err := repo.db.QueryRow("SELECT ewma_ns FROM node_latency WHERE node_hash = 'node'").Scan(&latency); err != nil || latency != 7000000 {
		t.Fatalf("rollback lost measurement: %d, error=%v", latency, err)
	}
}
