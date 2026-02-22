package platform

import (
	"math/rand/v2"
	"strconv"
	"sync"
	"testing"

	"github.com/Resinat/Resin/internal/node"
)

func makeHash(data string) node.Hash {
	return node.HashFromRawOptions([]byte(data))
}

func TestRoutableView_AddRemoveContains(t *testing.T) {
	rv := NewRoutableView()
	h1 := makeHash(`{"type":"ss","server":"1.1.1.1"}`)
	h2 := makeHash(`{"type":"ss","server":"2.2.2.2"}`)

	rv.Add(h1)
	rv.Add(h2)
	rv.Add(h1) // duplicate add

	if rv.Size() != 2 {
		t.Fatalf("expected size 2, got %d", rv.Size())
	}
	if !rv.Contains(h1) || !rv.Contains(h2) {
		t.Fatal("should contain both hashes")
	}

	rv.Remove(h1)
	if rv.Contains(h1) {
		t.Fatal("should not contain h1 after remove")
	}
	if rv.Size() != 1 {
		t.Fatalf("expected size 1, got %d", rv.Size())
	}

	// Remove non-existent — no-op.
	rv.Remove(h1)
	if rv.Size() != 1 {
		t.Fatal("size should still be 1")
	}
}

func TestRoutableView_RandomPick_Empty(t *testing.T) {
	rv := NewRoutableView()
	rng := rand.New(rand.NewPCG(1, 2))
	_, ok := rv.RandomPick(rng)
	if ok {
		t.Fatal("should return ok=false for empty view")
	}
}

func TestRoutableView_RandomPick_Single(t *testing.T) {
	rv := NewRoutableView()
	h := makeHash(`{"type":"ss"}`)
	rv.Add(h)

	rng := rand.New(rand.NewPCG(1, 2))
	got, ok := rv.RandomPick(rng)
	if !ok || got != h {
		t.Fatalf("expected %s, got %s (ok=%v)", h.Hex(), got.Hex(), ok)
	}
}

func TestRoutableView_RandomPick_Distribution(t *testing.T) {
	rv := NewRoutableView()
	hashes := make([]node.Hash, 100)
	for i := range hashes {
		h := makeHash(`{"type":"ss","id":` + string(rune('A'+i%26)) + `,"n":` + strconv.Itoa(i) + `}`)
		hashes[i] = h
		rv.Add(h)
	}

	rng := rand.New(rand.NewPCG(42, 99))
	picked := make(map[node.Hash]int)
	for i := 0; i < 10000; i++ {
		h, ok := rv.RandomPick(rng)
		if !ok {
			t.Fatal("should always pick from non-empty view")
		}
		picked[h]++
	}

	// Verify reasonable distribution (each hash picked at least once in 10k picks of 100 items).
	if len(picked) < 50 {
		t.Fatalf("poor distribution: only %d of 100 items picked", len(picked))
	}
}

func TestRoutableView_Clear(t *testing.T) {
	rv := NewRoutableView()
	for i := 0; i < 10; i++ {
		rv.Add(makeHash(`{"n":` + strconv.Itoa(i) + `}`))
	}
	if rv.Size() != 10 {
		t.Fatal("expected 10")
	}
	rv.Clear()
	if rv.Size() != 0 {
		t.Fatalf("expected 0 after clear, got %d", rv.Size())
	}
}

func TestRoutableView_Range(t *testing.T) {
	rv := NewRoutableView()
	h1 := makeHash(`{"a":1}`)
	h2 := makeHash(`{"a":2}`)
	rv.Add(h1)
	rv.Add(h2)

	seen := make(map[node.Hash]bool)
	rv.Range(func(h node.Hash) bool {
		seen[h] = true
		return true
	})
	if len(seen) != 2 {
		t.Fatalf("expected 2 in range, got %d", len(seen))
	}
}

func TestRoutableView_ConcurrentAddRemove(t *testing.T) {
	rv := NewRoutableView()
	hashes := make([]node.Hash, 200)
	for i := range hashes {
		hashes[i] = makeHash(`{"type":"ss","idx":` + strconv.Itoa(i) + `}`)
	}

	var wg sync.WaitGroup
	// Add all concurrently.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				rv.Add(hashes[base*20+j])
			}
		}(i)
	}
	wg.Wait()

	if rv.Size() != 200 {
		t.Fatalf("expected 200 after concurrent add, got %d", rv.Size())
	}

	// Remove half concurrently.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				rv.Remove(hashes[base*20+j])
			}
		}(i)
	}
	wg.Wait()

	if rv.Size() != 100 {
		t.Fatalf("expected 100 after concurrent remove, got %d", rv.Size())
	}
}
