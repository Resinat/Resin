package main

import (
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/config"
	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/state"
)

func TestBootstrapTopology_RestoresDefaultRoutingPolicy(t *testing.T) {
	stateDir, cacheDir := t.TempDir(), t.TempDir()
	engine, closer, err := state.PersistenceBootstrap(stateDir, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closer.Close() })
	if err := engine.UpsertSubscription(model.Subscription{
		ID: "restricted", Name: "restricted", URL: "https://example.com/sub",
		UpdateIntervalNs: int64(time.Minute), Enabled: true, DefaultRoutingDisabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	restartedEngine, restartedCloser, err := state.PersistenceBootstrap(stateDir, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restartedCloser.Close() })
	subs, pool := newBootstrapTestRuntime(config.NewDefaultRuntimeConfig())
	if err := bootstrapTopology(restartedEngine, subs, pool, newDefaultPlatformEnvConfig()); err != nil {
		t.Fatal(err)
	}
	sub := subs.Lookup("restricted")
	if sub == nil || !sub.Enabled() || !sub.DefaultRoutingDisabled() {
		t.Fatal("subscription routing policy was not restored after restart")
	}
}
