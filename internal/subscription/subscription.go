// Package subscription provides subscription types and parsing logic.
package subscription

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/puzpuzpuz/xsync/v4"
)

const defaultEphemeralNodeEvictDelayNs = int64(72 * time.Hour)

const (
	// SourceTypeRemote pulls subscription content over HTTP(S) from URL.
	SourceTypeRemote = "remote"
	// SourceTypeLocal reads subscription content from local text content.
	SourceTypeLocal = "local"
)

// ManagedNode represents one hash entry in subscription managed nodes.
type ManagedNode struct {
	Tags    []string
	Evicted bool
}

// ManagedNodes wraps hash->ManagedNode map.
//
// Maintenance rule:
//   - StoreNode makes a defensive copy of input Tags.
//   - LoadNode/RangeNodes return direct references to stored tag slices.
//   - Callers must treat returned Tags as read-only and must not mutate them.
//   - If mutation is needed, make an explicit copy first.
type ManagedNodes struct {
	m *xsync.Map[node.Hash, ManagedNode]
}

// NewManagedNodes creates an empty managed-node view.
func NewManagedNodes() *ManagedNodes {
	return &ManagedNodes{m: xsync.NewMap[node.Hash, ManagedNode]()}
}

// Size returns the count of hash entries (including evicted entries).
func (mn *ManagedNodes) Size() int {
	if mn == nil || mn.m == nil {
		return 0
	}
	return mn.m.Size()
}

// LoadNode loads the full managed-node state for a hash.
// Tags are returned as-is (no copy); treat them as read-only.
func (mn *ManagedNodes) LoadNode(h node.Hash) (ManagedNode, bool) {
	if mn == nil || mn.m == nil {
		return ManagedNode{}, false
	}
	n, ok := mn.m.Load(h)
	if !ok {
		return ManagedNode{}, false
	}
	return n, true
}

// StoreNode stores the full managed-node state for a hash.
// Tags are defensively copied on store.
func (mn *ManagedNodes) StoreNode(h node.Hash, n ManagedNode) {
	if mn == nil || mn.m == nil {
		return
	}
	mn.m.Store(h, ManagedNode{
		Tags:    cloneTags(n.Tags),
		Evicted: n.Evicted,
	})
}

// Delete deletes a hash entry.
func (mn *ManagedNodes) Delete(h node.Hash) {
	if mn == nil || mn.m == nil {
		return
	}
	mn.m.Delete(h)
}

// RangeNodes iterates hash->ManagedNode entries.
// ManagedNode.Tags is provided as-is (no copy); treat it as read-only.
func (mn *ManagedNodes) RangeNodes(fn func(node.Hash, ManagedNode) bool) {
	if mn == nil || mn.m == nil || fn == nil {
		return
	}
	mn.m.Range(fn)
}

// Subscription represents a subscription's runtime state.
// It has two synchronization layers:
//   - mu protects mutable config fields
//     (url/updateInterval/name/enabled/ephemeral/ephemeralNodeEvictDelayNs).
//   - opMu serializes high-level operations (update/rename/eviction/delete)
//     on the same subscription instance.
//
// Lock-order rule (must be preserved to avoid deadlocks):
//   - If both locks are needed in one flow, always acquire opMu before mu.
//   - Never call WithOpLock from code that already holds mu.
type Subscription struct {
	// Immutable after creation.
	ID string

	// Operation-level lock for serializing multi-step workflows.
	opMu sync.Mutex

	// Mutable fields guarded by mu.
	mu         sync.RWMutex
	url        string
	sourceType string
	content    string
	// updateIntervalNs is the configured subscription refresh interval.
	updateIntervalNs int64
	name             string
	enabled          bool
	ephemeral        bool
	// ephemeralNodeEvictDelayNs is the per-subscription eviction delay for
	// circuit-broken nodes when Ephemeral is enabled.
	ephemeralNodeEvictDelayNs int64

	// Persistence timestamps (written under mu or single-writer context).
	CreatedAtNs int64
	UpdatedAtNs int64

	// Runtime-only fields (NOT persisted). Atomic for lock-free reads
	// from the scheduler's due-check loop.
	LastCheckedNs atomic.Int64
	LastUpdatedNs atomic.Int64
	LastError     atomic.Pointer[string]

	// managedNodes is the subscription's node view: Hash → ManagedNode.
	// Swapped atomically on subscription update.
	managedNodes atomic.Pointer[ManagedNodes]

	// configVersion is incremented whenever refresh-input-related config changes
	// (URL/source/content/update-interval). Scheduler uses it for stale-guard.
	configVersion atomic.Int64
}

// NewSubscription creates a Subscription with an empty ManagedNodes map.
func NewSubscription(id, name, url string, enabled, ephemeral bool) *Subscription {
	s := &Subscription{
		ID:                        id,
		url:                       url,
		sourceType:                SourceTypeRemote,
		name:                      name,
		enabled:                   enabled,
		ephemeral:                 ephemeral,
		ephemeralNodeEvictDelayNs: defaultEphemeralNodeEvictDelayNs,
	}
	empty := NewManagedNodes()
	s.managedNodes.Store(empty)
	emptyErr := ""
	s.LastError.Store(&emptyErr)
	s.configVersion.Store(1)
	return s
}

// SetLastError atomically sets the last error string.
func (s *Subscription) SetLastError(err string) { s.LastError.Store(&err) }

// GetLastError atomically loads the last error string.
func (s *Subscription) GetLastError() string { return *s.LastError.Load() }

// WithOpLock runs fn under the subscription operation lock.
func (s *Subscription) WithOpLock(fn func()) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	fn()
}

// URL returns the subscription source URL (thread-safe).
func (s *Subscription) URL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.url
}

// SourceType returns the subscription source type (thread-safe).
func (s *Subscription) SourceType() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return normalizeSourceType(s.sourceType)
}

// Content returns the local subscription content (thread-safe).
func (s *Subscription) Content() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.content
}

// ConfigVersion returns the scheduler input config version.
func (s *Subscription) ConfigVersion() int64 {
	return s.configVersion.Load()
}

// UpdateIntervalNs returns the configured update interval in nanoseconds (thread-safe).
func (s *Subscription) UpdateIntervalNs() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.updateIntervalNs
}

// SetFetchConfig updates URL and update interval together atomically under lock.
func (s *Subscription) SetFetchConfig(url string, updateIntervalNs int64) {
	s.mu.Lock()
	changed := s.url != url || s.updateIntervalNs != updateIntervalNs
	s.url = url
	s.updateIntervalNs = updateIntervalNs
	if changed {
		s.configVersion.Add(1)
	}
	s.mu.Unlock()
}

// SetSourceType updates subscription source type (thread-safe).
func (s *Subscription) SetSourceType(sourceType string) {
	sourceType = normalizeSourceType(sourceType)
	s.mu.Lock()
	if s.sourceType != sourceType {
		s.sourceType = sourceType
		s.configVersion.Add(1)
	}
	s.mu.Unlock()
}

// SetContent updates local subscription content (thread-safe).
func (s *Subscription) SetContent(content string) {
	s.mu.Lock()
	if s.content != content {
		s.content = content
		s.configVersion.Add(1)
	}
	s.mu.Unlock()
}

// Name returns the subscription name (thread-safe).
func (s *Subscription) Name() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.name
}

// SetName updates the subscription name (thread-safe).
func (s *Subscription) SetName(name string) {
	s.mu.Lock()
	s.name = name
	s.mu.Unlock()
}

// Enabled returns whether the subscription is enabled (thread-safe).
func (s *Subscription) Enabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enabled
}

// SetEnabled updates the enabled flag (thread-safe).
func (s *Subscription) SetEnabled(v bool) {
	s.mu.Lock()
	s.enabled = v
	s.mu.Unlock()
}

// Ephemeral returns whether the subscription is ephemeral (thread-safe).
func (s *Subscription) Ephemeral() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ephemeral
}

// SetEphemeral updates the ephemeral flag (thread-safe).
func (s *Subscription) SetEphemeral(v bool) {
	s.mu.Lock()
	s.ephemeral = v
	s.mu.Unlock()
}

// EphemeralNodeEvictDelayNs returns the per-subscription eviction delay in nanoseconds.
func (s *Subscription) EphemeralNodeEvictDelayNs() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ephemeralNodeEvictDelayNs
}

// SetEphemeralNodeEvictDelayNs updates the per-subscription eviction delay.
func (s *Subscription) SetEphemeralNodeEvictDelayNs(v int64) {
	s.mu.Lock()
	s.ephemeralNodeEvictDelayNs = v
	s.mu.Unlock()
}

// ManagedNodes returns the current node view via atomic load.
func (s *Subscription) ManagedNodes() *ManagedNodes {
	return s.managedNodes.Load()
}

// SwapManagedNodes atomically replaces the managed nodes view.
func (s *Subscription) SwapManagedNodes(m *ManagedNodes) {
	s.managedNodes.Store(m)
}

// DiffHashes computes the hash diff between old and new managed-nodes maps.
// Returns slices of added, kept, and removed hashes.
func DiffHashes(
	oldMap, newMap *ManagedNodes,
) (added, kept, removed []node.Hash) {
	// Hashes only in new → added. Hashes in both → kept.
	newMap.RangeNodes(func(h node.Hash, _ ManagedNode) bool {
		if _, ok := oldMap.LoadNode(h); ok {
			kept = append(kept, h)
		} else {
			added = append(added, h)
		}
		return true
	})

	// Hashes only in old → removed.
	oldMap.RangeNodes(func(h node.Hash, _ ManagedNode) bool {
		if _, ok := newMap.LoadNode(h); !ok {
			removed = append(removed, h)
		}
		return true
	})

	return added, kept, removed
}

func normalizeSourceType(sourceType string) string {
	switch sourceType {
	case SourceTypeLocal:
		return SourceTypeLocal
	default:
		return SourceTypeRemote
	}
}

func cloneTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	cp := make([]string, len(tags))
	copy(cp, tags)
	return cp
}
