package routing

import (
	"sync"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/resin-proxy/resin/internal/scanloop"
)

// LeaseCleaner periodically sweeps for expired leases.
type LeaseCleaner struct {
	router      *Router
	stopCh      chan struct{}
	stopOnce    sync.Once
	wg          sync.WaitGroup
	minInterval time.Duration
	jitterRange time.Duration

	// test hook: called at the beginning of each sweep.
	sweepHook func()
}

func NewLeaseCleaner(router *Router) *LeaseCleaner {
	return newLeaseCleanerWithIntervals(router, 13*time.Second, 4*time.Second)
}

func newLeaseCleanerWithIntervals(router *Router, minInterval, jitterRange time.Duration) *LeaseCleaner {
	return &LeaseCleaner{
		router:      router,
		stopCh:      make(chan struct{}),
		minInterval: minInterval,
		jitterRange: jitterRange,
	}
}

func (c *LeaseCleaner) Start() {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		scanloop.Run(c.stopCh, c.minInterval, c.jitterRange, c.sweep)
	}()
}

func (c *LeaseCleaner) Stop() {
	c.stopOnce.Do(func() { close(c.stopCh) })
	c.wg.Wait()
}

func (c *LeaseCleaner) sweep() {
	if c.sweepHook != nil {
		c.sweepHook()
	}

	now := time.Now()
	nowNs := now.UnixNano()

	c.router.states.Range(func(platID string, state *PlatformRoutingState) bool {
		// Iterate over all leases for this platform
		state.Leases.Range(func(account string, lease Lease) bool {
			// Check against stop signal
			select {
			case <-c.stopCh:
				return false
			default:
			}

			if lease.ExpiryNs < nowNs {
				// Expired. Use Compute to verify and delete atomically.
				state.Leases.leases.Compute(account, func(current Lease, loaded bool) (Lease, xsync.ComputeOp) {
					if !loaded {
						return current, xsync.CancelOp
					}
					// Double-check expiry inside lock
					if current.ExpiryNs < nowNs {
						state.Leases.stats.Dec(current.EgressIP)

						if c.router.onLeaseEvent != nil {
							c.router.onLeaseEvent(LeaseEvent{
								Type:       LeaseExpire,
								PlatformID: platID,
								Account:    account,
								NodeHash:   current.NodeHash,
								EgressIP:   current.EgressIP,
							})
						}

						return current, xsync.DeleteOp
					}
					return current, xsync.CancelOp // Renewed concurrently, don't delete
				})
			}
			return true
		})
		return true
	})
}
