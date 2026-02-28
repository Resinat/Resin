package topology

import (
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/subscription"
)

// CleanupSubscriptionNodesWithConfirmNoLock marks managed nodes as evicted when
// they match shouldRemove, using two-pass confirmation to reduce TOCTOU issues.
//
// It keeps hashes in managed view, removes pool subscription references, and
// returns newly-evicted hashes for persistence upsert compensation.
// Caller must hold sub.WithOpLock while invoking this function.
func CleanupSubscriptionNodesWithConfirmNoLock(
	sub *subscription.Subscription,
	pool *GlobalNodePool,
	shouldRemove func(entry *node.NodeEntry) bool,
	betweenScans func(),
) (int, []node.Hash) {
	if sub == nil || pool == nil || shouldRemove == nil {
		return 0, nil
	}

	currentManaged := sub.ManagedNodes()
	removeCandidates := make(map[node.Hash]struct{})
	currentManaged.RangeNodes(func(h node.Hash, managed subscription.ManagedNode) bool {
		if managed.Evicted {
			return true
		}
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
		return 0, nil
	}

	if betweenScans != nil {
		betweenScans()
	}

	confirmedRemove := make(map[node.Hash]struct{})
	for h := range removeCandidates {
		managed, ok := currentManaged.LoadNode(h)
		if !ok || managed.Evicted {
			continue
		}
		entry, ok := pool.GetEntry(h)
		if !ok {
			continue
		}
		if shouldRemove(entry) {
			confirmedRemove[h] = struct{}{}
		}
	}
	if len(confirmedRemove) == 0 {
		return 0, nil
	}

	nextManaged := subscription.NewManagedNodes()
	newlyEvicted := make([]node.Hash, 0, len(confirmedRemove))
	currentManaged.RangeNodes(func(h node.Hash, managed subscription.ManagedNode) bool {
		if _, remove := confirmedRemove[h]; remove {
			if !managed.Evicted {
				newlyEvicted = append(newlyEvicted, h)
			}
			managed.Evicted = true
		}
		nextManaged.StoreNode(h, managed)
		return true
	})
	sub.SwapManagedNodes(nextManaged)

	for _, h := range newlyEvicted {
		pool.RemoveNodeFromSub(h, sub.ID)
	}

	return len(newlyEvicted), newlyEvicted
}
