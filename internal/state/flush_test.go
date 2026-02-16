package state

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/resin-proxy/resin/internal/model"
)

func TestFlushWorker_ThresholdTriggered(t *testing.T) {
	engine, _, _ := newTestEngine(t)

	nodeStore := map[string]*model.NodeStatic{
		"n1": {Hash: "n1", RawOptionsJSON: `{}`, CreatedAtNs: 1},
		"n2": {Hash: "n2", RawOptionsJSON: `{}`, CreatedAtNs: 2},
		"n3": {Hash: "n3", RawOptionsJSON: `{}`, CreatedAtNs: 3},
	}
	readers := CacheReaders{
		ReadNodeStatic:       func(h string) *model.NodeStatic { return nodeStore[h] },
		ReadNodeDynamic:      func(h string) *model.NodeDynamic { return nil },
		ReadNodeLatency:      func(k NodeLatencyDirtyKey) *model.NodeLatency { return nil },
		ReadLease:            func(k LeaseDirtyKey) *model.Lease { return nil },
		ReadSubscriptionNode: func(k SubscriptionNodeDirtyKey) *model.SubscriptionNode { return nil },
	}

	// Threshold = 2, interval very long, check tick short.
	w := NewCacheFlushWorker(
		engine,
		readers,
		func() int { return 2 },
		func() time.Duration { return 1 * time.Hour },
		50*time.Millisecond,
	)
	w.Start()

	// Mark 3 entries (above threshold of 2).
	engine.MarkNodeStatic("n1")
	engine.MarkNodeStatic("n2")
	engine.MarkNodeStatic("n3")

	// Wait for flush cycle.
	time.Sleep(300 * time.Millisecond)

	// Check: dirty count should be 0 (flushed).
	if dc := engine.DirtyCount(); dc != 0 {
		t.Fatalf("expected dirty count 0 after threshold flush, got %d", dc)
	}

	// Verify in DB.
	nodes, _ := engine.LoadAllNodesStatic()
	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes in DB, got %d", len(nodes))
	}

	w.Stop()
}

func TestFlushWorker_PeriodicTriggered(t *testing.T) {
	engine, _, _ := newTestEngine(t)

	nodeStore := map[string]*model.NodeStatic{
		"n1": {Hash: "n1", RawOptionsJSON: `{}`, CreatedAtNs: 1},
	}
	readers := CacheReaders{
		ReadNodeStatic:       func(h string) *model.NodeStatic { return nodeStore[h] },
		ReadNodeDynamic:      func(h string) *model.NodeDynamic { return nil },
		ReadNodeLatency:      func(k NodeLatencyDirtyKey) *model.NodeLatency { return nil },
		ReadLease:            func(k LeaseDirtyKey) *model.Lease { return nil },
		ReadSubscriptionNode: func(k SubscriptionNodeDirtyKey) *model.SubscriptionNode { return nil },
	}

	// Threshold very high (won't trigger), interval short (will trigger).
	w := NewCacheFlushWorker(
		engine,
		readers,
		func() int { return 10000 },
		func() time.Duration { return 100 * time.Millisecond },
		50*time.Millisecond,
	)
	w.Start()

	// Mark 1 entry (below threshold of 10000).
	engine.MarkNodeStatic("n1")

	// Wait longer than interval for periodic flush.
	time.Sleep(400 * time.Millisecond)

	if dc := engine.DirtyCount(); dc != 0 {
		t.Fatalf("expected dirty count 0 after periodic flush, got %d", dc)
	}

	w.Stop()
}

func TestFlushWorker_SkipsEmptyDirty(t *testing.T) {
	engine, _, _ := newTestEngine(t)

	readers := CacheReaders{
		ReadNodeStatic:       func(h string) *model.NodeStatic { return nil },
		ReadNodeDynamic:      func(h string) *model.NodeDynamic { return nil },
		ReadNodeLatency:      func(k NodeLatencyDirtyKey) *model.NodeLatency { return nil },
		ReadLease:            func(k LeaseDirtyKey) *model.Lease { return nil },
		ReadSubscriptionNode: func(k SubscriptionNodeDirtyKey) *model.SubscriptionNode { return nil },
	}

	// Very short interval — if not skipping empty, would spam flushes.
	w := NewCacheFlushWorker(
		engine,
		readers,
		func() int { return 1 },
		func() time.Duration { return 10 * time.Millisecond },
		5*time.Millisecond,
	)
	w.Start()

	// No dirty marks. Let it run a few cycles.
	time.Sleep(100 * time.Millisecond)

	// Still 0 dirty.
	if dc := engine.DirtyCount(); dc != 0 {
		t.Fatalf("expected 0, got %d", dc)
	}

	w.Stop()
}

func TestFlushWorker_StopFinalFlush(t *testing.T) {
	engine, _, _ := newTestEngine(t)

	nodeStore := map[string]*model.NodeStatic{
		"n1": {Hash: "n1", RawOptionsJSON: `{}`, CreatedAtNs: 1},
	}
	readers := CacheReaders{
		ReadNodeStatic:       func(h string) *model.NodeStatic { return nodeStore[h] },
		ReadNodeDynamic:      func(h string) *model.NodeDynamic { return nil },
		ReadNodeLatency:      func(k NodeLatencyDirtyKey) *model.NodeLatency { return nil },
		ReadLease:            func(k LeaseDirtyKey) *model.Lease { return nil },
		ReadSubscriptionNode: func(k SubscriptionNodeDirtyKey) *model.SubscriptionNode { return nil },
	}

	// Very high threshold + very long interval → won't auto-flush.
	w := NewCacheFlushWorker(
		engine,
		readers,
		func() int { return 10000 },
		func() time.Duration { return 1 * time.Hour },
		50*time.Millisecond,
	)
	w.Start()

	engine.MarkNodeStatic("n1")
	time.Sleep(100 * time.Millisecond)

	// Still dirty.
	if dc := engine.DirtyCount(); dc != 1 {
		t.Fatalf("expected 1 dirty before stop, got %d", dc)
	}

	// Stop should trigger final flush.
	w.Stop()

	if dc := engine.DirtyCount(); dc != 0 {
		t.Fatalf("expected 0 dirty after stop (final flush), got %d", dc)
	}

	nodes, _ := engine.LoadAllNodesStatic()
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node after final flush, got %d", len(nodes))
	}
}

func TestFlushWorker_DynamicConfigPulled(t *testing.T) {
	engine, _, _ := newTestEngine(t)

	nodeStore := map[string]*model.NodeStatic{
		"n1": {Hash: "n1", RawOptionsJSON: `{}`, CreatedAtNs: 1},
	}
	readers := CacheReaders{
		ReadNodeStatic:       func(h string) *model.NodeStatic { return nodeStore[h] },
		ReadNodeDynamic:      func(h string) *model.NodeDynamic { return nil },
		ReadNodeLatency:      func(k NodeLatencyDirtyKey) *model.NodeLatency { return nil },
		ReadLease:            func(k LeaseDirtyKey) *model.Lease { return nil },
		ReadSubscriptionNode: func(k SubscriptionNodeDirtyKey) *model.SubscriptionNode { return nil },
	}

	var threshold atomic.Int64
	threshold.Store(10000)

	w := NewCacheFlushWorker(
		engine,
		readers,
		func() int { return int(threshold.Load()) },
		func() time.Duration { return time.Hour },
		20*time.Millisecond,
	)
	w.Start()
	defer w.Stop()

	engine.MarkNodeStatic("n1")
	time.Sleep(120 * time.Millisecond)
	if dc := engine.DirtyCount(); dc != 1 {
		t.Fatalf("expected dirty count 1 before threshold change, got %d", dc)
	}

	threshold.Store(1)
	time.Sleep(180 * time.Millisecond)
	if dc := engine.DirtyCount(); dc != 0 {
		t.Fatalf("expected dirty count 0 after threshold change, got %d", dc)
	}
}
