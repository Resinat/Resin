package state

import (
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/model"
)

func TestSubscriptionDefaultRoutingMigrationAndPersistence(t *testing.T) {
	repo := newTestStateRepo(t)
	sub := model.Subscription{ID: "restricted", Name: "restricted", URL: "https://example.com", UpdateIntervalNs: int64(time.Minute)}
	if err := repo.UpsertSubscription(sub); err != nil {
		t.Fatal(err)
	}
	down, err := migrationsFS.ReadFile("migrations/state/000011_subscription_default_routing.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(string(down)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec("UPDATE schema_migrations SET version = 10"); err != nil {
		t.Fatal(err)
	}
	if err := MigrateStateDB(repo.db); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.ListSubscriptions()
	if err != nil || len(rows) != 1 || rows[0].DefaultRoutingDisabled {
		t.Fatalf("legacy subscriptions must retain default routing: %v, %v", rows, err)
	}
	for _, restricted := range []bool{true, false} {
		sub.DefaultRoutingDisabled = restricted
		if err := repo.UpsertSubscription(sub); err != nil {
			t.Fatal(err)
		}
		rows, err = repo.ListSubscriptions()
		if err != nil || len(rows) != 1 || rows[0].DefaultRoutingDisabled != restricted {
			t.Fatalf("routing policy round trip: %v, %v", rows, err)
		}
	}
	if _, err := repo.db.Exec("UPDATE subscriptions SET default_routing_disabled = 2"); err == nil {
		t.Fatal("database accepted a non-boolean routing policy")
	}
	if _, err := repo.db.Exec(string(down)); err != nil {
		t.Fatal(err)
	}
	if exists, err := hasTableColumn(repo.db, "subscriptions", "default_routing_disabled"); err != nil || exists {
		t.Fatalf("rollback: column exists=%v, error=%v", exists, err)
	}
}
