package topology

import (
	"log"
	"sync"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/scanloop"
	"github.com/resin-proxy/resin/internal/subscription"
)

// EphemeralCleaner periodically removes circuit-broken nodes from ephemeral subscriptions.
type EphemeralCleaner struct {
	subManager *SubscriptionManager
	pool       *GlobalNodePool
	evictDelay time.Duration // EphemeralNodeEvictDelay

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewEphemeralCleaner creates a new EphemeralCleaner.
func NewEphemeralCleaner(
	subManager *SubscriptionManager,
	pool *GlobalNodePool,
	evictDelay time.Duration,
) *EphemeralCleaner {
	return &EphemeralCleaner{
		subManager: subManager,
		pool:       pool,
		evictDelay: evictDelay,
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

	c.subManager.Range(func(id string, sub *subscription.Subscription) bool {
		if !sub.Ephemeral() {
			return true
		}

		// All candidate checks and evictions happen under the lock to
		// prevent TOCTOU: a node that recovers between check and eviction
		// would otherwise be erroneously removed.
		var evictCount int
		c.subManager.WithSubLock(sub.ID, func() {
			evictSet := make(map[node.Hash]struct{})
			sub.ManagedNodes().Range(func(h node.Hash, _ []string) bool {
				entry, ok := c.pool.GetEntry(h)
				if !ok {
					return true
				}
				circuitSince := entry.CircuitOpenSince.Load()
				if circuitSince > 0 && (now-circuitSince) > c.evictDelay.Nanoseconds() {
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
				cs := entry.CircuitOpenSince.Load()
				if cs > 0 && (now-cs) > c.evictDelay.Nanoseconds() {
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
		return true
	})
}
