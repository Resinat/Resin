package subscription

import (
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/puzpuzpuz/xsync/v4"
)

func TestNewSubscription(t *testing.T) {
	s := NewSubscription("id1", "MySub", "https://example.com", true, false)
	if s.ID != "id1" {
		t.Fatalf("expected id1, got %s", s.ID)
	}
	if s.Name() != "MySub" {
		t.Fatalf("expected MySub, got %s", s.Name())
	}
	if !s.Enabled() {
		t.Fatal("expected enabled")
	}
	if s.Ephemeral() {
		t.Fatal("expected not ephemeral")
	}
	if got, want := s.EphemeralNodeEvictDelayNs(), int64(72*time.Hour); got != want {
		t.Fatalf("expected default ephemeral evict delay ns %d, got %d", want, got)
	}
	if s.ManagedNodes() == nil {
		t.Fatal("ManagedNodes should not be nil")
	}
}

func TestSubscription_NameThreadSafe(t *testing.T) {
	s := NewSubscription("id1", "original", "url", true, false)
	s.SetName("updated")
	if s.Name() != "updated" {
		t.Fatalf("expected updated, got %s", s.Name())
	}
}

func TestSubscription_EphemeralNodeEvictDelayThreadSafe(t *testing.T) {
	s := NewSubscription("id1", "sub", "url", true, true)
	s.SetEphemeralNodeEvictDelayNs(int64(10 * time.Minute))
	if got, want := s.EphemeralNodeEvictDelayNs(), int64(10*time.Minute); got != want {
		t.Fatalf("expected %d, got %d", want, got)
	}
}

func TestSubscription_SwapManagedNodes(t *testing.T) {
	s := NewSubscription("id1", "sub", "url", true, false)

	h1 := node.HashFromRawOptions([]byte(`{"type":"ss","server":"1.1.1.1"}`))
	h2 := node.HashFromRawOptions([]byte(`{"type":"ss","server":"2.2.2.2"}`))

	newMap := xsync.NewMap[node.Hash, []string]()
	newMap.Store(h1, []string{"tag-a"})
	newMap.Store(h2, []string{"tag-b"})
	s.SwapManagedNodes(newMap)

	loaded := s.ManagedNodes()
	tags, ok := loaded.Load(h1)
	if !ok || len(tags) != 1 || tags[0] != "tag-a" {
		t.Fatalf("unexpected tag for h1: ok=%v, tags=%v", ok, tags)
	}
}

func TestDiffHashes(t *testing.T) {
	h1 := node.HashFromRawOptions([]byte(`{"type":"ss","server":"1.1.1.1"}`))
	h2 := node.HashFromRawOptions([]byte(`{"type":"ss","server":"2.2.2.2"}`))
	h3 := node.HashFromRawOptions([]byte(`{"type":"ss","server":"3.3.3.3"}`))

	oldMap := xsync.NewMap[node.Hash, []string]()
	oldMap.Store(h1, []string{"a"})
	oldMap.Store(h2, []string{"b"})

	newMap := xsync.NewMap[node.Hash, []string]()
	newMap.Store(h2, []string{"b"})
	newMap.Store(h3, []string{"c"})

	added, kept, removed := DiffHashes(oldMap, newMap)

	if len(added) != 1 || added[0] != h3 {
		t.Fatalf("expected h3 added, got %v", added)
	}
	if len(kept) != 1 || kept[0] != h2 {
		t.Fatalf("expected h2 kept, got %v", kept)
	}
	if len(removed) != 1 || removed[0] != h1 {
		t.Fatalf("expected h1 removed, got %v", removed)
	}
}

func TestDiffHashes_Empty(t *testing.T) {
	empty := xsync.NewMap[node.Hash, []string]()
	h1 := node.HashFromRawOptions([]byte(`{"type":"ss"}`))

	full := xsync.NewMap[node.Hash, []string]()
	full.Store(h1, []string{"t"})

	// All new.
	added, kept, removed := DiffHashes(empty, full)
	if len(added) != 1 || len(kept) != 0 || len(removed) != 0 {
		t.Fatalf("empty→full: added=%d kept=%d removed=%d", len(added), len(kept), len(removed))
	}

	// All removed.
	added, kept, removed = DiffHashes(full, empty)
	if len(added) != 0 || len(kept) != 0 || len(removed) != 1 {
		t.Fatalf("full→empty: added=%d kept=%d removed=%d", len(added), len(kept), len(removed))
	}
}
