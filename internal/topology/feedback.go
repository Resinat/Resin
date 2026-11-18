package topology

import (
	"context"
	"errors"
	"time"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/netutil"
	"github.com/Resinat/Resin/internal/node"
	"github.com/puzpuzpuz/xsync/v4"
)

// ApplyFeedback rejects absent/zero versions. No caller code runs under the
// node table lock, and all external notifications follow its release.
func (p *GlobalNodePool) ApplyFeedback(hash node.Hash, version uint64, f node.Feedback) bool {
	if version == 0 {
		return false
	}
	if errors.Is(f.Err, context.Canceled) {
		f.Result = nil
	}
	var relay *node.RelayError
	failedViaRelay := f.Result != nil && !*f.Result &&
		errors.As(f.Err, &relay)
	if failedViaRelay && relay.ServerDial {
		// Keep the landing's probe attempt/error, but not a failure sample.
		f.Result = nil
	}
	applied := p.applyFeedback(hash, version, f)
	if !applied {
		return applied
	}
	if f.Result != nil && *f.Result && !f.Relay.Hash.IsZero() && f.Relay.Version != 0 {
		p.ApplyFeedback(f.Relay.Hash, f.Relay.Version, node.Feedback{Result: f.Result, PlatformID: f.PlatformID})
	}
	if !failedViaRelay {
		return applied
	}
	if relay.ServerDial {
		failed, message := false, relay.Error()
		p.ApplyFeedback(relay.Hash, relay.Version, node.Feedback{
			Result: &failed, LastError: &message, PlatformID: f.PlatformID,
		})
	} else if p.onRelaySuspect != nil {
		if entry, ok := p.GetEntry(relay.Hash); ok && entry.RoutingVersion() == relay.Version && entry.RoutingRole() == node.RoleRelay {
			p.onRelaySuspect(relay.Hash)
		}
	}
	return applied
}
func (p *GlobalNodePool) applyFeedback(hash node.Hash, version uint64, f node.Feedback) bool {
	failed := (f.Result != nil && !*f.Result || f.Err != nil) && !errors.Is(f.Err, context.Canceled)
	binding := node.BindingFromError(f.Err)
	if binding == nil {
		binding = f.Relay.Binding
	}
	if f.Result != nil && !*f.Result && p.passiveCircuitBreakerDisabled(f.PlatformID) {
		f.Result = nil
	}
	threshold := p.currentMaxConsecutiveFailures()
	domain := netutil.ExtractDomain(f.Target)
	authority := p.isAuthorityDomain(domain)
	decay := 30 * time.Second
	if p.latencyDecayWindow != nil && p.latencyDecayWindow() > 0 {
		decay = p.latencyDecayWindow()
	}
	applied, dynamic, dirty := false, false, false
	revokeBinding := false
	var domains []string
	p.nodes.Compute(hash, func(e *node.NodeEntry, loaded bool) (*node.NodeEntry, xsync.ComputeOp) {
		if !loaded || (version != 0 && e.RoutingVersion() != version) {
			return e, xsync.CancelOp
		}
		applied = true
		if f.Result != nil {
			before := e.IsCircuitOpen()
			if *f.Result {
				dynamic = e.FailureCount.Swap(0) != 0
				dynamic = e.CircuitOpenSince.Swap(0) != 0 || dynamic
			} else {
				count := e.FailureCount.Add(1)
				dynamic = true
				if threshold > 0 && int(count) >= threshold {
					if e.CircuitOpenSince.CompareAndSwap(0, time.Now().UnixNano()) {
						e.CircuitGeneration.Add(1)
					}
				}
			}
			dirty = before != e.IsCircuitOpen()
		}
		if f.Target != "" {
			now := time.Now().UnixNano()
			e.LastLatencyProbeAttempt.Store(now)
			if authority {
				e.LastAuthorityLatencyProbeAttempt.Store(now)
			}
			dynamic = true
			if f.Latency != nil && *f.Latency > 0 && e.LatencyTable != nil {
				empty, evicted, ok := e.LatencyTable.UpdateClassified(domain, *f.Latency, decay, authority)
				dirty = dirty || empty
				domains = append(domains, domain)
				if ok {
					domains = append(domains, evicted)
				}
			}
		}
		if f.Egress {
			oldIP, oldRegion := e.GetEgressIP(), e.GetEgressRegion()
			now := time.Now().UnixNano()
			e.LastEgressUpdateAttempt.Store(now)
			dynamic = true
			if f.IP != nil {
				e.SetEgressIP(*f.IP)
				e.LastEgressUpdate.Store(now)
			}
			if f.Region != nil {
				e.SetEgressRegion(*f.Region)
			} else if f.IP != nil && oldIP != *f.IP {
				e.SetEgressRegion("")
			}
			dirty = dirty || oldIP != e.GetEgressIP() || oldRegion != e.GetEgressRegion()
		}
		if f.LastError != nil {
			e.SetLastError(*f.LastError)
		}
		revokeBinding = failed && binding != nil && e.IsCircuitOpen()
		return e, xsync.CancelOp
	})
	if revokeBinding {
		binding.Invalidate()
	}
	if dirty {
		p.notifyAllPlatformsDirty(hash)
	}
	if dynamic && p.onNodeDynamicChanged != nil {
		p.onNodeDynamicChanged(hash)
	}
	if p.onNodeLatencyChanged != nil {
		for _, d := range domains {
			p.onNodeLatencyChanged(hash, d)
		}
	}
	return applied
}

// SnapshotNodeDynamic serializes the persisted mode marker with its data using
// the existing per-node map operation, without acquiring another mutex.
func (p *GlobalNodePool) SnapshotNodeDynamic(hash node.Hash) *model.NodeDynamic {
	var result *model.NodeDynamic
	p.nodes.Compute(hash, func(e *node.NodeEntry, ok bool) (*node.NodeEntry, xsync.ComputeOp) {
		if ok {
			ip := ""
			if addr := e.GetEgressIP(); addr.IsValid() {
				ip = addr.String()
			}
			result = &model.NodeDynamic{
				Hash: hash.Hex(), ChainProxy: e.RoutingRole() == node.RoleChain,
				FailureCount: int(e.FailureCount.Load()), CircuitOpenSince: e.CircuitOpenSince.Load(),
				EgressIP: ip, EgressRegion: e.GetEgressRegion(), EgressUpdatedAtNs: e.LastEgressUpdate.Load(),
				LastLatencyProbeAttemptNs:          e.LastLatencyProbeAttempt.Load(),
				LastAuthorityLatencyProbeAttemptNs: e.LastAuthorityLatencyProbeAttempt.Load(),
				LastEgressUpdateAttemptNs:          e.LastEgressUpdateAttempt.Load(),
			}
		}
		return e, xsync.CancelOp
	})
	return result
}
func (p *GlobalNodePool) SnapshotNodeLatency(hash node.Hash, domain string) *model.NodeLatency {
	var result *model.NodeLatency
	p.nodes.Compute(hash, func(e *node.NodeEntry, ok bool) (*node.NodeEntry, xsync.ComputeOp) {
		if ok && e.LatencyTable != nil {
			if stats, found := e.LatencyTable.GetDomainStats(domain); found {
				result = &model.NodeLatency{NodeHash: hash.Hex(), Domain: domain, EwmaNs: int64(stats.Ewma), LastUpdatedNs: stats.LastUpdated.UnixNano(), ChainProxy: e.RoutingRole() == node.RoleChain}
			}
		}
		return e, xsync.CancelOp
	})
	return result
}
