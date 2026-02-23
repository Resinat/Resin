package topology

import (
	"github.com/puzpuzpuz/xsync/v4"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/subscription"
)

// CleanupSubscriptionNodesWithConfirmNoLock removes managed nodes of sub that
// match shouldRemove, using two-pass confirmation to reduce TOCTOU issues.
//
// Caller must hold sub.WithOpLock while invoking this function.
func CleanupSubscriptionNodesWithConfirmNoLock(
	sub *subscription.Subscription,
	pool *GlobalNodePool,
	shouldRemove func(entry *node.NodeEntry) bool,
	betweenScans func(),
) int {
	if sub == nil || pool == nil || shouldRemove == nil {
		return 0
	}

	currentManaged := sub.ManagedNodes()
	removeCandidates := make(map[node.Hash]struct{})
	currentManaged.Range(func(h node.Hash, _ []string) bool {
		entry, ok := pool.GetEntry(h)
		if !ok {
			return true
		}
		if shouldRemove(entry) {
			removeCandidates[h] = struct{}{}
		}
		return true
	})
	if len(removeCandidates) == 0 {
		return 0
	}

	if betweenScans != nil {
		betweenScans()
	}

	confirmedRemove := make(map[node.Hash]struct{})
	for h := range removeCandidates {
		entry, ok := pool.GetEntry(h)
		if !ok {
			continue
		}
		if shouldRemove(entry) {
			confirmedRemove[h] = struct{}{}
		}
	}
	if len(confirmedRemove) == 0 {
		return 0
	}

	nextManaged := xsync.NewMap[node.Hash, []string]()
	currentManaged.Range(func(h node.Hash, tags []string) bool {
		if _, remove := confirmedRemove[h]; !remove {
			nextManaged.Store(h, tags)
		}
		return true
	})
	sub.SwapManagedNodes(nextManaged)

	for h := range confirmedRemove {
		pool.RemoveNodeFromSub(h, sub.ID)
	}

	return len(confirmedRemove)
}
