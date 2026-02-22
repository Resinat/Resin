package requestlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/proxy"
)

func TestRepo_InsertListGetPayloads(t *testing.T) {
	repo := NewRepo(t.TempDir(), 1<<20, 5)
	if err := repo.Open(); err != nil {
		t.Fatalf("repo.Open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ts := time.Now().Add(-time.Minute).UnixNano()
	rows := []proxy.RequestLogEntry{
		{
			ID:                "log-a",
			StartedAtNs:       ts,
			ProxyType:         proxy.ProxyTypeForward,
			ClientIP:          "10.0.0.1",
			PlatformID:        "plat-1",
			PlatformName:      "Platform One",
			Account:           "acct-a",
			TargetHost:        "example.com",
			TargetURL:         "https://example.com/a",
			DurationNs:        int64(12 * time.Millisecond),
			NetOK:             true,
			HTTPMethod:        "GET",
			HTTPStatus:        200,
			IngressBytes:      1234,
			EgressBytes:       567,
			ReqHeadersLen:     8,
			ReqBodyLen:        7,
			RespHeadersLen:    6,
			RespBodyLen:       5,
			ReqHeaders:        []byte("req-h-a"),
			ReqBody:           []byte("req-b-a"),
			RespHeaders:       []byte("resp-h-a"),
			RespBody:          []byte("resp-b-a"),
			ReqBodyTruncated:  true,
			RespBodyTruncated: true,
		},
		{
			ID:           "log-b",
			StartedAtNs:  ts,
			ProxyType:    proxy.ProxyTypeReverse,
			ClientIP:     "10.0.0.2",
			PlatformID:   "plat-2",
			PlatformName: "Platform Two",
			Account:      "acct-b",
			TargetHost:   "example.org",
			TargetURL:    "https://example.org/b",
			DurationNs:   int64(20 * time.Millisecond),
			NetOK:        false,
			HTTPMethod:   "POST",
			HTTPStatus:   502,
			IngressBytes: 2222,
			EgressBytes:  1111,
			ReqBodyLen:   10,
			RespBodyLen:  11,
		},
	}
	inserted, err := repo.InsertBatch(rows)
	if err != nil {
		t.Fatalf("repo.InsertBatch: %v", err)
	}
	if inserted != 2 {
		t.Fatalf("inserted: got %d, want %d", inserted, 2)
	}

	list, hasMore, nextCursor, err := repo.List(ListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("repo.List: %v", err)
	}
	if hasMore {
		t.Fatalf("hasMore: got true, want false")
	}
	if nextCursor != nil {
		t.Fatalf("nextCursor: got %+v, want nil", nextCursor)
	}
	if len(list) != 2 {
		t.Fatalf("list len: got %d, want %d", len(list), 2)
	}
	if list[0].ID != "log-a" || list[1].ID != "log-b" {
		t.Fatalf("list order (ts desc, id asc tie-break): got [%s, %s]", list[0].ID, list[1].ID)
	}

	filtered, hasMore, nextCursor, err := repo.List(ListFilter{PlatformID: "plat-1", Limit: 10})
	if err != nil {
		t.Fatalf("repo.List filtered: %v", err)
	}
	if hasMore {
		t.Fatalf("filtered hasMore: got true, want false")
	}
	if nextCursor != nil {
		t.Fatalf("filtered nextCursor: got %+v, want nil", nextCursor)
	}
	if len(filtered) != 1 || filtered[0].ID != "log-a" {
		t.Fatalf("filtered list: got %+v", filtered)
	}

	filteredByName, hasMore, nextCursor, err := repo.List(ListFilter{PlatformName: "Platform One", Limit: 10})
	if err != nil {
		t.Fatalf("repo.List filtered by platform_name: %v", err)
	}
	if hasMore {
		t.Fatalf("filtered by platform_name hasMore: got true, want false")
	}
	if nextCursor != nil {
		t.Fatalf("filtered by platform_name nextCursor: got %+v, want nil", nextCursor)
	}
	if len(filteredByName) != 1 || filteredByName[0].ID != "log-a" {
		t.Fatalf("filtered by platform_name list: got %+v", filteredByName)
	}

	fuzzyFiltered, hasMore, nextCursor, err := repo.List(ListFilter{
		PlatformID:   "LAT-1",
		PlatformName: "FORM o",
		Account:      "CT-A",
		TargetHost:   "AMPLE.C",
		Fuzzy:        true,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("repo.List fuzzy filtered: %v", err)
	}
	if hasMore {
		t.Fatalf("fuzzy filtered hasMore: got true, want false")
	}
	if nextCursor != nil {
		t.Fatalf("fuzzy filtered nextCursor: got %+v, want nil", nextCursor)
	}
	if len(fuzzyFiltered) != 1 || fuzzyFiltered[0].ID != "log-a" {
		t.Fatalf("fuzzy filtered list: got %+v", fuzzyFiltered)
	}

	strictPartial, hasMore, nextCursor, err := repo.List(ListFilter{
		PlatformName: "form O",
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("repo.List strict partial: %v", err)
	}
	if hasMore {
		t.Fatalf("strict partial hasMore: got true, want false")
	}
	if nextCursor != nil {
		t.Fatalf("strict partial nextCursor: got %+v, want nil", nextCursor)
	}
	if len(strictPartial) != 0 {
		t.Fatalf("strict partial list len: got %d, want 0", len(strictPartial))
	}

	row, err := repo.GetByID("log-a")
	if err != nil {
		t.Fatalf("repo.GetByID: %v", err)
	}
	if row == nil || !row.PayloadPresent {
		t.Fatalf("expected payload-present log row, got %+v", row)
	}
	if row.IngressBytes != 1234 || row.EgressBytes != 567 {
		t.Fatalf("traffic bytes not persisted: ingress=%d egress=%d", row.IngressBytes, row.EgressBytes)
	}
	if !row.ReqBodyTruncated || !row.RespBodyTruncated {
		t.Fatalf("truncated flags not persisted: %+v", row)
	}

	payload, err := repo.GetPayloads("log-a")
	if err != nil {
		t.Fatalf("repo.GetPayloads: %v", err)
	}
	if payload == nil {
		t.Fatal("expected payload row for log-a")
	}
	if string(payload.ReqHeaders) != "req-h-a" || string(payload.RespBody) != "resp-b-a" {
		t.Fatalf("payload mismatch: %+v", payload)
	}

	none, err := repo.GetPayloads("log-b")
	if err != nil {
		t.Fatalf("repo.GetPayloads(log-b): %v", err)
	}
	if none != nil {
		t.Fatalf("expected nil payload for log-b, got %+v", none)
	}
}

func TestService_FlushesByBatchSize(t *testing.T) {
	repo := NewRepo(t.TempDir(), 1<<20, 5)
	if err := repo.Open(); err != nil {
		t.Fatalf("repo.Open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	svc := NewService(ServiceConfig{
		Repo:          repo,
		QueueSize:     8,
		FlushBatch:    2,
		FlushInterval: time.Hour,
	})
	svc.Start()
	t.Cleanup(svc.Stop)

	baseTs := time.Now().UnixNano()
	svc.EmitRequestLog(proxy.RequestLogEntry{
		StartedAtNs: baseTs,
		ProxyType:   proxy.ProxyTypeForward,
		ClientIP:    "127.0.0.1",
		PlatformID:  "plat-1",
		Account:     "acct-1",
		TargetHost:  "example.com",
		TargetURL:   "https://example.com/1",
		HTTPMethod:  "GET",
		HTTPStatus:  200,
		NetOK:       true,
	})
	svc.EmitRequestLog(proxy.RequestLogEntry{
		StartedAtNs: baseTs + 1,
		ProxyType:   proxy.ProxyTypeReverse,
		ClientIP:    "127.0.0.2",
		PlatformID:  "plat-1",
		Account:     "acct-2",
		TargetHost:  "example.com",
		TargetURL:   "https://example.com/2",
		HTTPMethod:  "POST",
		HTTPStatus:  502,
		NetOK:       false,
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rows, _, _, err := repo.List(ListFilter{PlatformID: "plat-1", Limit: 10})
		if err != nil {
			t.Fatalf("repo.List: %v", err)
		}
		if len(rows) == 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for service flush")
}

func TestService_RepoReadFlushesQueuedLogs(t *testing.T) {
	repo := NewRepo(t.TempDir(), 1<<20, 5)
	if err := repo.Open(); err != nil {
		t.Fatalf("repo.Open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	svc := NewService(ServiceConfig{
		Repo:          repo,
		QueueSize:     8,
		FlushBatch:    1000,      // keep below batch threshold
		FlushInterval: time.Hour, // avoid timer-driven flush in test
	})
	svc.Start()
	t.Cleanup(svc.Stop)

	baseTs := time.Now().UnixNano()
	svc.EmitRequestLog(proxy.RequestLogEntry{
		ID:          "barrier-log-1",
		StartedAtNs: baseTs,
		ProxyType:   proxy.ProxyTypeForward,
		PlatformID:  "plat-1",
		TargetHost:  "example.com",
		TargetURL:   "https://example.com/barrier",
		HTTPMethod:  "GET",
		HTTPStatus:  200,
		NetOK:       true,
	})

	rows, _, _, err := repo.List(ListFilter{PlatformID: "plat-1", Limit: 10})
	if err != nil {
		t.Fatalf("repo.List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows len: got %d, want 1", len(rows))
	}
	if rows[0].ID != "barrier-log-1" {
		t.Fatalf("row id: got %q, want %q", rows[0].ID, "barrier-log-1")
	}
}

func TestRepo_OpenCreatesLogDir(t *testing.T) {
	root := t.TempDir()
	logDir := filepath.Join(root, "logs")
	repo := NewRepo(logDir, 1<<20, 5)
	if err := repo.Open(); err != nil {
		t.Fatalf("repo.Open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
}

func TestRepo_ListAcrossDBsUsesGlobalTsOrdering(t *testing.T) {
	repo := NewRepo(t.TempDir(), 1<<20, 5)
	if err := repo.Open(); err != nil {
		t.Fatalf("repo.Open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	// Insert a newer timestamp into the first DB file.
	if _, err := repo.InsertBatch([]proxy.RequestLogEntry{{
		ID:          "old-file-new-ts",
		StartedAtNs: 200,
		ProxyType:   proxy.ProxyTypeForward,
	}}); err != nil {
		t.Fatalf("insert first db row: %v", err)
	}

	// Rotate and insert an older timestamp into the newer DB file.
	if err := repo.rotateDB(); err != nil {
		t.Fatalf("rotateDB: %v", err)
	}
	if _, err := repo.InsertBatch([]proxy.RequestLogEntry{{
		ID:          "new-file-old-ts",
		StartedAtNs: 100,
		ProxyType:   proxy.ProxyTypeForward,
	}}); err != nil {
		t.Fatalf("insert second db row: %v", err)
	}

	rows, hasMore, nextCursor, err := repo.List(ListFilter{Limit: 1})
	if err != nil {
		t.Fatalf("repo.List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows len: got %d, want 1", len(rows))
	}
	if !hasMore {
		t.Fatalf("hasMore: got false, want true")
	}
	if nextCursor == nil {
		t.Fatal("nextCursor: got nil, want non-nil")
	}
	if rows[0].ID != "old-file-new-ts" {
		t.Fatalf("top row id: got %q, want %q", rows[0].ID, "old-file-new-ts")
	}
}

func TestRepo_ListCursorPagination(t *testing.T) {
	repo := NewRepo(t.TempDir(), 1<<20, 5)
	if err := repo.Open(); err != nil {
		t.Fatalf("repo.Open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	// Same ts to verify id ASC tie-break within ts.
	rows := []proxy.RequestLogEntry{
		{ID: "a", StartedAtNs: 300, ProxyType: proxy.ProxyTypeForward},
		{ID: "b", StartedAtNs: 300, ProxyType: proxy.ProxyTypeForward},
		{ID: "c", StartedAtNs: 200, ProxyType: proxy.ProxyTypeForward},
	}
	if _, err := repo.InsertBatch(rows); err != nil {
		t.Fatalf("repo.InsertBatch: %v", err)
	}

	page1, hasMore1, next1, err := repo.List(ListFilter{Limit: 2})
	if err != nil {
		t.Fatalf("repo.List page1: %v", err)
	}
	if len(page1) != 2 || page1[0].ID != "a" || page1[1].ID != "b" {
		t.Fatalf("page1 rows: got %+v", page1)
	}
	if !hasMore1 || next1 == nil {
		t.Fatalf("page1 pagination: hasMore=%v next=%+v", hasMore1, next1)
	}

	page2, hasMore2, next2, err := repo.List(ListFilter{
		Limit:  2,
		Cursor: next1,
	})
	if err != nil {
		t.Fatalf("repo.List page2: %v", err)
	}
	if len(page2) != 1 || page2[0].ID != "c" {
		t.Fatalf("page2 rows: got %+v", page2)
	}
	if hasMore2 {
		t.Fatalf("page2 hasMore: got true, want false")
	}
	if next2 != nil {
		t.Fatalf("page2 next: got %+v, want nil", next2)
	}
}

func TestRepo_MaybeRotateCountsWalAndShmSize(t *testing.T) {
	repo := NewRepo(t.TempDir(), 1024, 5)
	if err := repo.Open(); err != nil {
		t.Fatalf("repo.Open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	// Make base DB tiny but WAL large enough to cross threshold.
	if err := os.WriteFile(repo.activePath+"-wal", make([]byte, 1500), 0o644); err != nil {
		t.Fatalf("write wal: %v", err)
	}

	before := repo.activePath
	if err := repo.maybeRotate(); err != nil {
		t.Fatalf("repo.maybeRotate: %v", err)
	}
	if repo.activePath == before {
		t.Fatal("expected rotation when wal size exceeds threshold")
	}
}

func TestRepo_InsertBatchRecoversAfterActiveDBLost(t *testing.T) {
	repo := NewRepo(t.TempDir(), 1<<20, 5)
	if err := repo.Open(); err != nil {
		t.Fatalf("repo.Open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	if repo.activeDB == nil || repo.activePath == "" {
		t.Fatalf("repo should have active db after open")
	}

	// Simulate a failed rotation aftermath:
	// old DB handle is gone, but activePath still points to the old DB file.
	if err := repo.activeDB.Close(); err != nil {
		t.Fatalf("close active db: %v", err)
	}
	repo.activeDB = nil

	inserted, err := repo.InsertBatch([]proxy.RequestLogEntry{{
		ID:          "recovered-insert",
		StartedAtNs: time.Now().UnixNano(),
		ProxyType:   proxy.ProxyTypeForward,
	}})
	if err != nil {
		t.Fatalf("repo.InsertBatch recover path: %v", err)
	}
	if inserted != 1 {
		t.Fatalf("inserted: got %d, want 1", inserted)
	}

	row, err := repo.GetByID("recovered-insert")
	if err != nil {
		t.Fatalf("repo.GetByID: %v", err)
	}
	if row == nil {
		t.Fatal("expected inserted row after recovery")
	}
}

func TestRepo_InsertBatchWithoutOpenReturnsNoActiveDB(t *testing.T) {
	repo := NewRepo(t.TempDir(), 1<<20, 5)
	_, err := repo.InsertBatch([]proxy.RequestLogEntry{{
		ID:          "without-open",
		StartedAtNs: time.Now().UnixNano(),
		ProxyType:   proxy.ProxyTypeForward,
	}})
	if err == nil {
		t.Fatal("expected error when InsertBatch is called before Open")
	}
	if !strings.Contains(err.Error(), "no active db") {
		t.Fatalf("unexpected error: %v", err)
	}
}
