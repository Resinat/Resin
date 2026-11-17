package probe

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/topology"
)

func TestUnavailableRelayDoesNotCountAsNodeFailure(t *testing.T) {
	pool := topology.NewGlobalNodePool(topology.PoolConfig{MaxLatencyTableEntries: 8, MaxConsecutiveFailures: func() int { return 1 }})
	raw := []byte(`{"type":"http","server":"landing"}`)
	hash := node.HashFromRawOptions(raw)
	pool.AddNodeFromSub(hash, raw, "sub")
	e, _ := pool.GetEntry(hash)
	storeOutbound(e)
	pool.RecordResult(hash, true)
	m := NewProbeManager(ProbeConfig{Pool: pool, Fetcher: func(node.Hash, string) ([]byte, time.Duration, error) {
		return nil, 0, fmt.Errorf("dial: %w", routing.ErrNoAvailableRelay)
	}})
	if err := m.performLatencyProbe(hash, "https://speed.example"); !errors.Is(err, routing.ErrNoAvailableRelay) {
		t.Fatal(err)
	}
	_, _, _ = m.performEgressProbe(hash)
	if e.FailureCount.Load() != 0 || e.IsCircuitOpen() {
		t.Fatal("relay unavailability changed landing failure state")
	}
	if e.LastLatencyProbeAttempt.Load() == 0 || e.LastEgressUpdateAttempt.Load() == 0 {
		t.Fatal("unavailable attempts must retain scheduling timestamps")
	}
}

func TestFirstHealthyRelayImmediatelyRecoversWaitingChain(t *testing.T) {
	subs := topology.NewSubscriptionManager()
	landing := subscription.NewSubscription("landing", "landing", "", true, false)
	landing.SetChainConfig(true, false)
	subs.Register(landing)
	relay := subscription.NewSubscription("relay", "relay", "", true, false)
	relay.SetChainConfig(false, true)
	subs.Register(relay)
	pool := topology.NewGlobalNodePool(topology.PoolConfig{SubLookup: subs.Lookup, MaxLatencyTableEntries: 8, MaxConsecutiveFailures: func() int { return 1 }})
	add := func(sub *subscription.Subscription, server string) node.Hash {
		raw := []byte(`{"type":"http","server":"` + server + `"}`)
		hash := node.HashFromRawOptions(raw)
		sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{})
		pool.AddNodeFromSub(hash, raw, sub.ID)
		e, _ := pool.GetEntry(hash)
		storeOutbound(e)
		return hash
	}
	h, jump := add(landing, "landing"), add(relay, "jump")
	m := NewProbeManager(ProbeConfig{Pool: pool, Concurrency: 1, Fetcher: func(hash node.Hash, _ string) ([]byte, time.Duration, error) {
		if hash != jump && pool.RelayView("", "tcp").Size() == 0 {
			return nil, 0, routing.ErrNoAvailableRelay
		}
		return []byte("ip=203.0.113.1\nloc=US\n"), time.Millisecond, nil
	}})
	pool.SetOnRelayAvailable(m.TriggerChainRecovery)
	if _, _, err := m.performEgressProbe(h); !errors.Is(err, routing.ErrNoAvailableRelay) {
		t.Fatal(err)
	}
	m.Start()
	defer m.Stop()
	if _, _, err := m.performEgressProbe(jump); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		e, _ := pool.GetEntry(h)
		if !e.IsCircuitOpen() && e.GetEgressIP().IsValid() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("chain recovery waited for the periodic probe interval")
}

func TestFailedOutboundInitializationRecordsSchedulingAttempt(t *testing.T) {
	pool := topology.NewGlobalNodePool(topology.PoolConfig{MaxConsecutiveFailures: func() int { return 1 }})
	raw := []byte(`{"type":"http","server":"unbuilt"}`)
	hash := node.HashFromRawOptions(raw)
	pool.AddNodeFromSub(hash, raw, "sub")
	m := NewProbeManager(ProbeConfig{Pool: pool, EnsureOutbound: func(node.Hash) {}})
	m.executeTask(probeTask{key: probeTaskKey{hash: hash, kind: probeTaskKindEgress}})
	e, _ := pool.GetEntry(hash)
	if e.LastEgressUpdateAttempt.Load() == 0 || e.FailureCount.Load() != 0 {
		t.Fatal("failed initialization was not rate limited independently of health")
	}
}

func TestChainRecoveryWithFullQueueRemainsDue(t *testing.T) {
	pool := topology.NewGlobalNodePool(topology.PoolConfig{MaxConsecutiveFailures: func() int { return 1 }})
	raw := []byte(`{"type":"http","server":"landing"}`)
	hash := node.HashFromRawOptions(raw)
	pool.AddNodeFromSub(hash, raw, "sub")
	entry, _ := pool.GetEntry(hash)
	entry.UpdateRoutingRole(node.RoleChain)
	entry.LastEgressUpdateAttempt.Store(time.Now().UnixNano())
	entry.LastLatencyProbeAttempt.Store(time.Now().UnixNano())
	m := NewProbeManager(ProbeConfig{Pool: pool, QueueCapacity: 1})
	defer m.Stop()
	m.TriggerImmediateEgressProbe(node.Hash{99})
	m.TriggerChainRecovery()
	if entry.LastEgressUpdateAttempt.Load() != 0 || entry.LastLatencyProbeAttempt.Load() != 0 {
		t.Fatal("full queue postponed chain recovery until the probe interval")
	}
}

func TestProbeFromOldModeCannotRestoreHealth(t *testing.T) {
	pool := topology.NewGlobalNodePool(topology.PoolConfig{MaxLatencyTableEntries: 8, MaxConsecutiveFailures: func() int { return 1 }})
	raw := []byte(`{"type":"http","server":"landing"}`)
	hash := node.HashFromRawOptions(raw)
	pool.AddNodeFromSub(hash, raw, "sub")
	e, _ := pool.GetEntry(hash)
	storeOutbound(e)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	m := NewProbeManager(ProbeConfig{Pool: pool, Fetcher: func(node.Hash, string) ([]byte, time.Duration, error) {
		close(started)
		<-release
		return []byte("ip=203.0.113.1\nloc=US\n"), time.Millisecond, nil
	}})
	go func() { defer close(done); _, _, _ = m.performEgressProbe(hash) }()
	<-started
	e.UpdateRoutingRole(node.RoleChain)
	close(release)
	<-done
	if e.GetEgressIP().IsValid() || e.HasLatency() || !e.IsCircuitOpen() {
		t.Fatal("old mode probe restored new mode health")
	}
}
