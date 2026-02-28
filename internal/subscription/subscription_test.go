package subscription

import (
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
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
	if got := s.SourceType(); got != SourceTypeRemote {
		t.Fatalf("expected default source type %q, got %q", SourceTypeRemote, got)
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

	newMap := NewManagedNodes()
	newMap.StoreNode(h1, ManagedNode{Tags: []string{"tag-a"}})
	newMap.StoreNode(h2, ManagedNode{Tags: []string{"tag-b"}})
	s.SwapManagedNodes(newMap)

	loaded := s.ManagedNodes()
	managed, ok := loaded.LoadNode(h1)
	tags := managed.Tags
	if !ok || len(tags) != 1 || tags[0] != "tag-a" {
		t.Fatalf("unexpected tag for h1: ok=%v, tags=%v", ok, tags)
	}
}

func TestManagedNodes_LoadNodeStoreNode(t *testing.T) {
	m := NewManagedNodes()
	h := node.HashFromRawOptions([]byte(`{"type":"ss","server":"1.1.1.1"}`))

	m.StoreNode(h, ManagedNode{
		Tags:    []string{"tag-a", "tag-b"},
		Evicted: true,
	})

	got, ok := m.LoadNode(h)
	if !ok {
		t.Fatal("expected hash to exist")
	}
	if !got.Evicted {
		t.Fatal("expected Evicted=true")
	}
	if len(got.Tags) != 2 || got.Tags[0] != "tag-a" || got.Tags[1] != "tag-b" {
		t.Fatalf("unexpected tags: %+v", got.Tags)
	}
}

func TestManagedNodes_StoreNodeCopiesInputTags(t *testing.T) {
	m := NewManagedNodes()
	h := node.HashFromRawOptions([]byte(`{"type":"ss","server":"8.8.8.8"}`))
	input := []string{"tag-a", "tag-b"}

	m.StoreNode(h, ManagedNode{Tags: input})
	input[0] = "mutated"
	input[1] = "changed"

	got, ok := m.LoadNode(h)
	if !ok {
		t.Fatal("expected hash to exist")
	}
	if len(got.Tags) != 2 || got.Tags[0] != "tag-a" || got.Tags[1] != "tag-b" {
		t.Fatalf("stored tags should not be affected by caller mutation: %+v", got.Tags)
	}
}

func TestSubscription_SourceTypeAndContent(t *testing.T) {
	s := NewSubscription("id1", "sub", "url", true, false)
	v0 := s.ConfigVersion()

	s.SetSourceType(SourceTypeLocal)
	s.SetContent("vmess://example")
	if got := s.SourceType(); got != SourceTypeLocal {
		t.Fatalf("expected source type %q, got %q", SourceTypeLocal, got)
	}
	if got := s.Content(); got != "vmess://example" {
		t.Fatalf("unexpected content: %q", got)
	}
	if s.ConfigVersion() <= v0 {
		t.Fatalf("expected config version to increase: old=%d new=%d", v0, s.ConfigVersion())
	}
}

func TestDiffHashes(t *testing.T) {
	h1 := node.HashFromRawOptions([]byte(`{"type":"ss","server":"1.1.1.1"}`))
	h2 := node.HashFromRawOptions([]byte(`{"type":"ss","server":"2.2.2.2"}`))
	h3 := node.HashFromRawOptions([]byte(`{"type":"ss","server":"3.3.3.3"}`))

	oldMap := NewManagedNodes()
	oldMap.StoreNode(h1, ManagedNode{Tags: []string{"a"}})
	oldMap.StoreNode(h2, ManagedNode{Tags: []string{"b"}})

	newMap := NewManagedNodes()
	newMap.StoreNode(h2, ManagedNode{Tags: []string{"b"}})
	newMap.StoreNode(h3, ManagedNode{Tags: []string{"c"}})

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
	empty := NewManagedNodes()
	h1 := node.HashFromRawOptions([]byte(`{"type":"ss"}`))

	full := NewManagedNodes()
	full.StoreNode(h1, ManagedNode{Tags: []string{"t"}})

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
