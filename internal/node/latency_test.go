package node

import (
	"testing"
	"time"
)

func TestLatencyTable_FirstRecord(t *testing.T) {
	lt := NewLatencyTable(16)

	wasEmpty := lt.Update("example.com", 100*time.Millisecond, 30*time.Second)
	if !wasEmpty {
		t.Fatal("should report wasEmpty on first-ever record")
	}

	stats, ok := lt.GetDomainStats("example.com")
	if !ok {
		t.Fatal("should find stats for example.com")
	}
	if stats.Ewma != 100*time.Millisecond {
		t.Fatalf("first Ewma should equal raw latency, got %v", stats.Ewma)
	}
}

func TestLatencyTable_SecondRecord_NotWasEmpty(t *testing.T) {
	lt := NewLatencyTable(16)

	lt.Update("example.com", 100*time.Millisecond, 30*time.Second)
	wasEmpty := lt.Update("example.com", 200*time.Millisecond, 30*time.Second)
	if wasEmpty {
		t.Fatal("should not report wasEmpty on second record")
	}
}

func TestLatencyTable_TDEWMA_Decay(t *testing.T) {
	lt := NewLatencyTable(16)

	// Preload with known stats.
	base := time.Now().Add(-10 * time.Second)
	lt.LoadEntry("example.com", DomainLatencyStats{
		Ewma:        100 * time.Millisecond,
		LastUpdated: base,
	})

	// Update with a much higher value.
	lt.Update("example.com", 500*time.Millisecond, 30*time.Second)

	stats, _ := lt.GetDomainStats("example.com")
	// New EWMA should be between old (100ms) and new (500ms).
	if stats.Ewma <= 100*time.Millisecond || stats.Ewma >= 500*time.Millisecond {
		t.Fatalf("EWMA should be between 100ms and 500ms, got %v", stats.Ewma)
	}
}

func TestLatencyTable_BoundedEviction(t *testing.T) {
	capacity := 4
	lt := NewLatencyTable(capacity)

	// Add more entries than capacity.
	for i := 0; i < capacity+10; i++ {
		domain := "domain" + string(rune('A'+i)) + ".com"
		lt.Update(domain, time.Duration(i+1)*time.Millisecond, 30*time.Second)
	}

	// Size may be <= capacity due to eviction (otter is probabilistic but bounded).
	if lt.Size() > capacity+2 { // allow small margin for async eviction
		t.Fatalf("expected at most %d entries (with margin), got %d", capacity+2, lt.Size())
	}
}

func TestLatencyTable_Range(t *testing.T) {
	lt := NewLatencyTable(16)

	lt.Update("a.com", 10*time.Millisecond, 30*time.Second)
	lt.Update("b.com", 20*time.Millisecond, 30*time.Second)

	count := 0
	lt.Range(func(domain string, stats DomainLatencyStats) bool {
		count++
		return true
	})
	if count != 2 {
		t.Fatalf("expected 2 entries in Range, got %d", count)
	}
}

func TestLatencyTable_NotFound(t *testing.T) {
	lt := NewLatencyTable(16)

	_, ok := lt.GetDomainStats("nonexistent.com")
	if ok {
		t.Fatal("should not find stats for nonexistent domain")
	}
}

func TestLatencyTable_LoadEntry(t *testing.T) {
	lt := NewLatencyTable(16)
	now := time.Now()

	lt.LoadEntry("test.com", DomainLatencyStats{
		Ewma:        50 * time.Millisecond,
		LastUpdated: now,
	})

	stats, ok := lt.GetDomainStats("test.com")
	if !ok {
		t.Fatal("should find loaded entry")
	}
	if stats.Ewma != 50*time.Millisecond {
		t.Fatalf("LoadEntry should preserve exact Ewma, got %v", stats.Ewma)
	}
}
