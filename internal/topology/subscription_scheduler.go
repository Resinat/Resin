package topology

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/Resinat/Resin/internal/netutil"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/scanloop"
	"github.com/Resinat/Resin/internal/subscription"
)

const (
	schedulerLookahead      = 15 * time.Second
	chainedOutboundTagPrefix = "resin-node-"
)

// SubscriptionScheduler manages periodic subscription updates.
type SubscriptionScheduler struct {
	subManager     *SubscriptionManager
	pool           *GlobalNodePool
	downloader     netutil.Downloader
	downloadCtx    context.Context
	cancelDownload context.CancelFunc

	// Fetcher fetches subscription data from a URL.
	// Defaults to downloader.Download; injectable for testing.
	Fetcher func(url string) ([]byte, error)

	// For persistence.
	onSubUpdated func(sub *subscription.Subscription)
	// onSubReenabledNode is called for each non-evicted node hash when a
	// subscription transitions from disabled to enabled.
	onSubReenabledNode func(hash node.Hash)

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// SchedulerConfig configures the SubscriptionScheduler.
type SchedulerConfig struct {
	SubManager   *SubscriptionManager
	Pool         *GlobalNodePool
	Downloader   netutil.Downloader               // shared downloader
	Fetcher      func(url string) ([]byte, error) // optional, defaults to Downloader.Download
	OnSubUpdated func(sub *subscription.Subscription)
	// OnSubReenabledNode is fired after false->true enabled transition.
	OnSubReenabledNode func(hash node.Hash)
}

// NewSubscriptionScheduler creates a new scheduler.
func NewSubscriptionScheduler(cfg SchedulerConfig) *SubscriptionScheduler {
	downloadCtx, cancelDownload := context.WithCancel(context.Background())
	sched := &SubscriptionScheduler{
		subManager:         cfg.SubManager,
		pool:               cfg.Pool,
		downloader:         cfg.Downloader,
		downloadCtx:        downloadCtx,
		cancelDownload:     cancelDownload,
		onSubUpdated:       cfg.OnSubUpdated,
		onSubReenabledNode: cfg.OnSubReenabledNode,
		stopCh:             make(chan struct{}),
	}
	if cfg.Fetcher != nil {
		sched.Fetcher = cfg.Fetcher
	} else {
		sched.Fetcher = sched.fetchViaDownloader
	}
	return sched
}

// Start launches the background scheduler goroutine.
func (s *SubscriptionScheduler) Start() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		scanloop.Run(s.stopCh, scanloop.DefaultMinInterval, scanloop.DefaultJitterRange, s.tick)
	}()
}

// Stop signals the scheduler to stop and waits for it to finish.
func (s *SubscriptionScheduler) Stop() {
	close(s.stopCh)
	s.cancelDownload()
	s.wg.Wait()
}

// ForceRefreshAll unconditionally updates ALL enabled subscriptions, regardless
// of their next-check timestamps. Called once at startup to compensate for
// lost data from weak persistence (DESIGN.md step 8 batch 3).
// Updates run in parallel, and this method waits until all started updates exit.
func (s *SubscriptionScheduler) ForceRefreshAll() {
	select {
	case <-s.stopCh:
		return
	default:
	}

	subsToRefresh := make([]*subscription.Subscription, 0, s.subManager.Size())
	s.subManager.Range(func(id string, sub *subscription.Subscription) bool {
		select {
		case <-s.stopCh:
			return false
		default:
		}
		if sub.Enabled() {
			subsToRefresh = append(subsToRefresh, sub)
		}
		return true
	})
	s.runUpdatesWithWorkerLimit(subsToRefresh)
}

// ForceRefreshAllAsync triggers ForceRefreshAll in a background goroutine.
// The goroutine is tracked by scheduler waitgroup so Stop() waits for exit.
func (s *SubscriptionScheduler) ForceRefreshAllAsync() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.ForceRefreshAll()
	}()
}

func (s *SubscriptionScheduler) tick() {
	select {
	case <-s.stopCh:
		return
	default:
	}

	now := time.Now().UnixNano()
	dueSubs := make([]*subscription.Subscription, 0, s.subManager.Size())
	s.subManager.Range(func(id string, sub *subscription.Subscription) bool {
		select {
		case <-s.stopCh:
			return false
		default:
		}
		if !sub.Enabled() {
			return true
		}
		// Check if due: lastChecked + interval - lookahead <= now.
		if sub.LastCheckedNs.Load()+sub.UpdateIntervalNs()-int64(schedulerLookahead) <= now {
			dueSubs = append(dueSubs, sub)
		}
		return true
	})
	s.runUpdatesWithWorkerLimit(dueSubs)
}

func (s *SubscriptionScheduler) runUpdatesWithWorkerLimit(subs []*subscription.Subscription) {
	if len(subs) == 0 {
		return
	}

	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	if workers > len(subs) {
		workers = len(subs)
	}

	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, sub := range subs {
		select {
		case <-s.stopCh:
			wg.Wait()
			return
		default:
		}

		sem <- struct{}{}
		wg.Add(1)
		go func(sub *subscription.Subscription) {
			defer wg.Done()
			defer func() { <-sem }()
			select {
			case <-s.stopCh:
				return
			default:
			}
			s.UpdateSubscription(sub)
		}(sub)
	}
	wg.Wait()
}

// UpdateSubscription fetches and parses outside the lock, then diffs and
// applies the result under WithSubLock. This keeps the lock scope narrow
// (no I/O under lock) while still preventing concurrent diff/apply races.
func (s *SubscriptionScheduler) UpdateSubscription(sub *subscription.Subscription) {
	attemptStartedNs := time.Now().UnixNano()
	attemptURL := sub.URL()
	attemptSourceType := sub.SourceType()
	attemptContent := sub.Content()
	attemptConfigVersion := sub.ConfigVersion()

	// 1. Fetch/read content (lock-free).
	var (
		body []byte
		err  error
	)
	if attemptSourceType == subscription.SourceTypeLocal {
		body = []byte(attemptContent)
	} else {
		body, err = s.Fetcher(attemptURL)
		if err != nil {
			s.handleUpdateFailure(sub, attemptStartedNs, attemptConfigVersion, "fetch", err)
			return
		}
	}

	// 2. Parse (lock-free).
	parsed, err := subscription.ParseGeneralSubscription(body)
	if err != nil {
		s.handleUpdateFailure(sub, attemptStartedNs, attemptConfigVersion, "parse", err)
		return
	}

	// 3. Build new managed nodes map (lock-free, pure computation).
	upstreamDetourTag, err := s.resolveUpstreamDetourTag(sub.UpstreamSubscriptionID())
	if err != nil {
		s.handleUpdateFailure(sub, attemptStartedNs, attemptConfigVersion, "chain", err)
		return
	}
	log.Printf("[scheduler] subscription=%s upstream=%s detour=%s", sub.ID, sub.UpstreamSubscriptionID(), upstreamDetourTag)

	newManagedNodes := subscription.NewManagedNodes()
	rawByHash := make(map[node.Hash][]byte)
	for _, p := range parsed {
		rawOptions, hash, err := rewriteParsedOutboundRaw(p.RawOptions, upstreamDetourTag)
		if err != nil {
			s.handleUpdateFailure(sub, attemptStartedNs, attemptConfigVersion, "rewrite", err)
			return
		}
		log.Printf("[scheduler] subscription=%s node=%s tag=%s detour=%s", sub.ID, hash.Hex(), p.Tag, upstreamDetourTag)

		existing, _ := newManagedNodes.LoadNode(hash)
		existing.Tags = append(existing.Tags, p.Tag)
		newManagedNodes.StoreNode(hash, existing)
		if _, ok := rawByHash[hash]; !ok {
			rawByHash[hash] = rawOptions
		}
	}

	// 4. Diff, swap, add/remove — under lock.
	applied := false
	sub.WithOpLock(func() {
		// If refresh-input config changed while this attempt was in-flight, discard.
		if sub.ConfigVersion() != attemptConfigVersion {
			return
		}
		// Stale success guard: if a newer successful update has already landed,
		// discard this older attempt to avoid rolling state backward.
		if sub.LastUpdatedNs.Load() >= attemptStartedNs {
			return
		}

		old := sub.ManagedNodes()

		// Keep hashes inherit historical eviction state so refresh will not
		// re-add previously evicted nodes back into pool.
		old.RangeNodes(func(h node.Hash, oldNode subscription.ManagedNode) bool {
			if !oldNode.Evicted {
				return true
			}
			nextNode, ok := newManagedNodes.LoadNode(h)
			if !ok {
				return true
			}
			nextNode.Evicted = true
			newManagedNodes.StoreNode(h, nextNode)
			return true
		})
		added, kept, removed := subscription.DiffHashes(old, newManagedNodes)

		sub.SwapManagedNodes(newManagedNodes)

		for _, h := range added {
			s.pool.AddNodeFromSub(h, rawByHash[h], sub.ID)
		}
		for _, h := range kept {
			managed, ok := newManagedNodes.LoadNode(h)
			if ok && managed.Evicted {
				continue
			}
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
		applied = true
	})
	if !applied {
		log.Printf("[scheduler] stale success ignored for %s", sub.ID)
		return
	}

	if s.onSubUpdated != nil {
		s.onSubUpdated(sub)
	}
	s.refreshDownstreamSubscriptions(sub.ID)
}

// handleUpdateFailure applies a fetch/parse failure to subscription state.
// It ignores stale failures from an outdated attempt (config-version guard +
// LastUpdatedNs stale-success guard).
func (s *SubscriptionScheduler) handleUpdateFailure(
	sub *subscription.Subscription,
	attemptStartedNs int64,
	attemptConfigVersion int64,
	stage string,
	err error,
) {
	applied := false
	sub.WithOpLock(func() {
		// If refresh-input config changed while this attempt was in-flight, discard.
		if sub.ConfigVersion() != attemptConfigVersion {
			return
		}
		if sub.LastUpdatedNs.Load() >= attemptStartedNs {
			return
		}
		now := time.Now().UnixNano()
		sub.LastCheckedNs.Store(now)
		sub.SetLastError(err.Error())
		applied = true
	})
	if !applied {
		log.Printf("[scheduler] stale %s failure ignored for %s: %v", stage, sub.ID, err)
		return
	}

	log.Printf("[scheduler] %s %s failed: %v", stage, sub.ID, err)
	if s.onSubUpdated != nil {
		s.onSubUpdated(sub)
	}
}

// SetSubscriptionEnabled updates the enabled flag and rebuilds all platform
// routable views. Disabling a subscription makes its nodes invisible to
// platform tag-regex matching; enabling makes them visible again.
func (s *SubscriptionScheduler) SetSubscriptionEnabled(sub *subscription.Subscription, enabled bool) {
	wasEnabled := false
	var candidateHashes []node.Hash
	wasDisabled := make(map[node.Hash]struct{})
	sub.WithOpLock(func() {
		wasEnabled = sub.Enabled()

		if !wasEnabled && enabled {
			sub.ManagedNodes().RangeNodes(func(h node.Hash, managed subscription.ManagedNode) bool {
				if managed.Evicted {
					return true
				}
				candidateHashes = append(candidateHashes, h)
				if s.pool != nil && s.pool.IsNodeDisabled(h) {
					wasDisabled[h] = struct{}{}
				}
				return true
			})
		}

		sub.SetEnabled(enabled)
	})
	// Rebuild all platform views so they pick up the visibility change.
	s.pool.RebuildAllPlatforms()

	// On re-enable, immediately re-check node outbound/probe state for nodes
	// that actually transitioned from disabled -> enabled.
	if !wasEnabled && enabled && s.onSubReenabledNode != nil {
		for _, h := range candidateHashes {
			if _, ok := wasDisabled[h]; !ok {
				continue
			}
			if s.pool.IsNodeDisabled(h) {
				continue
			}
			s.onSubReenabledNode(h)
		}
	}

	if s.onSubUpdated != nil {
		s.onSubUpdated(sub)
	}
}

// RenameSubscription updates the subscription name and re-triggers platform
// re-evaluation for all managed nodes (since tags include the sub name).
func (s *SubscriptionScheduler) RenameSubscription(sub *subscription.Subscription, newName string) {
	sub.WithOpLock(func() {
		sub.SetName(newName)
		// Re-add all managed hashes to trigger platform re-filter.
		sub.ManagedNodes().RangeNodes(func(h node.Hash, managed subscription.ManagedNode) bool {
			if managed.Evicted {
				return true
			}
			entry, ok := s.pool.GetEntry(h)
			if ok {
				s.pool.AddNodeFromSub(h, entry.RawOptions, sub.ID)
			}
			return true
		})
	})
}

func (s *SubscriptionScheduler) fetchViaDownloader(url string) ([]byte, error) {
	return s.downloader.Download(s.downloadCtx, url)
}

func (s *SubscriptionScheduler) resolveUpstreamDetourTag(upstreamSubID string) (string, error) {
	if upstreamSubID == "" {
		return "", nil
	}
	upstreamSub := s.subManager.Lookup(upstreamSubID)
	if upstreamSub == nil {
		return "", fmt.Errorf("upstream subscription %s not found", upstreamSubID)
	}

	hashes := make([]node.Hash, 0)
	upstreamSub.ManagedNodes().RangeNodes(func(h node.Hash, managed subscription.ManagedNode) bool {
		if managed.Evicted {
			return true
		}
		if _, ok := s.pool.GetEntry(h); !ok {
			return true
		}
		hashes = append(hashes, h)
		return true
	})
	if len(hashes) == 0 {
		return "", fmt.Errorf("upstream subscription %s has no active nodes in pool", upstreamSubID)
	}
	sort.Slice(hashes, func(i, j int) bool {
		return hashes[i].Hex() < hashes[j].Hex()
	})
	return chainedOutboundTagPrefix + hashes[0].Hex(), nil
}

func (s *SubscriptionScheduler) refreshDownstreamSubscriptions(upstreamSubID string) {
	if upstreamSubID == "" {
		return
	}
	queue := []string{upstreamSubID}
	visited := map[string]struct{}{upstreamSubID: {}}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		s.subManager.Range(func(_ string, sub *subscription.Subscription) bool {
			if sub.UpstreamSubscriptionID() != current {
				return true
			}
			if _, ok := visited[sub.ID]; ok {
				return true
			}
			visited[sub.ID] = struct{}{}
			if sub.Enabled() {
				go s.UpdateSubscription(sub)
			}
			queue = append(queue, sub.ID)
			return true
		})
	}
}

func rewriteParsedOutboundRaw(raw []byte, detourTag string) ([]byte, node.Hash, error) {
	var outbound map[string]any
	if err := json.Unmarshal(raw, &outbound); err != nil {
		return nil, node.Zero, fmt.Errorf("decode outbound: %w", err)
	}
	hash := node.HashFromRawOptions(raw)
	outbound["tag"] = chainedOutboundTagPrefix + hash.Hex()
	if detourTag != "" {
		outbound["detour"] = detourTag
	} else {
		delete(outbound, "detour")
	}
	rewritten, err := json.Marshal(outbound)
	if err != nil {
		return nil, node.Zero, fmt.Errorf("encode outbound: %w", err)
	}
	return rewritten, hash, nil
}
