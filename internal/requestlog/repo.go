package requestlog

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/resin-proxy/resin/internal/proxy"
	"github.com/resin-proxy/resin/internal/state"
)

const logSummarySelectColumns = "id, ts_ns, proxy_type, client_ip, platform_id, platform_name, account, target_host, target_url, node_hash, node_tag, egress_ip, duration_ns, net_ok, http_method, http_status, ingress_bytes, egress_bytes, payload_present, req_headers_len, req_body_len, resp_headers_len, resp_body_len, req_headers_truncated, req_body_truncated, resp_headers_truncated, resp_body_truncated"

// Repo manages rolling SQLite databases for request logs.
// Each DB is named request_logs-<unix_ms>.db and lives in logDir.
type Repo struct {
	logDir      string
	maxBytes    int64
	retainCount int

	// Active DB handle and path.
	activeDB   *sql.DB
	activePath string

	// readBarrier runs before read queries to improve freshness.
	readBarrierMu sync.RWMutex
	readBarrier   func()
}

// NewRepo creates a Repo that manages rolling request log databases.
// maxBytes controls when the active DB is rotated; retainCount sets
// how many historical DB files are kept.
func NewRepo(logDir string, maxBytes int64, retainCount int) *Repo {
	if maxBytes <= 0 {
		maxBytes = 512 * 1024 * 1024 // 512 MB default
	}
	if retainCount <= 0 {
		retainCount = 5
	}
	return &Repo{
		logDir:      logDir,
		maxBytes:    maxBytes,
		retainCount: retainCount,
	}
}

// Open opens (or creates) the active request log database.
// If a previous DB exists in the directory it is reused as active;
// a new one is created only when no existing DB is found.
func (r *Repo) Open() error {
	if err := os.MkdirAll(r.logDir, 0o755); err != nil {
		return fmt.Errorf("requestlog repo mkdir %s: %w", r.logDir, err)
	}

	files, err := r.listDBFiles()
	if err != nil {
		return fmt.Errorf("requestlog repo open: %w", err)
	}

	if len(files) > 0 {
		// Re-use latest as active.
		latest := files[len(files)-1]
		if err := r.openDB(latest); err != nil {
			return err
		}
		// DESIGN.md §576: prune old files on startup.
		return r.cleanup()
	}
	return r.rotateDB()
}

// Close closes the active DB.
func (r *Repo) Close() error {
	if r.activeDB != nil {
		err := r.activeDB.Close()
		r.activeDB = nil
		r.activePath = ""
		return err
	}
	return nil
}

// InsertBatch inserts a batch of log entries + optional payloads in a single
// transaction. Returns the number of rows successfully inserted.
func (r *Repo) InsertBatch(entries []proxy.RequestLogEntry) (int, error) {
	if r.activeDB == nil {
		if err := r.recoverActiveDB(); err != nil {
			return 0, err
		}
	}

	// Check if rotation is needed before insert.
	if err := r.maybeRotate(); err != nil {
		return 0, fmt.Errorf("requestlog repo rotate: %w", err)
	}

	tx, err := r.activeDB.Begin()
	if err != nil {
		return 0, fmt.Errorf("requestlog repo begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	insertLog, err := tx.Prepare(`INSERT OR IGNORE INTO request_logs (
		id, ts_ns, proxy_type, client_ip,
		platform_id, platform_name, account,
		target_host, target_url, node_hash, node_tag, egress_ip,
		duration_ns, net_ok, http_method, http_status,
		ingress_bytes, egress_bytes,
		payload_present,
		req_headers_len, req_body_len, resp_headers_len, resp_body_len,
		req_headers_truncated, req_body_truncated, resp_headers_truncated, resp_body_truncated
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, fmt.Errorf("requestlog repo prepare log: %w", err)
	}
	defer insertLog.Close()

	insertPayload, err := tx.Prepare(`INSERT OR IGNORE INTO request_log_payloads (
		log_id, req_headers, req_body, resp_headers, resp_body
	) VALUES (?,?,?,?,?)`)
	if err != nil {
		return 0, fmt.Errorf("requestlog repo prepare payload: %w", err)
	}
	defer insertPayload.Close()

	inserted := 0
	for i := range entries {
		e := &entries[i]
		id := e.ID
		if id == "" {
			id = uuid.NewString()
		}
		netOK := 0
		if e.NetOK {
			netOK = 1
		}
		hasPayload := 0
		if e.ReqHeaders != nil || e.ReqBody != nil || e.RespHeaders != nil || e.RespBody != nil {
			hasPayload = 1
		}

		_, err := insertLog.Exec(
			id, e.StartedAtNs, int(e.ProxyType), e.ClientIP,
			e.PlatformID, e.PlatformName, e.Account,
			e.TargetHost, e.TargetURL, e.NodeHash, e.NodeTag, e.EgressIP,
			e.DurationNs, netOK, e.HTTPMethod, e.HTTPStatus,
			e.IngressBytes, e.EgressBytes,
			hasPayload,
			e.ReqHeadersLen, e.ReqBodyLen, e.RespHeadersLen, e.RespBodyLen,
			boolToInt(e.ReqHeadersTruncated), boolToInt(e.ReqBodyTruncated),
			boolToInt(e.RespHeadersTruncated), boolToInt(e.RespBodyTruncated),
		)
		if err != nil {
			log.Printf("[requestlog] warning: skip log row id=%q insert failed: %v", id, err)
			continue // skip individual row errors
		}

		if hasPayload == 1 {
			if _, err := insertPayload.Exec(id, e.ReqHeaders, e.ReqBody, e.RespHeaders, e.RespBody); err != nil {
				log.Printf("[requestlog] warning: payload insert failed for id=%q: %v", id, err)
			}
		}
		inserted++
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("requestlog repo commit: %w", err)
	}
	return inserted, nil
}

// recoverActiveDB attempts to recover from a missing active DB handle.
// This can happen if a previous rotation closed the old DB but failed
// to open a new one. We keep the documented rotation semantics (close then create)
// and only recover on subsequent writes.
func (r *Repo) recoverActiveDB() error {
	if r.activeDB != nil {
		return nil
	}
	if r.activePath == "" {
		return fmt.Errorf("requestlog repo: no active db")
	}
	if err := r.rotateDB(); err != nil {
		return fmt.Errorf("requestlog repo recover active db: %w", err)
	}
	return nil
}

// LogSummary is the result of listing logs (without payload blobs).
type LogSummary struct {
	ID           string `json:"id"`
	TsNs         int64  `json:"ts_ns"`
	ProxyType    int    `json:"proxy_type"`
	ClientIP     string `json:"client_ip"`
	PlatformID   string `json:"platform_id"`
	PlatformName string `json:"platform_name"`
	Account      string `json:"account"`
	TargetHost   string `json:"target_host"`
	TargetURL    string `json:"target_url"`
	NodeHash     string `json:"node_hash"`
	NodeTag      string `json:"node_tag"`
	EgressIP     string `json:"egress_ip"`
	DurationNs   int64  `json:"duration_ns"`
	NetOK        bool   `json:"net_ok"`
	HTTPMethod   string `json:"http_method"`
	HTTPStatus   int    `json:"http_status"`
	IngressBytes int64  `json:"ingress_bytes"`
	EgressBytes  int64  `json:"egress_bytes"`

	PayloadPresent       bool `json:"payload_present"`
	ReqHeadersLen        int  `json:"req_headers_len"`
	ReqBodyLen           int  `json:"req_body_len"`
	RespHeadersLen       int  `json:"resp_headers_len"`
	RespBodyLen          int  `json:"resp_body_len"`
	ReqHeadersTruncated  bool `json:"req_headers_truncated"`
	ReqBodyTruncated     bool `json:"req_body_truncated"`
	RespHeadersTruncated bool `json:"resp_headers_truncated"`
	RespBodyTruncated    bool `json:"resp_body_truncated"`
}

// PayloadRow holds the payload data for a single log entry.
type PayloadRow struct {
	LogID       string `json:"log_id"`
	ReqHeaders  []byte `json:"req_headers"`
	ReqBody     []byte `json:"req_body"`
	RespHeaders []byte `json:"resp_headers"`
	RespBody    []byte `json:"resp_body"`
}

// ListFilter specifies query filters for listing logs.
type ListFilter struct {
	ProxyType    *int
	PlatformID   string
	PlatformName string
	Account      string
	TargetHost   string
	Fuzzy        bool // Enables substring matching on platform_id/platform_name/account/target_host.
	EgressIP     string
	NetOK        *bool // true/false filter
	HTTPStatus   *int  // exact match
	Before       int64 // ts_ns < Before (0 means no upper bound)
	After        int64 // ts_ns > After (0 means no lower bound)
	Limit        int
	Cursor       *ListCursor
}

// ListCursor encodes a request-log pagination position.
// Ordering is ts_ns DESC then id ASC.
type ListCursor struct {
	TsNs int64
	ID   string
}

// List queries all retained DBs and returns a page of matching log summaries
// ordered by ts_ns DESC, same ts_ns by id ASC.
func (r *Repo) List(f ListFilter) ([]LogSummary, bool, *ListCursor, error) {
	r.runReadBarrier()

	files, err := r.listDBFiles()
	if err != nil {
		return nil, false, nil, err
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	// Fetch one extra row across retained DBs to derive has_more.
	fetchLimit := limit + 1
	var results []LogSummary
	// Iterate every retained DB, then globally merge-sort.
	// We must not early-stop by file order because request ts_ns can be out-of-order
	// relative to DB filename time (e.g. long-lived requests flushed later).
	for i := len(files) - 1; i >= 0; i-- {
		db, err := r.openReadOnly(files[i])
		if err != nil {
			log.Printf("[requestlog] warning: list open db failed path=%q: %v", files[i], err)
			continue
		}
		rows, err := r.queryLogs(db, f, fetchLimit)
		if closeErr := db.Close(); closeErr != nil {
			log.Printf("[requestlog] warning: list close db failed path=%q: %v", files[i], closeErr)
		}
		if err != nil {
			log.Printf("[requestlog] warning: list query failed path=%q: %v", files[i], err)
			continue
		}
		results = append(results, rows...)
	}

	// Global merge sort: DESIGN.md §577 requires ts_ns DESC, same ts_ns by id ASC.
	sort.Slice(results, func(i, j int) bool {
		if results[i].TsNs != results[j].TsNs {
			return results[i].TsNs > results[j].TsNs
		}
		return results[i].ID < results[j].ID
	})
	if len(results) == 0 {
		return []LogSummary{}, false, nil, nil
	}

	hasMore := len(results) > limit
	if hasMore {
		results = results[:limit]
	}

	var nextCursor *ListCursor
	if hasMore && len(results) > 0 {
		last := results[len(results)-1]
		nextCursor = &ListCursor{TsNs: last.TsNs, ID: last.ID}
	}
	return results, hasMore, nextCursor, nil
}

// GetByID looks up a single log entry across all retained DBs.
func (r *Repo) GetByID(id string) (*LogSummary, error) {
	r.runReadBarrier()

	files, err := r.listDBFiles()
	if err != nil {
		return nil, err
	}

	var result *LogSummary
	r.queryAcrossRetainedDBs(files, "get_by_id", "id", id, func(db *sql.DB) (bool, error) {
		row, err := r.queryLogByID(db, id)
		if err != nil {
			return false, err
		}
		if row != nil {
			result = row
			return true, nil
		}
		return false, nil
	})
	return result, nil
}

// GetPayloads retrieves payload data for a given log ID across all retained DBs.
func (r *Repo) GetPayloads(logID string) (*PayloadRow, error) {
	r.runReadBarrier()

	files, err := r.listDBFiles()
	if err != nil {
		return nil, err
	}

	var result *PayloadRow
	r.queryAcrossRetainedDBs(files, "get_payloads", "log_id", logID, func(db *sql.DB) (bool, error) {
		row, err := r.queryPayload(db, logID)
		if err != nil {
			return false, err
		}
		if row != nil {
			result = row
			return true, nil
		}
		return false, nil
	})
	return result, nil
}

func (r *Repo) queryAcrossRetainedDBs(
	files []string,
	op string,
	keyName string,
	keyValue string,
	query func(*sql.DB) (bool, error),
) {
	for i := len(files) - 1; i >= 0; i-- {
		path := files[i]
		db, err := r.openReadOnly(path)
		if err != nil {
			log.Printf("[requestlog] warning: %s open db failed path=%q %s=%q: %v", op, path, keyName, keyValue, err)
			continue
		}
		row, err := query(db)
		if closeErr := db.Close(); closeErr != nil {
			log.Printf("[requestlog] warning: %s close db failed path=%q %s=%q: %v", op, path, keyName, keyValue, closeErr)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			log.Printf("[requestlog] warning: %s query failed path=%q %s=%q: %v", op, path, keyName, keyValue, err)
		}
		if err == nil && row {
			return
		}
	}
}

func (r *Repo) setReadBarrier(fn func()) {
	r.readBarrierMu.Lock()
	r.readBarrier = fn
	r.readBarrierMu.Unlock()
}

func (r *Repo) runReadBarrier() {
	r.readBarrierMu.RLock()
	barrier := r.readBarrier
	r.readBarrierMu.RUnlock()
	if barrier != nil {
		barrier()
	}
}

// --- internal helpers ---

func (r *Repo) openDB(path string) error {
	db, err := state.OpenDB(path)
	if err != nil {
		return err
	}
	if err := state.InitDB(db, CreateDDL); err != nil {
		db.Close()
		return err
	}
	r.activeDB = db
	r.activePath = path
	return nil
}

func (r *Repo) rotateDB() error {
	if r.activeDB != nil {
		r.activeDB.Close()
		r.activeDB = nil
	}
	name := fmt.Sprintf("request_logs-%d.db", time.Now().UnixMilli())
	path := filepath.Join(r.logDir, name)
	if err := r.openDB(path); err != nil {
		return fmt.Errorf("requestlog rotate: %w", err)
	}
	return r.cleanup()
}

func (r *Repo) maybeRotate() error {
	if r.activePath == "" {
		return r.rotateDB()
	}
	totalSize, err := sqliteFilesSize(r.activePath)
	if err != nil {
		log.Printf("[requestlog] warning: stat active db failed path=%q: %v", r.activePath, err)
		return nil // can't stat; skip rotation check
	}
	if totalSize >= r.maxBytes {
		return r.rotateDB()
	}
	return nil
}

func (r *Repo) cleanup() error {
	files, err := r.listDBFiles()
	if err != nil {
		return err
	}
	// Keep retainCount most recent files (the active one is always latest).
	if len(files) <= r.retainCount {
		return nil
	}
	toRemove := files[:len(files)-r.retainCount]
	for _, f := range toRemove {
		os.Remove(f)
		// Also clean up WAL/SHM files.
		os.Remove(f + "-wal")
		os.Remove(f + "-shm")
	}
	return nil
}

func (r *Repo) listDBFiles() ([]string, error) {
	entries, err := os.ReadDir(r.logDir)
	if err != nil {
		return nil, fmt.Errorf("requestlog list dir %s: %w", r.logDir, err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, "request_logs-") && strings.HasSuffix(name, ".db") {
			files = append(files, filepath.Join(r.logDir, name))
		}
	}
	sort.Strings(files) // lexicographic sort == chronological for our naming
	return files, nil
}

func (r *Repo) openReadOnly(path string) (*sql.DB, error) {
	dsn := path + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func (r *Repo) queryLogs(db *sql.DB, f ListFilter, limit int) ([]LogSummary, error) {
	var where []string
	var args []interface{}

	if f.ProxyType != nil {
		where = append(where, "proxy_type = ?")
		args = append(args, *f.ProxyType)
	}
	if f.PlatformID != "" {
		if f.Fuzzy {
			where = append(where, "instr(platform_id, ?) > 0")
		} else {
			where = append(where, "platform_id = ?")
		}
		args = append(args, f.PlatformID)
	}
	if f.PlatformName != "" {
		if f.Fuzzy {
			where = append(where, "instr(platform_name, ?) > 0")
		} else {
			where = append(where, "platform_name = ?")
		}
		args = append(args, f.PlatformName)
	}
	if f.Account != "" {
		if f.Fuzzy {
			where = append(where, "instr(account, ?) > 0")
		} else {
			where = append(where, "account = ?")
		}
		args = append(args, f.Account)
	}
	if f.TargetHost != "" {
		if f.Fuzzy {
			where = append(where, "instr(target_host, ?) > 0")
		} else {
			where = append(where, "target_host = ?")
		}
		args = append(args, f.TargetHost)
	}
	if f.EgressIP != "" {
		where = append(where, "egress_ip = ?")
		args = append(args, f.EgressIP)
	}
	if f.NetOK != nil {
		where = append(where, "net_ok = ?")
		args = append(args, boolToInt(*f.NetOK))
	}
	if f.HTTPStatus != nil {
		where = append(where, "http_status = ?")
		args = append(args, *f.HTTPStatus)
	}
	if f.Before > 0 {
		where = append(where, "ts_ns < ?")
		args = append(args, f.Before)
	}
	if f.After > 0 {
		where = append(where, "ts_ns > ?")
		args = append(args, f.After)
	}
	if f.Cursor != nil {
		// Pagination condition for ORDER BY ts_ns DESC, id ASC:
		// next rows are strictly "after" the cursor in that ordering.
		where = append(where, "(ts_ns < ? OR (ts_ns = ? AND id > ?))")
		args = append(args, f.Cursor.TsNs, f.Cursor.TsNs, f.Cursor.ID)
	}

	q := "SELECT " + logSummarySelectColumns + " FROM request_logs"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY ts_ns DESC, id ASC LIMIT ?"
	args = append(args, limit)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanLogSummaries(rows)
}

func (r *Repo) queryLogByID(db *sql.DB, id string) (*LogSummary, error) {
	row := db.QueryRow("SELECT "+logSummarySelectColumns+" FROM request_logs WHERE id = ?", id)
	s, err := scanLogSummary(row)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *Repo) queryPayload(db *sql.DB, logID string) (*PayloadRow, error) {
	row := db.QueryRow("SELECT log_id, req_headers, req_body, resp_headers, resp_body FROM request_log_payloads WHERE log_id = ?", logID)
	var p PayloadRow
	err := row.Scan(&p.LogID, &p.ReqHeaders, &p.ReqBody, &p.RespHeaders, &p.RespBody)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func scanLogSummaries(rows *sql.Rows) ([]LogSummary, error) {
	var results []LogSummary
	for rows.Next() {
		s, err := scanLogSummary(rows)
		if err != nil {
			log.Printf("[requestlog] warning: skip malformed log row during scan: %v", err)
			continue
		}
		results = append(results, s)
	}
	return results, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanLogSummary(s rowScanner) (LogSummary, error) {
	var row LogSummary
	var netOK, payloadPresent, rht, rbt, rsht, rsbt int
	err := s.Scan(
		&row.ID, &row.TsNs, &row.ProxyType, &row.ClientIP,
		&row.PlatformID, &row.PlatformName, &row.Account,
		&row.TargetHost, &row.TargetURL, &row.NodeHash, &row.NodeTag, &row.EgressIP,
		&row.DurationNs, &netOK, &row.HTTPMethod, &row.HTTPStatus, &row.IngressBytes, &row.EgressBytes,
		&payloadPresent,
		&row.ReqHeadersLen, &row.ReqBodyLen, &row.RespHeadersLen, &row.RespBodyLen,
		&rht, &rbt, &rsht, &rsbt,
	)
	if err != nil {
		return LogSummary{}, err
	}
	row.NetOK = netOK != 0
	row.PayloadPresent = payloadPresent != 0
	row.ReqHeadersTruncated = rht != 0
	row.ReqBodyTruncated = rbt != 0
	row.RespHeadersTruncated = rsht != 0
	row.RespBodyTruncated = rsbt != 0
	return row, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// sqliteFilesSize returns the total size of a SQLite database set:
// base db file + optional -wal and -shm sidecar files.
func sqliteFilesSize(basePath string) (int64, error) {
	paths := []string{basePath, basePath + "-wal", basePath + "-shm"}
	var total int64
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, err
		}
		total += info.Size()
	}
	return total, nil
}
