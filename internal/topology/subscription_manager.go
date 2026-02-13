package topology

import (
	"sync"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/resin-proxy/resin/internal/subscription"
)

// SubscriptionManager holds all subscription instances and provides
// per-subscription operation locks for serializing update/rename/eviction.
type SubscriptionManager struct {
	subs  *xsync.Map[string, *subscription.Subscription]
	locks sync.Map // map[string]*sync.Mutex — per-sub operation locks
}

// NewSubscriptionManager creates a new SubscriptionManager.
func NewSubscriptionManager() *SubscriptionManager {
	return &SubscriptionManager{
		subs: xsync.NewMap[string, *subscription.Subscription](),
	}
}

// Get retrieves a subscription by ID.
func (m *SubscriptionManager) Get(id string) (*subscription.Subscription, bool) {
	return m.subs.Load(id)
}

// Register adds a subscription to the manager.
func (m *SubscriptionManager) Register(sub *subscription.Subscription) {
	m.subs.Store(sub.ID, sub)
	m.locks.LoadOrStore(sub.ID, &sync.Mutex{})
}

// Unregister removes a subscription from the manager.
func (m *SubscriptionManager) Unregister(id string) {
	m.subs.Delete(id)
	m.locks.Delete(id)
}

// Lookup returns a subscription by ID (convenience for pool's subLookup).
func (m *SubscriptionManager) Lookup(subID string) *subscription.Subscription {
	sub, _ := m.subs.Load(subID)
	return sub
}

// WithSubLock acquires the per-subscription operation lock, runs fn, and releases.
// This serializes UpdateSubscription, RenameSubscription, and Ephemeral eviction
// on the same subscription.
func (m *SubscriptionManager) WithSubLock(subID string, fn func()) {
	lockVal, _ := m.locks.LoadOrStore(subID, &sync.Mutex{})
	mu := lockVal.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	fn()
}

// Range iterates all subscriptions.
func (m *SubscriptionManager) Range(fn func(id string, sub *subscription.Subscription) bool) {
	m.subs.Range(fn)
}

// Size returns the number of subscriptions.
func (m *SubscriptionManager) Size() int {
	return m.subs.Size()
}
