package topology

import (
	"github.com/puzpuzpuz/xsync/v4"
	"github.com/Resinat/Resin/internal/subscription"
)

// SubscriptionManager holds all subscription instances and provides
// lifecycle-safe lookup/register/unregister for subscription instances.
type SubscriptionManager struct {
	subs *xsync.Map[string, *subscription.Subscription]
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
}

// Unregister removes a subscription from the manager.
func (m *SubscriptionManager) Unregister(id string) {
	m.subs.Delete(id)
}

// Lookup returns a subscription by ID (convenience for pool's subLookup).
func (m *SubscriptionManager) Lookup(subID string) *subscription.Subscription {
	sub, _ := m.subs.Load(subID)
	return sub
}

// Range iterates all subscriptions.
func (m *SubscriptionManager) Range(fn func(id string, sub *subscription.Subscription) bool) {
	m.subs.Range(fn)
}

// Size returns the number of subscriptions.
func (m *SubscriptionManager) Size() int {
	return m.subs.Size()
}
