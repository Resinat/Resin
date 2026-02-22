package metrics

import "testing"

func TestCollector_RecordLatency_BoundaryAndOverflowBuckets(t *testing.T) {
	c := NewCollector(100, 3000)

	// Boundary value: overflow_ms itself should go to overflow bucket.
	c.RecordRequest("", true, 3000, false)
	// Strictly greater than overflow_ms should go to overflow bucket.
	c.RecordRequest("", true, 3001, false)
	// 100ms should be in second bucket [100,200), not first.
	c.RecordRequest("", true, 100, false)

	snap := c.Snapshot()
	regularBins := (3000 + 100 - 1) / 100
	if len(snap.LatencyBuckets) != regularBins+1 {
		t.Fatalf("bucket count: got %d, want %d", len(snap.LatencyBuckets), regularBins+1)
	}

	if snap.LatencyBuckets[0] != 0 {
		t.Fatalf("first bucket count: got %d, want %d", snap.LatencyBuckets[0], 0)
	}
	if snap.LatencyBuckets[1] != 1 {
		t.Fatalf("second bucket count: got %d, want %d", snap.LatencyBuckets[1], 1)
	}
	if snap.LatencyBuckets[regularBins-1] != 0 {
		t.Fatalf("last regular bucket count: got %d, want %d", snap.LatencyBuckets[regularBins-1], 0)
	}
	if snap.LatencyBuckets[regularBins] != 2 {
		t.Fatalf("overflow bucket count: got %d, want %d", snap.LatencyBuckets[regularBins], 2)
	}
}

func TestCollector_SwapConnectionWindowMax_TracksPeakAndResetsBaseline(t *testing.T) {
	c := NewCollector(100, 3000)

	// inbound: 0 -> 1 -> 2 -> 1, outbound: 0 -> 1 -> 0
	c.RecordConnection(ConnInbound, 1)
	c.RecordConnection(ConnInbound, 1)
	c.RecordConnection(ConnInbound, -1)
	c.RecordConnection(ConnOutbound, 1)
	c.RecordConnection(ConnOutbound, -1)

	inboundMax, outboundMax := c.SwapConnectionWindowMax()
	if inboundMax != 2 || outboundMax != 1 {
		t.Fatalf("first window max mismatch: inbound=%d outbound=%d", inboundMax, outboundMax)
	}

	// No new events: next window max should reflect current active levels.
	inboundMax, outboundMax = c.SwapConnectionWindowMax()
	if inboundMax != 1 || outboundMax != 0 {
		t.Fatalf("second window max mismatch: inbound=%d outbound=%d", inboundMax, outboundMax)
	}
}
