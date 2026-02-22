package topology

import (
	"log"
	"runtime"
	"sync"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/scanloop"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/puzpuzpuz/xsync/v4"
)

// EphemeralCleaner periodically removes unhealthy nodes from ephemeral subscriptions.
type EphemeralCleaner struct {
	subManager *SubscriptionManager
	pool       *GlobalNodePool
	evictDelay func() time.Duration // EphemeralNodeEvictDelay

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewEphemeralCleaner creates an EphemeralCleaner that pulls
// evictDelay from callback on each sweep.
func NewEphemeralCleaner(
	subManager *SubscriptionManager,
	pool *GlobalNodePool,
	evictDelayFn func() time.Duration,
) *EphemeralCleaner {
	if evictDelayFn == nil {
		panic("topology: NewEphemeralCleaner requires non-nil evictDelayFn")
	}
	return &EphemeralCleaner{
		subManager: subManager,
		pool:       pool,
		evictDelay: evictDelayFn,
		stopCh:     make(chan struct{}),
	}
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
	evictDelayNs := c.evictDelay().Nanoseconds()

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
			c.sweepOneSubscription(id, sub, now, evictDelayNs, betweenScans)
		}(item.id, item.sub)
	}
	wg.Wait()
}

func (c *EphemeralCleaner) sweepOneSubscription(
	id string,
	sub *subscription.Subscription,
	now int64,
	evictDelayNs int64,
	betweenScans func(),
) {
	// All candidate checks and evictions happen under the lock to
	// prevent TOCTOU: a node that recovers between check and eviction
	// would otherwise be erroneously removed.
	var evictCount int
	sub.WithOpLock(func() {
		evictSet := make(map[node.Hash]struct{})
		sub.ManagedNodes().Range(func(h node.Hash, _ []string) bool {
			entry, ok := c.pool.GetEntry(h)
			if !ok {
				return true
			}
			if c.shouldEvictEntry(entry, now, evictDelayNs) {
				evictSet[h] = struct{}{}
			}
			return true
		})

		if len(evictSet) == 0 {
			return
		}

		// --- TOCTOU window: test hook ---
		if betweenScans != nil {
			betweenScans()
		}

		// Second check: re-verify each candidate. A node may have
		// recovered between the initial scan and now.
		confirmedEvict := make(map[node.Hash]struct{})
		for h := range evictSet {
			entry, ok := c.pool.GetEntry(h)
			if !ok {
				continue
			}
			if c.shouldEvictEntry(entry, now, evictDelayNs) {
				confirmedEvict[h] = struct{}{}
			}
		}

		if len(confirmedEvict) == 0 {
			return
		}

		// Build new map without confirmed-evict hashes.
		current := sub.ManagedNodes()
		newMap := xsync.NewMap[node.Hash, []string]()
		current.Range(func(h node.Hash, tags []string) bool {
			if _, evict := confirmedEvict[h]; !evict {
				newMap.Store(h, tags)
			}
			return true
		})
		sub.SwapManagedNodes(newMap)

		for h := range confirmedEvict {
			c.pool.RemoveNodeFromSub(h, sub.ID)
		}
		evictCount = len(confirmedEvict)
	})

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
