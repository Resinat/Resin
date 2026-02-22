package routing

import (
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/Resinat/Resin/internal/node"
)

// Lease represents a sticky routing lease.
// It is a value type to avoid pointer aliasing races.
type Lease struct {
	NodeHash       node.Hash
	EgressIP       netip.Addr
	CreatedAtNs    int64
	ExpiryNs       int64
	LastAccessedNs int64
}

// LeaseTable manages per-account sticky leases for a single platform.
type LeaseTable struct {
	// key: account
	leases *xsync.Map[string, Lease]
	stats  *IPLoadStats
}

// IPLoadStats tracks the number of active leases per egress IP.
type IPLoadStats struct {
	counts *xsync.Map[netip.Addr, *atomic.Int64]
}

// NewLeaseTable creates a new lease table linked to the given stats.
func NewLeaseTable(stats *IPLoadStats) *LeaseTable {
	return &LeaseTable{
		leases: xsync.NewMap[string, Lease](),
		stats:  stats,
	}
}

// NewIPLoadStats creates a new IP load stats tracker.
func NewIPLoadStats() *IPLoadStats {
	return &IPLoadStats{
		counts: xsync.NewMap[netip.Addr, *atomic.Int64](),
	}
}

// GetLease returns the lease for the given account.
func (t *LeaseTable) GetLease(account string) (Lease, bool) {
	return t.leases.Load(account)
}

// Range iterates over all leases.
func (t *LeaseTable) Range(fn func(account string, lease Lease) bool) {
	t.leases.Range(fn)
}

// CreateLease adds a new lease and increments the IP load count.
// If a lease explicitly exists (e.g. race), it is overwritten (and counts adjusted).
func (t *LeaseTable) CreateLease(account string, lease Lease) {
	t.leases.Compute(account, func(oldVal Lease, loaded bool) (Lease, xsync.ComputeOp) {
		if loaded {
			t.stats.Dec(oldVal.EgressIP)
		}
		t.stats.Inc(lease.EgressIP)
		return lease, xsync.UpdateOp
	})
}

// DeleteLease atomically removes a lease and decrements IP load stats.
// Returns the deleted lease and true when a lease was actually deleted.
func (t *LeaseTable) DeleteLease(account string) (Lease, bool) {
	var deleted Lease
	ok := false
	t.leases.Compute(account, func(oldVal Lease, loaded bool) (Lease, xsync.ComputeOp) {
		if loaded {
			t.stats.Dec(oldVal.EgressIP)
			deleted = oldVal
			ok = true
			return oldVal, xsync.DeleteOp
		}
		return oldVal, xsync.CancelOp
	})
	return deleted, ok
}

// Size returns the number of leases in the table.
func (t *LeaseTable) Size() int {
	return t.leases.Size()
}

// Inc increments the lease count for an IP.
func (s *IPLoadStats) Inc(ip netip.Addr) {
	if !ip.IsValid() {
		return
	}
	ctr, _ := s.counts.LoadOrStore(ip, new(atomic.Int64))
	ctr.Add(1)
}

// Dec decrements the lease count for an IP.
func (s *IPLoadStats) Dec(ip netip.Addr) {
	if !ip.IsValid() {
		return
	}
	if ctr, ok := s.counts.Load(ip); ok {
		ctr.Add(-1)
		// We don't remove zero counters to avoid race conditions with concurrent Inc.
		// Since the set of egress IPs is bounded (by number of nodes), this leakage
		// is acceptable and bounded.
	}
}

// Get returns the current lease count for an IP.
func (s *IPLoadStats) Get(ip netip.Addr) int64 {
	if !ip.IsValid() {
		return 0
	}
	if ctr, ok := s.counts.Load(ip); ok {
		return ctr.Load()
	}
	return 0
}

// Snapshot returns a best-effort point-in-time copy of positive lease counts.
// Entries with non-positive counts are skipped.
func (s *IPLoadStats) Snapshot() map[netip.Addr]int64 {
	out := make(map[netip.Addr]int64)
	s.counts.Range(func(ip netip.Addr, ctr *atomic.Int64) bool {
		if !ip.IsValid() || ctr == nil {
			return true
		}
		n := ctr.Load()
		if n <= 0 {
			return true
		}
		out[ip] = n
		return true
	})
	return out
}

// IsExpired checks if a lease is expired relative to the given time.
func (l Lease) IsExpired(now time.Time) bool {
	return l.ExpiryNs < now.UnixNano()
}
