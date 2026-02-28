package topology

import (
	"log"
	"runtime"
	"sync"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/scanloop"
	"github.com/Resinat/Resin/internal/subscription"
)

// EphemeralCleaner periodically removes unhealthy nodes from ephemeral subscriptions.
type EphemeralCleaner struct {
	subManager    *SubscriptionManager
	pool          *GlobalNodePool
	onNodeEvicted func(subID string, hash node.Hash)

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewEphemeralCleaner creates an EphemeralCleaner that reads per-subscription
// eviction delay values during each sweep.
func NewEphemeralCleaner(
	subManager *SubscriptionManager,
	pool *GlobalNodePool,
) *EphemeralCleaner {
	return &EphemeralCleaner{
		subManager: subManager,
		pool:       pool,
		stopCh:     make(chan struct{}),
	}
}

// SetOnNodeEvicted sets callback invoked for each newly-evicted node.
func (c *EphemeralCleaner) SetOnNodeEvicted(fn func(subID string, hash node.Hash)) {
	c.onNodeEvicted = fn
}

// Start launches the background cleaner goroutine.
func (c *EphemeralCleaner) Start() {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		scanloop.Run(c.stopCh, scanloop.DefaultMinInterval, scanloop.DefaultJitterRange, c.sweep)
	}()
}

// Stop signals the cleaner to stop and waits for it to finish.
func (c *EphemeralCleaner) Stop() {
	close(c.stopCh)
	c.wg.Wait()
}

func (c *EphemeralCleaner) sweep() {
	c.sweepWithHook(nil)
}

// sweepWithHook runs the sweep. If betweenScans is non-nil, it is called
// after the candidate set (evictSet) is built but before the second
// verification check. This allows tests to inject state changes at the
// exact TOCTOU window.
func (c *EphemeralCleaner) sweepWithHook(betweenScans func()) {
	now := time.Now().UnixNano()

	type ephemeralSub struct {
		id  string
		sub *subscription.Subscription
	}
	ephemeralSubs := make([]ephemeralSub, 0, c.subManager.Size())

	c.subManager.Range(func(id string, sub *subscription.Subscription) bool {
		if sub.Ephemeral() {
			ephemeralSubs = append(ephemeralSubs, ephemeralSub{id: id, sub: sub})
		}
		return true
	})

	if len(ephemeralSubs) == 0 {
		return
	}

	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	if workers > len(ephemeralSubs) {
		workers = len(ephemeralSubs)
	}

	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, item := range ephemeralSubs {
		sem <- struct{}{}
		wg.Add(1)
		go func(id string, sub *subscription.Subscription) {
			defer wg.Done()
			defer func() { <-sem }()
			c.sweepOneSubscription(id, sub, now, betweenScans)
		}(item.id, item.sub)
	}
	wg.Wait()
}

func (c *EphemeralCleaner) sweepOneSubscription(
	id string,
	sub *subscription.Subscription,
	now int64,
	betweenScans func(),
) {
	var (
		evictCount    int
		evictedHashes []node.Hash
	)
	sub.WithOpLock(func() {
		evictDelayNs := sub.EphemeralNodeEvictDelayNs()
		evictCount, evictedHashes = CleanupSubscriptionNodesWithConfirmNoLock(
			sub,
			c.pool,
			func(entry *node.NodeEntry) bool {
				return c.shouldEvictEntry(entry, now, evictDelayNs)
			},
			betweenScans,
		)
	})
	if c.onNodeEvicted != nil {
		for _, h := range evictedHashes {
			c.onNodeEvicted(id, h)
		}
	}

	if evictCount > 0 {
		log.Printf("[ephemeral] evicted %d nodes from sub %s", evictCount, id)
	}
}

func (c *EphemeralCleaner) shouldEvictEntry(entry *node.NodeEntry, now int64, evictDelayNs int64) bool {
	if entry == nil {
		return false
	}

	// Outbound build failed and node is still without outbound.
	// For ephemeral subscriptions, this node should be dropped quickly.
	if !entry.HasOutbound() && entry.GetLastError() != "" {
		return true
	}

	// Circuit remains open beyond configured eviction delay.
	circuitSince := entry.CircuitOpenSince.Load()
	return circuitSince > 0 && (now-circuitSince) > evictDelayNs
}
