package platform

import (
	"net/netip"
	"regexp"
	"slices"
	"sync"

	"github.com/Resinat/Resin/internal/node"
)

// DefaultPlatformID is the well-known UUID of the built-in Default platform.
const DefaultPlatformID = "00000000-0000-0000-0000-000000000000"

// DefaultPlatformName is the built-in platform name.
const DefaultPlatformName = "Default"

// GeoLookupFunc resolves an IP address to a lowercase ISO country code.
type GeoLookupFunc func(netip.Addr) string

// PoolRangeFunc iterates all nodes in the global pool.
type PoolRangeFunc func(fn func(node.Hash, *node.NodeEntry) bool)

// GetEntryFunc retrieves a node entry from the global pool by hash.
type GetEntryFunc func(node.Hash) (*node.NodeEntry, bool)

// Platform represents a routing platform with its filtered routable view.
type Platform struct {
	ID   string
	Name string

	// Filter configuration.
	RegexFilters  []*regexp.Regexp
	RegionFilters []string // lowercase ISO codes

	// Other config fields.
	StickyTTLNs            int64
	ReverseProxyMissAction string
	AllocationPolicy       AllocationPolicy

	// Routable view & its lock.
	// viewMu serializes both FullRebuild and NotifyDirty.
	view   *RoutableView
	viewMu sync.Mutex
}

// NewPlatform creates a Platform with an empty routable view.
func NewPlatform(id, name string, regexFilters []*regexp.Regexp, regionFilters []string) *Platform {
	return &Platform{
		ID:            id,
		Name:          name,
		RegexFilters:  regexFilters,
		RegionFilters: regionFilters,
		view:          NewRoutableView(),
	}
}

// View returns the platform's routable view as a read-only interface.
// External callers cannot Add/Remove/Clear — only FullRebuild and NotifyDirty can mutate.
func (p *Platform) View() ReadOnlyView {
	return p.view
}

// FullRebuild clears the routable view and re-evaluates all nodes from the pool.
// Acquires viewMu — any concurrent NotifyDirty calls block until rebuild completes.
func (p *Platform) FullRebuild(
	poolRange PoolRangeFunc,
	subLookup node.SubLookupFunc,
	geoLookup GeoLookupFunc,
) {
	p.viewMu.Lock()
	defer p.viewMu.Unlock()

	p.view.Clear()
	poolRange(func(h node.Hash, entry *node.NodeEntry) bool {
		if p.evaluateNode(entry, subLookup, geoLookup) {
			p.view.Add(h)
		}
		return true
	})
}

// NotifyDirty re-evaluates a single node and adds/removes it from the view.
// Acquires viewMu — serialized with FullRebuild.
func (p *Platform) NotifyDirty(
	h node.Hash,
	getEntry GetEntryFunc,
	subLookup node.SubLookupFunc,
	geoLookup GeoLookupFunc,
) {
	p.viewMu.Lock()
	defer p.viewMu.Unlock()

	entry, ok := getEntry(h)
	if !ok {
		// Node was deleted from pool.
		p.view.Remove(h)
		return
	}

	if p.evaluateNode(entry, subLookup, geoLookup) {
		p.view.Add(h)
	} else {
		p.view.Remove(h)
	}
}

// evaluateNode checks all filter conditions for platform routability.
func (p *Platform) evaluateNode(
	entry *node.NodeEntry,
	subLookup node.SubLookupFunc,
	geoLookup GeoLookupFunc,
) bool {
	// 1. Not circuit-broken.
	if entry.IsCircuitOpen() {
		return false
	}

	// 2. Tag regex match.
	if !entry.MatchRegexs(p.RegexFilters, subLookup) {
		return false
	}

	// 3. Egress IP must be known.
	egressIP := entry.GetEgressIP()
	if !egressIP.IsValid() {
		return false
	}

	// 4. Region filter (when configured).
	if len(p.RegionFilters) > 0 {
		region := entry.GetRegion(geoLookup)
		if !matchRegion(region, p.RegionFilters) {
			return false
		}
	}

	// 5. Has at least one latency record.
	if !entry.HasLatency() {
		return false
	}

	// 6. Outbound is not nil.
	if !entry.HasOutbound() {
		return false
	}

	return true
}

// matchRegion checks if the region is in the allowed list.
func matchRegion(region string, allowed []string) bool {
	return slices.Contains(allowed, region)
}
