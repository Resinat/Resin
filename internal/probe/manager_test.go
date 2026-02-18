package probe

import (
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/testutil"
	"github.com/resin-proxy/resin/internal/topology"
)

// storeOutbound sets a non-nil outbound on the entry.
func storeOutbound(entry *node.NodeEntry) {
	ob := testutil.NewNoopOutbound()
	entry.Outbound.Store(&ob)
}

// TestProbeEgress_Success verifies that a successful egress probe calls
// RecordResult(true), RecordLatency("cloudflare.com"), and UpdateNodeEgressIP.
func TestProbeEgress_Success(t *testing.T) {
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
	})

	hash := node.HashFromRawOptions([]byte(`{"type":"egress-ok"}`))
	pool.AddNodeFromSub(hash, []byte(`{"type":"egress-ok"}`), "sub1")

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("entry not found")
	}
	storeOutbound(entry)

	traceBody := []byte("fl=123\nip=203.0.113.1\nts=1234567890")
	mgr := NewProbeManager(ProbeConfig{
		Pool: pool,
		Fetcher: func(_ node.Hash, url string) ([]byte, time.Duration, error) {
			return traceBody, 42 * time.Millisecond, nil
		},
	})

	mgr.probeEgress(hash, entry)

	// Verify RecordResult(true) was applied.
	if entry.FailureCount.Load() != 0 {
		t.Fatalf("expected 0 failures, got %d", entry.FailureCount.Load())
	}
	if entry.CircuitOpenSince.Load() != 0 {
		t.Fatal("circuit should not be open")
	}

	// Verify UpdateNodeEgressIP.
	got := entry.GetEgressIP()
	want := netip.MustParseAddr("203.0.113.1")
	if got != want {
		t.Fatalf("egress IP: got %v, want %v", got, want)
	}

	// Verify RecordLatency for cloudflare.com.
	if !entry.HasLatency() {
		t.Fatal("expected latency data")
	}
	stats, ok := entry.LatencyTable.GetDomainStats("cloudflare.com")
	if !ok {
		t.Fatal("expected cloudflare.com latency entry")
	}
	if stats.Ewma != 42*time.Millisecond {
		t.Fatalf("ewma: got %v, want %v", stats.Ewma, 42*time.Millisecond)
	}
}

// TestProbeEgress_Failure verifies that a failed egress probe calls
// RecordResult(false) and accumulates failure count.
func TestProbeEgress_Failure(t *testing.T) {
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
	})

	hash := node.HashFromRawOptions([]byte(`{"type":"egress-fail"}`))
	pool.AddNodeFromSub(hash, []byte(`{"type":"egress-fail"}`), "sub1")

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("entry not found")
	}
	storeOutbound(entry)

	mgr := NewProbeManager(ProbeConfig{
		Pool: pool,
		Fetcher: func(_ node.Hash, url string) ([]byte, time.Duration, error) {
			return nil, 0, errors.New("connection refused")
		},
	})

	mgr.probeEgress(hash, entry)

	if entry.FailureCount.Load() != 1 {
		t.Fatalf("expected 1 failure, got %d", entry.FailureCount.Load())
	}

	// No latency or egress IP should be recorded.
	if entry.HasLatency() {
		t.Fatal("should not have latency on failure")
	}
}

// TestProbeEgress_CircuitBreak verifies consecutive failures trigger circuit break.
func TestProbeEgress_CircuitBreak(t *testing.T) {
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 2 },
	})

	hash := node.HashFromRawOptions([]byte(`{"type":"egress-circuit"}`))
	pool.AddNodeFromSub(hash, []byte(`{"type":"egress-circuit"}`), "sub1")

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("entry not found")
	}
	storeOutbound(entry)

	mgr := NewProbeManager(ProbeConfig{
		Pool: pool,
		Fetcher: func(_ node.Hash, url string) ([]byte, time.Duration, error) {
			return nil, 0, errors.New("timeout")
		},
	})

	mgr.probeEgress(hash, entry)
	mgr.probeEgress(hash, entry)

	if entry.CircuitOpenSince.Load() == 0 {
		t.Fatal("circuit should be open after 2 consecutive failures")
	}
}

// TestProbeLatency_Success verifies latency probe writes RecordResult+RecordLatency.
func TestProbeLatency_Success(t *testing.T) {
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
	})

	hash := node.HashFromRawOptions([]byte(`{"type":"latency-ok"}`))
	pool.AddNodeFromSub(hash, []byte(`{"type":"latency-ok"}`), "sub1")

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("entry not found")
	}
	storeOutbound(entry)

	mgr := NewProbeManager(ProbeConfig{
		Pool: pool,
		Fetcher: func(_ node.Hash, url string) ([]byte, time.Duration, error) {
			return []byte("OK"), 50 * time.Millisecond, nil
		},
	})

	mgr.probeLatency(hash, entry, "https://www.gstatic.com/generate_204")

	if entry.FailureCount.Load() != 0 {
		t.Fatalf("expected 0 failures, got %d", entry.FailureCount.Load())
	}

	// Should have latency recorded.
	if !entry.HasLatency() {
		t.Fatal("expected latency data")
	}
}

// TestProbeLatency_Failure verifies latency probe failure calls RecordResult(false).
func TestProbeLatency_Failure(t *testing.T) {
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
	})

	hash := node.HashFromRawOptions([]byte(`{"type":"latency-fail"}`))
	pool.AddNodeFromSub(hash, []byte(`{"type":"latency-fail"}`), "sub1")

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("entry not found")
	}
	storeOutbound(entry)

	mgr := NewProbeManager(ProbeConfig{
		Pool: pool,
		Fetcher: func(_ node.Hash, url string) ([]byte, time.Duration, error) {
			return nil, 0, errors.New("tls handshake failed")
		},
	})

	mgr.probeLatency(hash, entry, "https://www.gstatic.com/generate_204")

	if entry.FailureCount.Load() != 1 {
		t.Fatalf("expected 1 failure, got %d", entry.FailureCount.Load())
	}
}

// TestProbeEgress_ZeroLatencyIgnored verifies that a successful probe with a
// non-positive latency sample does not write latency stats.
func TestProbeEgress_ZeroLatencyIgnored(t *testing.T) {
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
	})

	hash := node.HashFromRawOptions([]byte(`{"type":"egress-zero-latency"}`))
	pool.AddNodeFromSub(hash, []byte(`{"type":"egress-zero-latency"}`), "sub1")

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("entry not found")
	}
	storeOutbound(entry)

	traceBody := []byte("fl=123\nip=203.0.113.1\nts=1234567890")
	mgr := NewProbeManager(ProbeConfig{
		Pool: pool,
		Fetcher: func(_ node.Hash, url string) ([]byte, time.Duration, error) {
			return traceBody, 0, nil
		},
	})

	mgr.probeEgress(hash, entry)

	if entry.FailureCount.Load() != 0 {
		t.Fatalf("expected 0 failures, got %d", entry.FailureCount.Load())
	}
	if entry.HasLatency() {
		t.Fatal("zero latency sample should be ignored")
	}
	if got := entry.GetEgressIP(); got != netip.MustParseAddr("203.0.113.1") {
		t.Fatalf("egress IP: got %v, want 203.0.113.1", got)
	}
}

// TestProbeLatency_ZeroLatencyIgnored verifies that successful latency probes
// skip latency writeback when sample <= 0.
func TestProbeLatency_ZeroLatencyIgnored(t *testing.T) {
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
	})

	hash := node.HashFromRawOptions([]byte(`{"type":"latency-zero"}`))
	pool.AddNodeFromSub(hash, []byte(`{"type":"latency-zero"}`), "sub1")

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("entry not found")
	}
	storeOutbound(entry)

	mgr := NewProbeManager(ProbeConfig{
		Pool: pool,
		Fetcher: func(_ node.Hash, url string) ([]byte, time.Duration, error) {
			return []byte("ok"), 0, nil
		},
	})

	mgr.probeLatency(hash, entry, "https://www.gstatic.com/generate_204")

	if entry.FailureCount.Load() != 0 {
		t.Fatalf("expected 0 failures, got %d", entry.FailureCount.Load())
	}
	if entry.HasLatency() {
		t.Fatal("zero latency sample should be ignored")
	}
}

// TestProbeEgress_NilFetcher verifies graceful handling of nil fetcher.
func TestProbeEgress_NilFetcher(t *testing.T) {
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
	})

	hash := node.HashFromRawOptions([]byte(`{"type":"nil-fetcher"}`))
	pool.AddNodeFromSub(hash, []byte(`{"type":"nil-fetcher"}`), "sub1")

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("entry not found")
	}

	mgr := NewProbeManager(ProbeConfig{Pool: pool}) // no Fetcher
	mgr.probeEgress(hash, entry)                    // should not panic
}

// TestTriggerImmediateEgressProbe_WithFetcher is an integration test
// verifying async probe + health writeback.
func TestTriggerImmediateEgressProbe_WithFetcher(t *testing.T) {
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
	})

	hash := node.HashFromRawOptions([]byte(`{"type":"trigger-egress"}`))
	pool.AddNodeFromSub(hash, []byte(`{"type":"trigger-egress"}`), "sub1")

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("entry not found")
	}
	storeOutbound(entry)

	var called sync.WaitGroup
	called.Add(1)
	mgr := NewProbeManager(ProbeConfig{
		Pool:        pool,
		Concurrency: 1,
		Fetcher: func(_ node.Hash, url string) ([]byte, time.Duration, error) {
			defer called.Done()
			return []byte("ip=198.51.100.1"), 10 * time.Millisecond, nil
		},
	})

	mgr.TriggerImmediateEgressProbe(hash)
	called.Wait()

	// Allow goroutines to complete.
	time.Sleep(20 * time.Millisecond)

	got := entry.GetEgressIP()
	if got != netip.MustParseAddr("198.51.100.1") {
		t.Fatalf("egress IP: got %v, want 198.51.100.1", got)
	}
}

func TestProbeManager_StopWaitsImmediateProbe(t *testing.T) {
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
	})

	hash := node.HashFromRawOptions([]byte(`{"type":"stop-immediate"}`))
	pool.AddNodeFromSub(hash, []byte(`{"type":"stop-immediate"}`), "sub1")
	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("entry not found")
	}
	storeOutbound(entry)

	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32

	mgr := NewProbeManager(ProbeConfig{
		Pool:        pool,
		Concurrency: 1,
		Fetcher: func(_ node.Hash, url string) ([]byte, time.Duration, error) {
			if calls.Add(1) == 1 {
				close(started)
				<-release
			}
			return []byte("ip=203.0.113.1"), 10 * time.Millisecond, nil
		},
	})

	mgr.TriggerImmediateEgressProbe(hash)
	<-started

	stopDone := make(chan struct{})
	go func() {
		mgr.Stop()
		close(stopDone)
	}()

	select {
	case <-stopDone:
		t.Fatal("Stop returned before in-flight immediate probe finished")
	case <-time.After(30 * time.Millisecond):
	}

	close(release)
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("Stop did not complete after in-flight immediate probe finished")
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 probe call, got %d", got)
	}
}

func TestProbeLatencySync_ReturnsEWMAFromNormalizedDomain(t *testing.T) {
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
	})

	hash := node.HashFromRawOptions([]byte(`{"type":"latency-sync"}`))
	pool.AddNodeFromSub(hash, []byte(`{"type":"latency-sync"}`), "sub1")

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("entry not found")
	}
	storeOutbound(entry)

	mgr := NewProbeManager(ProbeConfig{
		Pool: pool,
		Fetcher: func(_ node.Hash, url string) ([]byte, time.Duration, error) {
			return []byte("ok"), 80 * time.Millisecond, nil
		},
		LatencyTestURL: func() string {
			return "https://www.gstatic.com/generate_204"
		},
	})

	result, err := mgr.ProbeLatencySync(hash)
	if err != nil {
		t.Fatalf("ProbeLatencySync: %v", err)
	}
	if result.LatencyEwmaMs <= 0 {
		t.Fatalf("latency_ewma_ms = %f, want > 0", result.LatencyEwmaMs)
	}

	stats, ok := entry.LatencyTable.GetDomainStats("gstatic.com")
	if !ok {
		t.Fatal("expected normalized domain latency entry for gstatic.com")
	}
	if stats.Ewma != 80*time.Millisecond {
		t.Fatalf("stored EWMA = %v, want %v", stats.Ewma, 80*time.Millisecond)
	}
}

// TestParseCloudflareTrace_Success verifies IP extraction from trace body.
func TestParseCloudflareTrace_Success(t *testing.T) {
	body := []byte("fl=abc\nip=1.2.3.4\nts=12345")
	addr, err := ParseCloudflareTrace(body)
	if err != nil {
		t.Fatal(err)
	}
	if addr != netip.MustParseAddr("1.2.3.4") {
		t.Fatalf("got %v, want 1.2.3.4", addr)
	}
}

// TestParseCloudflareTrace_NoIP verifies error when ip= field is missing.
func TestParseCloudflareTrace_NoIP(t *testing.T) {
	body := []byte("fl=abc\nts=12345")
	_, err := ParseCloudflareTrace(body)
	if err == nil {
		t.Fatal("expected error when ip field is missing")
	}
}
