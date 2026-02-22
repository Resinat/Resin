package routing

import (
	"net/netip"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
)

type snapshotTestPool struct{}

func (snapshotTestPool) GetEntry(node.Hash) (*node.NodeEntry, bool)          { return nil, false }
func (snapshotTestPool) GetPlatform(string) (*platform.Platform, bool)       { return nil, false }
func (snapshotTestPool) GetPlatformByName(string) (*platform.Platform, bool) { return nil, false }
func (snapshotTestPool) RangePlatforms(func(*platform.Platform) bool)        {}

func newSnapshotTestRouter() *Router {
	return NewRouter(RouterConfig{
		Pool:        snapshotTestPool{},
		Authorities: func() []string { return nil },
		P2CWindow:   func() time.Duration { return time.Minute },
	})
}

func TestIPLoadStatsSnapshot(t *testing.T) {
	stats := NewIPLoadStats()
	ip1 := netip.MustParseAddr("1.1.1.1")
	ip2 := netip.MustParseAddr("2.2.2.2")

	stats.Inc(ip1)
	stats.Inc(ip1)
	stats.Inc(ip2)
	stats.Dec(ip2) // count becomes zero and should be skipped in snapshot

	snapshot := stats.Snapshot()
	if got, ok := snapshot[ip1]; !ok || got != 2 {
		t.Fatalf("snapshot[%v] = (%v, %d), want (true, 2)", ip1, ok, got)
	}
	if _, ok := snapshot[ip2]; ok {
		t.Fatalf("snapshot should not include %v with zero count", ip2)
	}

	// Ensure caller mutations do not affect internal counters.
	snapshot[ip1] = 999
	again := stats.Snapshot()
	if got := again[ip1]; got != 2 {
		t.Fatalf("second snapshot[%v] = %d, want 2", ip1, got)
	}
}

func TestRouterSnapshotIPLoad(t *testing.T) {
	router := newSnapshotTestRouter()

	empty := router.SnapshotIPLoad("platform-missing")
	if len(empty) != 0 {
		t.Fatalf("missing platform snapshot size = %d, want 0", len(empty))
	}

	state, _ := router.states.LoadOrCompute("platform-1", func() (*PlatformRoutingState, bool) {
		return NewPlatformRoutingState(), false
	})
	ip := netip.MustParseAddr("3.3.3.3")
	state.IPLoadStats.Inc(ip)
	state.IPLoadStats.Inc(ip)

	snapshot := router.SnapshotIPLoad("platform-1")
	if len(snapshot) != 1 {
		t.Fatalf("snapshot size = %d, want 1", len(snapshot))
	}
	if got, ok := snapshot[ip]; !ok || got != 2 {
		t.Fatalf("snapshot[%v] = (%v, %d), want (true, 2)", ip, ok, got)
	}
}
