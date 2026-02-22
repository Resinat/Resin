package routing

import (
	"time"

	"github.com/Resinat/Resin/internal/node"
)

func lookupRecentDomainLatency(
	entry *node.NodeEntry,
	domain string,
	now time.Time,
	window time.Duration,
) (time.Duration, bool) {
	if entry == nil || entry.LatencyTable == nil || domain == "" {
		return 0, false
	}
	stats, ok := entry.LatencyTable.GetDomainStats(domain)
	if !ok || !isRecent(stats.LastUpdated, now, window) {
		return 0, false
	}
	return stats.Ewma, true
}

func averageRecentAuthorityLatency(
	entry *node.NodeEntry,
	authorities []string,
	now time.Time,
	window time.Duration,
) (time.Duration, bool) {
	if entry == nil || entry.LatencyTable == nil || len(authorities) == 0 {
		return 0, false
	}
	var sum time.Duration
	var count int64
	for _, domain := range authorities {
		latency, ok := lookupRecentDomainLatency(entry, domain, now, window)
		if !ok {
			continue
		}
		sum += latency
		count++
	}
	if count == 0 {
		return 0, false
	}
	return time.Duration(int64(sum) / count), true
}

func averageComparableAuthorityLatencies(
	e1, e2 *node.NodeEntry,
	authorities []string,
	now time.Time,
	window time.Duration,
) (time.Duration, time.Duration, bool) {
	if e1 == nil || e2 == nil || e1.LatencyTable == nil || e2.LatencyTable == nil || len(authorities) == 0 {
		return 0, 0, false
	}
	var sum1, sum2 time.Duration
	var count int64
	for _, domain := range authorities {
		latency1, ok1 := lookupRecentDomainLatency(e1, domain, now, window)
		latency2, ok2 := lookupRecentDomainLatency(e2, domain, now, window)
		if !ok1 || !ok2 {
			continue
		}
		sum1 += latency1
		sum2 += latency2
		count++
	}
	if count == 0 {
		return 0, 0, false
	}
	return time.Duration(int64(sum1) / count), time.Duration(int64(sum2) / count), true
}
