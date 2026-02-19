package requestlog

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/resin-proxy/resin/internal/proxy"
)

func TestRepo_InsertListGetPayloads(t *testing.T) {
	repo := NewRepo(t.TempDir(), 1<<20, 5)
	if err := repo.Open(); err != nil {
		t.Fatalf("repo.Open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ts := time.Now().Add(-time.Minute).UnixNano()
	rows := []LogRow{
		{
			ID:                "log-a",
			TsNs:              ts,
			ProxyType:         int(proxy.ProxyTypeForward),
			ClientIP:          "10.0.0.1",
			PlatformID:        "plat-1",
			Account:           "acct-a",
			TargetHost:        "example.com",
			TargetURL:         "https://example.com/a",
			DurationNs:        int64(12 * time.Millisecond),
			NetOK:             true,
			HTTPMethod:        "GET",
			HTTPStatus:        200,
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
			ID:          "log-b",
			TsNs:        ts,
			ProxyType:   int(proxy.ProxyTypeReverse),
			ClientIP:    "10.0.0.2",
			PlatformID:  "plat-2",
			Account:     "acct-b",
			TargetHost:  "example.org",
			TargetURL:   "https://example.org/b",
			DurationNs:  int64(20 * time.Millisecond),
			NetOK:       false,
			HTTPMethod:  "POST",
			HTTPStatus:  502,
			ReqBodyLen:  10,
			RespBodyLen: 11,
		},
	}
	inserted, err := repo.InsertBatch(rows)
	if err != nil {
		t.Fatalf("repo.InsertBatch: %v", err)
	}
	if inserted != 2 {
		t.Fatalf("inserted: got %d, want %d", inserted, 2)
	}

	list, total, err := repo.List(ListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("repo.List: %v", err)
	}
	if total != 2 {
		t.Fatalf("list total: got %d, want %d", total, 2)
	}
	if len(list) != 2 {
		t.Fatalf("list len: got %d, want %d", len(list), 2)
	}
	if list[0].ID != "log-a" || list[1].ID != "log-b" {
		t.Fatalf("list order (ts desc, id asc tie-break): got [%s, %s]", list[0].ID, list[1].ID)
	}

	filtered, total, err := repo.List(ListFilter{PlatformID: "plat-1", Limit: 10})
	if err != nil {
		t.Fatalf("repo.List filtered: %v", err)
	}
	if total != 1 {
		t.Fatalf("filtered total: got %d, want %d", total, 1)
	}
	if len(filtered) != 1 || filtered[0].ID != "log-a" {
		t.Fatalf("filtered list: got %+v", filtered)
	}

	row, err := repo.GetByID("log-a")
	if err != nil {
		t.Fatalf("repo.GetByID: %v", err)
	}
	if row == nil || !row.PayloadPresent {
		t.Fatalf("expected payload-present log row, got %+v", row)
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
		rows, _, err := repo.List(ListFilter{PlatformID: "plat-1", Limit: 10})
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
	if _, err := repo.InsertBatch([]LogRow{{
		ID:        "old-file-new-ts",
		TsNs:      200,
		ProxyType: int(proxy.ProxyTypeForward),
	}}); err != nil {
		t.Fatalf("insert first db row: %v", err)
	}

	// Rotate and insert an older timestamp into the newer DB file.
	if err := repo.rotateDB(); err != nil {
		t.Fatalf("rotateDB: %v", err)
	}
	if _, err := repo.InsertBatch([]LogRow{{
		ID:        "new-file-old-ts",
		TsNs:      100,
		ProxyType: int(proxy.ProxyTypeForward),
	}}); err != nil {
		t.Fatalf("insert second db row: %v", err)
	}

	rows, total, err := repo.List(ListFilter{Limit: 1})
	if err != nil {
		t.Fatalf("repo.List: %v", err)
	}
	if total != 2 {
		t.Fatalf("rows total: got %d, want %d", total, 2)
	}
	if len(rows) != 1 {
		t.Fatalf("rows len: got %d, want 1", len(rows))
	}
	if rows[0].ID != "old-file-new-ts" {
		t.Fatalf("top row id: got %q, want %q", rows[0].ID, "old-file-new-ts")
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
