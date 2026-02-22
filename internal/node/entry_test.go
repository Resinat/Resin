package node

import (
	"net/netip"
	"regexp"
	"testing"
	"time"

	"github.com/resin-proxy/resin/internal/testutil"
)

func TestNodeEntry_SubscriptionIDs(t *testing.T) {
	e := NewNodeEntry(Hash{}, nil, time.Now(), 0)

	e.AddSubscriptionID("s1")
	e.AddSubscriptionID("s2")
	e.AddSubscriptionID("s1") // idempotent

	ids := e.SubscriptionIDs()
	if len(ids) != 2 {
		t.Fatalf("expected 2 subs, got %d: %v", len(ids), ids)
	}

	empty := e.RemoveSubscriptionID("s1")
	if empty {
		t.Fatal("should not be empty after removing s1")
	}
	if e.SubscriptionCount() != 1 {
		t.Fatalf("expected 1 sub, got %d", e.SubscriptionCount())
	}

	empty = e.RemoveSubscriptionID("s2")
	if !empty {
		t.Fatal("should be empty after removing s2")
	}

	// Idempotent remove.
	empty = e.RemoveSubscriptionID("s999")
	if !empty {
		t.Fatal("removing nonexistent should report empty if already empty")
	}
}

func TestNodeEntry_MatchRegexs_EmptyRegex(t *testing.T) {
	e := NewNodeEntry(Hash{}, nil, time.Now(), 0)
	if !e.MatchRegexs(nil, nil) {
		t.Fatal("empty regex list should match")
	}
	if !e.MatchRegexs([]*regexp.Regexp{}, nil) {
		t.Fatal("empty regex slice should match")
	}
}

func TestNodeEntry_MatchRegexs_EmptyRegex_RequiresEnabledSubscriptionWhenLookupProvided(t *testing.T) {
	h := HashFromRawOptions([]byte(`{"type":"ss"}`))
	e := NewNodeEntry(h, nil, time.Now(), 0)
	e.AddSubscriptionID("sub-disabled")

	lookup := func(subID string, hash Hash) (string, bool, []string, bool) {
		switch subID {
		case "sub-disabled":
			return "SubDisabled", false, []string{"node-a"}, true
		case "sub-enabled":
			return "SubEnabled", true, []string{"node-b"}, true
		default:
			return "", false, nil, false
		}
	}

	if e.MatchRegexs([]*regexp.Regexp{}, lookup) {
		t.Fatal("empty regex with lookup should not match when all subscriptions are disabled")
	}

	e.AddSubscriptionID("sub-enabled")
	if !e.MatchRegexs([]*regexp.Regexp{}, lookup) {
		t.Fatal("empty regex with lookup should match when any subscription is enabled")
	}
}

func TestNodeEntry_MatchRegexs_Basic(t *testing.T) {
	h := HashFromRawOptions([]byte(`{"type":"ss"}`))
	e := NewNodeEntry(h, nil, time.Now(), 0)
	e.AddSubscriptionID("sub-1")

	lookup := func(subID string, hash Hash) (string, bool, []string, bool) {
		if subID == "sub-1" {
			return "MySub", true, []string{"us-node", "fast"}, true
		}
		return "", false, nil, false
	}

	// Match "MySub/us-node" — should match regex "us".
	regexes := []*regexp.Regexp{regexp.MustCompile("us")}
	if !e.MatchRegexs(regexes, lookup) {
		t.Fatal("should match 'us' regex")
	}

	// Should not match "jp".
	regexes = []*regexp.Regexp{regexp.MustCompile("jp")}
	if e.MatchRegexs(regexes, lookup) {
		t.Fatal("should not match 'jp' regex")
	}
}

func TestNodeEntry_MatchRegexs_AllRegexesMustMatch(t *testing.T) {
	h := HashFromRawOptions([]byte(`{"type":"ss"}`))
	e := NewNodeEntry(h, nil, time.Now(), 0)
	e.AddSubscriptionID("sub-1")

	lookup := func(subID string, hash Hash) (string, bool, []string, bool) {
		return "Provider", true, []string{"us-fast-1"}, true
	}

	// Both "us" and "fast" match "Provider/us-fast-1".
	regexes := []*regexp.Regexp{
		regexp.MustCompile("us"),
		regexp.MustCompile("fast"),
	}
	if !e.MatchRegexs(regexes, lookup) {
		t.Fatal("both regexes should match")
	}

	// "us" matches but "jp" does not.
	regexes = []*regexp.Regexp{
		regexp.MustCompile("us"),
		regexp.MustCompile("jp"),
	}
	if e.MatchRegexs(regexes, lookup) {
		t.Fatal("should not match when one regex fails")
	}
}

func TestNodeEntry_MatchRegexs_DisabledSubSkipped(t *testing.T) {
	h := HashFromRawOptions([]byte(`{"type":"ss"}`))
	e := NewNodeEntry(h, nil, time.Now(), 0)
	e.AddSubscriptionID("sub-1")

	lookup := func(subID string, hash Hash) (string, bool, []string, bool) {
		return "MySub", false, []string{"us-node"}, true // disabled
	}

	regexes := []*regexp.Regexp{regexp.MustCompile("us")}
	if e.MatchRegexs(regexes, lookup) {
		t.Fatal("disabled sub should not contribute to match")
	}
}

func TestNodeEntry_MatchRegexs_MultiSub(t *testing.T) {
	h := HashFromRawOptions([]byte(`{"type":"ss"}`))
	e := NewNodeEntry(h, nil, time.Now(), 0)
	e.AddSubscriptionID("sub-1")
	e.AddSubscriptionID("sub-2")

	lookup := func(subID string, hash Hash) (string, bool, []string, bool) {
		switch subID {
		case "sub-1":
			return "Provider-A", true, []string{"eu-node"}, true
		case "sub-2":
			return "Provider-B", true, []string{"us-node"}, true
		}
		return "", false, nil, false
	}

	// Match "us" — should match via sub-2.
	regexes := []*regexp.Regexp{regexp.MustCompile("us")}
	if !e.MatchRegexs(regexes, lookup) {
		t.Fatal("should match via second subscription")
	}
}

func TestNodeEntry_CircuitBreaker(t *testing.T) {
	e := NewNodeEntry(Hash{}, nil, time.Now(), 0)
	if e.IsCircuitOpen() {
		t.Fatal("should not be circuit-open by default")
	}

	e.CircuitOpenSince.Store(time.Now().UnixNano())
	if !e.IsCircuitOpen() {
		t.Fatal("should be circuit-open after store")
	}

	e.CircuitOpenSince.Store(0)
	if e.IsCircuitOpen() {
		t.Fatal("should not be circuit-open after reset")
	}
}

func TestNodeEntry_LatencyCount(t *testing.T) {
	e := NewNodeEntry(Hash{}, nil, time.Now(), 16)
	if e.HasLatency() {
		t.Fatal("should not have latency by default")
	}

	e.LatencyTable.LoadEntry("example.com", DomainLatencyStats{
		Ewma:        100 * time.Millisecond,
		LastUpdated: time.Now(),
	})
	if !e.HasLatency() {
		t.Fatal("should have latency after adding an entry")
	}
}

func TestNodeEntry_Outbound(t *testing.T) {
	e := NewNodeEntry(Hash{}, nil, time.Now(), 0)
	if e.HasOutbound() {
		t.Fatal("should not have outbound by default")
	}

	ob := testutil.NewNoopOutbound()
	e.Outbound.Store(&ob)
	if !e.HasOutbound() {
		t.Fatal("should have outbound after store")
	}
}

func TestNodeEntry_EgressIP(t *testing.T) {
	e := NewNodeEntry(Hash{}, nil, time.Now(), 0)

	// Before any store, should return zero addr.
	addr := e.GetEgressIP()
	if addr.IsValid() {
		t.Fatal("should be invalid before first store")
	}

	ip := netip.MustParseAddr("1.2.3.4")
	e.SetEgressIP(ip)
	if got := e.GetEgressIP(); got != ip {
		t.Fatalf("expected %s, got %s", ip, got)
	}
}

func TestNodeEntry_EgressRegion(t *testing.T) {
	e := NewNodeEntry(Hash{}, nil, time.Now(), 0)

	if got := e.GetEgressRegion(); got != "" {
		t.Fatalf("default egress region: got %q, want empty", got)
	}

	e.SetEgressRegion("US")
	if got := e.GetEgressRegion(); got != "us" {
		t.Fatalf("normalized egress region: got %q, want %q", got, "us")
	}

	e.SetEgressRegion("")
	if got := e.GetEgressRegion(); got != "" {
		t.Fatalf("cleared egress region: got %q, want empty", got)
	}
}

func TestNodeEntry_GetRegion_UsesStoredThenGeoIPFallback(t *testing.T) {
	e := NewNodeEntry(Hash{}, nil, time.Now(), 0)
	e.SetEgressIP(netip.MustParseAddr("203.0.113.1"))

	geoLookupCalled := false
	geoLookup := func(_ netip.Addr) string {
		geoLookupCalled = true
		return "jp"
	}

	if got := e.GetRegion(geoLookup); got != "jp" {
		t.Fatalf("fallback region: got %q, want %q", got, "jp")
	}
	if !geoLookupCalled {
		t.Fatal("expected geo lookup to be called without stored region")
	}

	geoLookupCalled = false
	e.SetEgressRegion("US")
	if got := e.GetRegion(geoLookup); got != "us" {
		t.Fatalf("stored region: got %q, want %q", got, "us")
	}
	if geoLookupCalled {
		t.Fatal("geo lookup should not be called when stored region exists")
	}
}
