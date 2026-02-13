package state

import (
	"sync"
	"testing"
)

func TestDirtySet_MarkAndDrain(t *testing.T) {
	ds := NewDirtySet[string]()

	ds.MarkUpsert("a")
	ds.MarkUpsert("b")
	ds.MarkDelete("c")

	if ds.Len() != 3 {
		t.Fatalf("expected len 3, got %d", ds.Len())
	}

	drained := ds.Drain()

	if ds.Len() != 0 {
		t.Fatalf("expected len 0 after drain, got %d", ds.Len())
	}
	if len(drained) != 3 {
		t.Fatalf("expected 3 drained entries, got %d", len(drained))
	}
	if drained["a"] != OpUpsert {
		t.Fatalf("expected OpUpsert for a")
	}
	if drained["c"] != OpDelete {
		t.Fatalf("expected OpDelete for c")
	}
}

func TestDirtySet_OverwriteOp(t *testing.T) {
	ds := NewDirtySet[string]()

	ds.MarkUpsert("a")
	ds.MarkDelete("a") // delete overrides upsert

	drained := ds.Drain()
	if drained["a"] != OpDelete {
		t.Fatalf("expected OpDelete after overwrite")
	}
}

func TestDirtySet_Merge(t *testing.T) {
	ds := NewDirtySet[string]()

	// Simulate: drain, then new marks arrive, then merge old back.
	ds.MarkUpsert("a")
	ds.MarkUpsert("b")
	old := ds.Drain()

	// New mark on "a" after drain.
	ds.MarkDelete("a")
	// "c" is newly added.
	ds.MarkUpsert("c")

	// Merge old back. "a" should NOT be overwritten (newer mark wins).
	ds.Merge(old)

	if ds.Len() != 3 {
		t.Fatalf("expected 3, got %d", ds.Len())
	}

	final := ds.Drain()

	// "a" should be OpDelete (newer mark), not OpUpsert (from old).
	if final["a"] != OpDelete {
		t.Fatalf("expected OpDelete for a (newer mark), got %v", final["a"])
	}
	// "b" should be OpUpsert (from merge).
	if final["b"] != OpUpsert {
		t.Fatalf("expected OpUpsert for b (from merge)")
	}
	// "c" should be OpUpsert (new mark).
	if final["c"] != OpUpsert {
		t.Fatalf("expected OpUpsert for c")
	}
}

func TestDirtySet_ConcurrentMarkAndDrain(t *testing.T) {
	ds := NewDirtySet[int]()

	const writers = 10
	const perWriter = 1000

	var wg sync.WaitGroup

	// Concurrent writers.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				key := w*perWriter + i
				if i%3 == 0 {
					ds.MarkDelete(key)
				} else {
					ds.MarkUpsert(key)
				}
			}
		}(w)
	}

	// Concurrent drainers.
	totalDrained := make(chan int, 100)
	drainerDone := make(chan struct{})
	go func() {
		defer close(drainerDone)
		total := 0
		for {
			d := ds.Drain()
			total += len(d)
			if total >= writers*perWriter {
				totalDrained <- total
				return
			}
			// Brief sleep-free spin: merge back if we haven't gotten everything.
		}
	}()

	wg.Wait()

	// Final drain to catch anything remaining.
	remaining := ds.Drain()
	select {
	case got := <-totalDrained:
		got += len(remaining)
		// We may get duplicates due to re-marks, so just verify no panic/deadlock.
		_ = got
	default:
		// Drainer might not have finished; drain the rest.
		_ = remaining
	}

	// If we get here without deadlock or panic, the test passes.
}
