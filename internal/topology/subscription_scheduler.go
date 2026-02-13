package topology

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/subscription"
)

// SubscriptionScheduler manages periodic subscription updates.
type SubscriptionScheduler struct {
	subManager *SubscriptionManager
	pool       *GlobalNodePool
	httpClient *http.Client
	userAgent  string

	// Fetcher fetches subscription data from a URL.
	// Defaults to HTTP fetch; injectable for testing.
	Fetcher func(url string) ([]byte, error)

	// For persistence.
	onSubUpdated func(sub *subscription.Subscription)

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// SchedulerConfig configures the SubscriptionScheduler.
type SchedulerConfig struct {
	SubManager   *SubscriptionManager
	Pool         *GlobalNodePool
	HTTPTimeout  time.Duration
	UserAgent    string
	Fetcher      func(url string) ([]byte, error) // optional, defaults to HTTP
	OnSubUpdated func(sub *subscription.Subscription)
}

// NewSubscriptionScheduler creates a new scheduler.
func NewSubscriptionScheduler(cfg SchedulerConfig) *SubscriptionScheduler {
	timeout := cfg.HTTPTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	sched := &SubscriptionScheduler{
		subManager:   cfg.SubManager,
		pool:         cfg.Pool,
		httpClient:   &http.Client{Timeout: timeout},
		userAgent:    cfg.UserAgent,
		onSubUpdated: cfg.OnSubUpdated,
		stopCh:       make(chan struct{}),
	}
	if cfg.Fetcher != nil {
		sched.Fetcher = cfg.Fetcher
	} else {
		sched.Fetcher = sched.fetchURL
	}
	return sched
}

// Start launches the background scheduler goroutine.
func (s *SubscriptionScheduler) Start() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		runLoop(s.stopCh, s.tick)
	}()
}

// Stop signals the scheduler to stop and waits for it to finish.
func (s *SubscriptionScheduler) Stop() {
	close(s.stopCh)
	s.wg.Wait()
}

func (s *SubscriptionScheduler) tick() {
	now := time.Now().UnixNano()
	s.subManager.Range(func(id string, sub *subscription.Subscription) bool {
		if !sub.Enabled() {
			return true
		}
		// Check if due: lastChecked + interval - lookahead <= now.
		if sub.LastCheckedNs.Load()+sub.UpdateIntervalNs-int64(schedulerLookahead) <= now {
			s.UpdateSubscription(sub)
		}
		return true
	})
}

// UpdateSubscription fetches and parses outside the lock, then diffs and
// applies the result under WithSubLock. This keeps the lock scope narrow
// (no I/O under lock) while still preventing concurrent diff/apply races.
func (s *SubscriptionScheduler) UpdateSubscription(sub *subscription.Subscription) {
	attemptStartedNs := time.Now().UnixNano()

	// 1. Fetch (lock-free).
	body, err := s.Fetcher(sub.URL)
	if err != nil {
		applied := false
		s.subManager.WithSubLock(sub.ID, func() {
			if sub.LastUpdatedNs.Load() > attemptStartedNs {
				return
			}
			now := time.Now().UnixNano()
			sub.LastCheckedNs.Store(now)
			sub.SetLastError(err.Error())
			applied = true
		})
		if !applied {
			log.Printf("[scheduler] stale fetch failure ignored for %s: %v", sub.ID, err)
			return
		}
		log.Printf("[scheduler] fetch %s failed: %v", sub.ID, err)
		if s.onSubUpdated != nil {
			s.onSubUpdated(sub)
		}
		return
	}

	// 2. Parse (lock-free).
	parsed, err := subscription.ParseSingboxSubscription(body)
	if err != nil {
		applied := false
		s.subManager.WithSubLock(sub.ID, func() {
			if sub.LastUpdatedNs.Load() > attemptStartedNs {
				return
			}
			now := time.Now().UnixNano()
			sub.LastCheckedNs.Store(now)
			sub.SetLastError(err.Error())
			applied = true
		})
		if !applied {
			log.Printf("[scheduler] stale parse failure ignored for %s: %v", sub.ID, err)
			return
		}
		log.Printf("[scheduler] parse %s failed: %v", sub.ID, err)
		if s.onSubUpdated != nil {
			s.onSubUpdated(sub)
		}
		return
	}

	// 3. Build new managed nodes map (lock-free, pure computation).
	newManagedNodes := xsync.NewMap[node.Hash, []string]()
	rawByHash := make(map[node.Hash][]byte)
	for _, p := range parsed {
		h := node.HashFromRawOptions(p.RawOptions)
		existing, _ := newManagedNodes.Load(h)
		existing = append(existing, p.Tag)
		newManagedNodes.Store(h, existing)
		if _, ok := rawByHash[h]; !ok {
			rawByHash[h] = p.RawOptions
		}
	}

	// 4. Diff, swap, add/remove — under lock.
	s.subManager.WithSubLock(sub.ID, func() {
		old := sub.ManagedNodes()
		added, kept, removed := subscription.DiffHashes(old, newManagedNodes)

		sub.SwapManagedNodes(newManagedNodes)

		for _, h := range added {
			s.pool.AddNodeFromSub(h, rawByHash[h], sub.ID)
		}
		for _, h := range kept {
			s.pool.AddNodeFromSub(h, rawByHash[h], sub.ID)
		}
		for _, h := range removed {
			s.pool.RemoveNodeFromSub(h, sub.ID)
		}

		// 5. Update timestamps (inside lock, using current time).
		now := time.Now().UnixNano()
		sub.LastCheckedNs.Store(now)
		sub.LastUpdatedNs.Store(now)
		sub.SetLastError("")
	})

	if s.onSubUpdated != nil {
		s.onSubUpdated(sub)
	}
}

// SetSubscriptionEnabled updates the enabled flag and rebuilds all platform
// routable views. Disabling a subscription makes its nodes invisible to
// platform tag-regex matching; enabling makes them visible again.
func (s *SubscriptionScheduler) SetSubscriptionEnabled(sub *subscription.Subscription, enabled bool) {
	s.subManager.WithSubLock(sub.ID, func() {
		sub.SetEnabled(enabled)
	})
	// Rebuild all platform views so they pick up the visibility change.
	s.pool.RebuildAllPlatforms()
	if s.onSubUpdated != nil {
		s.onSubUpdated(sub)
	}
}

// RenameSubscription updates the subscription name and re-triggers platform
// re-evaluation for all managed nodes (since tags include the sub name).
func (s *SubscriptionScheduler) RenameSubscription(sub *subscription.Subscription, newName string) {
	s.subManager.WithSubLock(sub.ID, func() {
		sub.SetName(newName)
		// Re-add all managed hashes to trigger platform re-filter.
		sub.ManagedNodes().Range(func(h node.Hash, _ []string) bool {
			entry, ok := s.pool.GetEntry(h)
			if ok {
				s.pool.AddNodeFromSub(h, entry.RawOptions, sub.ID)
			}
			return true
		})
	})
}

func (s *SubscriptionScheduler) fetchURL(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.httpClient.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if s.userAgent != "" {
		req.Header.Set("User-Agent", s.userAgent)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}
