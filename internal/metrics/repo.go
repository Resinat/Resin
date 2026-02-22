package metrics

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/Resinat/Resin/internal/state"
)

// MetricsDBDDL defines the schema for metrics.db.
const MetricsDBDDL = `
CREATE TABLE IF NOT EXISTS metric_traffic_bucket (
	bucket_start_unix INTEGER PRIMARY KEY,
	ingress_bytes     INTEGER NOT NULL DEFAULT 0,
	egress_bytes      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS metric_request_bucket (
	bucket_start_unix  INTEGER NOT NULL,
	platform_id        TEXT,
	total_requests     INTEGER NOT NULL DEFAULT 0,
	success_requests   INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_metric_request_bucket_dim
	ON metric_request_bucket(bucket_start_unix, platform_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_metric_request_bucket_global
	ON metric_request_bucket(bucket_start_unix)
	WHERE platform_id IS NULL;

CREATE TABLE IF NOT EXISTS metric_access_latency_bucket (
	bucket_start_unix INTEGER NOT NULL,
	platform_id       TEXT,
	buckets_json      TEXT NOT NULL DEFAULT '[]'
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_metric_access_latency_bucket_dim
	ON metric_access_latency_bucket(bucket_start_unix, platform_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_metric_access_latency_bucket_global
	ON metric_access_latency_bucket(bucket_start_unix)
	WHERE platform_id IS NULL;

CREATE TABLE IF NOT EXISTS metric_probe_bucket (
	bucket_start_unix INTEGER PRIMARY KEY,
	total_count       INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS metric_node_pool_bucket (
	bucket_start_unix INTEGER PRIMARY KEY,
	total_nodes       INTEGER NOT NULL DEFAULT 0,
	healthy_nodes     INTEGER NOT NULL DEFAULT 0,
	egress_ip_count   INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS metric_lease_lifetime_bucket (
	bucket_start_unix INTEGER NOT NULL,
	platform_id       TEXT NOT NULL,
	sample_count      INTEGER NOT NULL DEFAULT 0,
	p1_ms             REAL NOT NULL DEFAULT 0,
	p5_ms             REAL NOT NULL DEFAULT 0,
	p50_ms            REAL NOT NULL DEFAULT 0,
	PRIMARY KEY (bucket_start_unix, platform_id)
);
`

// MetricsRepo handles persistence of metric buckets to metrics.db.
type MetricsRepo struct {
	db *sql.DB
}

// NewMetricsRepo opens (or creates) metrics.db at the given path and initializes the schema.
func NewMetricsRepo(path string) (*MetricsRepo, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("metrics repo mkdir: %w", err)
	}
	db, err := state.OpenDB(path)
	if err != nil {
		return nil, fmt.Errorf("metrics repo open: %w", err)
	}
	if err := state.InitDB(db, MetricsDBDDL); err != nil {
		db.Close()
		return nil, fmt.Errorf("metrics repo init: %w", err)
	}
	return &MetricsRepo{db: db}, nil
}

// Close closes the database.
func (r *MetricsRepo) Close() error {
	if r.db != nil {
		return r.db.Close()
	}
	return nil
}

// WriteBucket persists a bucket flush data set in a single transaction.
func (r *MetricsRepo) WriteBucket(data *BucketFlushData) error {
	if data == nil {
		return nil
	}

	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("metrics repo begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Traffic.
	_, err = tx.Exec(`INSERT INTO metric_traffic_bucket (bucket_start_unix, ingress_bytes, egress_bytes)
		VALUES (?,?,?) ON CONFLICT(bucket_start_unix)
		DO UPDATE SET ingress_bytes = excluded.ingress_bytes, egress_bytes = excluded.egress_bytes`,
		data.BucketStartUnix, data.Traffic.IngressBytes, data.Traffic.EgressBytes)
	if err != nil {
		return fmt.Errorf("metrics repo upsert global traffic: %w", err)
	}

	// Requests.
	globalRequests := requestAccum{}
	if rq, ok := data.Requests[""]; ok {
		globalRequests = rq
	}
	_, err = tx.Exec(`INSERT INTO metric_request_bucket (bucket_start_unix, platform_id, total_requests, success_requests)
		VALUES (?,NULL,?,?) ON CONFLICT(bucket_start_unix) WHERE platform_id IS NULL
		DO UPDATE SET total_requests = excluded.total_requests, success_requests = excluded.success_requests`,
		data.BucketStartUnix, globalRequests.Total, globalRequests.Success)
	if err != nil {
		return fmt.Errorf("metrics repo upsert global request: %w", err)
	}

	for pid, rq := range data.Requests {
		if pid == "" {
			continue
		}
		_, err = tx.Exec(`INSERT INTO metric_request_bucket (bucket_start_unix, platform_id, total_requests, success_requests)
			VALUES (?,?,?,?) ON CONFLICT(bucket_start_unix, platform_id)
			DO UPDATE SET total_requests = excluded.total_requests, success_requests = excluded.success_requests`,
			data.BucketStartUnix, pid, rq.Total, rq.Success)
		if err != nil {
			return fmt.Errorf("metrics repo upsert request: %w", err)
		}
	}

	// Probes.
	_, err = tx.Exec(`INSERT INTO metric_probe_bucket (bucket_start_unix, total_count)
		VALUES (?,?) ON CONFLICT(bucket_start_unix)
		DO UPDATE SET total_count = excluded.total_count`,
		data.BucketStartUnix, data.Probes.Total)
	if err != nil {
		return fmt.Errorf("metrics repo upsert probe: %w", err)
	}

	// Lease lifetimes.
	for pid, acc := range data.LeaseLifetimes {
		if len(acc.Samples) == 0 {
			continue
		}
		p1, p5, p50 := computePercentiles(acc.Samples)
		_, err := tx.Exec(`INSERT INTO metric_lease_lifetime_bucket (bucket_start_unix, platform_id, sample_count, p1_ms, p5_ms, p50_ms)
			VALUES (?,?,?,?,?,?) ON CONFLICT(bucket_start_unix, platform_id)
			DO UPDATE SET sample_count = excluded.sample_count, p1_ms = excluded.p1_ms, p5_ms = excluded.p5_ms, p50_ms = excluded.p50_ms`,
			data.BucketStartUnix, pid, len(acc.Samples), p1, p5, p50)
		if err != nil {
			return fmt.Errorf("metrics repo upsert lease lifetime: %w", err)
		}
	}

	return tx.Commit()
}

// WriteNodePoolSnapshot writes a node pool snapshot for a bucket.
func (r *MetricsRepo) WriteNodePoolSnapshot(bucketStartUnix int64, totalNodes, healthyNodes, egressIPCount int) error {
	_, err := r.db.Exec(`INSERT INTO metric_node_pool_bucket (bucket_start_unix, total_nodes, healthy_nodes, egress_ip_count)
		VALUES (?,?,?,?) ON CONFLICT(bucket_start_unix)
		DO UPDATE SET total_nodes = excluded.total_nodes, healthy_nodes = excluded.healthy_nodes, egress_ip_count = excluded.egress_ip_count`,
		bucketStartUnix, totalNodes, healthyNodes, egressIPCount)
	return err
}

// WriteLatencyBucket writes access latency histogram for a bucket.
func (r *MetricsRepo) WriteLatencyBucket(bucketStartUnix int64, platformID string, buckets []int64) error {
	bucketsJSON, _ := json.Marshal(buckets)
	var (
		err error
	)
	if platformID == "" {
		_, err = r.db.Exec(`INSERT INTO metric_access_latency_bucket (bucket_start_unix, platform_id, buckets_json)
			VALUES (?,NULL,?) ON CONFLICT(bucket_start_unix) WHERE platform_id IS NULL
			DO UPDATE SET buckets_json = excluded.buckets_json`,
			bucketStartUnix, string(bucketsJSON))
	} else {
		_, err = r.db.Exec(`INSERT INTO metric_access_latency_bucket (bucket_start_unix, platform_id, buckets_json)
			VALUES (?,?,?) ON CONFLICT(bucket_start_unix, platform_id)
			DO UPDATE SET buckets_json = excluded.buckets_json`,
			bucketStartUnix, platformID, string(bucketsJSON))
	}
	return err
}

// TrafficBucketRow holds a single traffic bucket result.
type TrafficBucketRow struct {
	BucketStartUnix int64 `json:"bucket_start_unix"`
	IngressBytes    int64 `json:"ingress_bytes"`
	EgressBytes     int64 `json:"egress_bytes"`
}

// QueryTraffic returns traffic buckets in a time range.
func (r *MetricsRepo) QueryTraffic(from, to int64) ([]TrafficBucketRow, error) {
	q := `SELECT bucket_start_unix, ingress_bytes, egress_bytes
		FROM metric_traffic_bucket WHERE bucket_start_unix >= ? AND bucket_start_unix <= ?`
	args := []interface{}{from, to}
	q += " ORDER BY bucket_start_unix"
	rows, err := r.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TrafficBucketRow
	for rows.Next() {
		var row TrafficBucketRow
		if err := rows.Scan(&row.BucketStartUnix, &row.IngressBytes, &row.EgressBytes); err != nil {
			continue
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// RequestBucketRow holds a single request bucket result.
type RequestBucketRow struct {
	BucketStartUnix int64  `json:"bucket_start_unix"`
	PlatformID      string `json:"platform_id"`
	TotalRequests   int64  `json:"total_requests"`
	SuccessRequests int64  `json:"success_requests"`
}

// QueryRequests returns request buckets in a time range.
func (r *MetricsRepo) QueryRequests(from, to int64, platformID string) ([]RequestBucketRow, error) {
	q := `SELECT bucket_start_unix, platform_id, total_requests, success_requests
		FROM metric_request_bucket WHERE bucket_start_unix >= ? AND bucket_start_unix <= ?`
	args := []interface{}{from, to}
	if platformID != "" {
		q += " AND platform_id = ?"
		args = append(args, platformID)
	} else {
		// Empty platformID means global scope only.
		q += " AND platform_id IS NULL"
	}
	q += " ORDER BY bucket_start_unix"
	rows, err := r.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []RequestBucketRow
	for rows.Next() {
		var row RequestBucketRow
		var pid sql.NullString
		if err := rows.Scan(&row.BucketStartUnix, &pid, &row.TotalRequests, &row.SuccessRequests); err != nil {
			continue
		}
		if pid.Valid {
			row.PlatformID = pid.String
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// ProbeBucketRow holds a single probe bucket result.
type ProbeBucketRow struct {
	BucketStartUnix int64 `json:"bucket_start_unix"`
	TotalCount      int64 `json:"total_count"`
}

// QueryProbes returns probe buckets in a time range.
func (r *MetricsRepo) QueryProbes(from, to int64) ([]ProbeBucketRow, error) {
	rows, err := r.db.Query(`SELECT bucket_start_unix, total_count
		FROM metric_probe_bucket WHERE bucket_start_unix >= ? AND bucket_start_unix <= ?
		ORDER BY bucket_start_unix`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ProbeBucketRow
	for rows.Next() {
		var row ProbeBucketRow
		if err := rows.Scan(&row.BucketStartUnix, &row.TotalCount); err != nil {
			continue
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// NodePoolBucketRow holds a single node pool bucket result.
type NodePoolBucketRow struct {
	BucketStartUnix int64 `json:"bucket_start_unix"`
	TotalNodes      int   `json:"total_nodes"`
	HealthyNodes    int   `json:"healthy_nodes"`
	EgressIPCount   int   `json:"egress_ip_count"`
}

// QueryNodePool returns node pool buckets in a time range.
func (r *MetricsRepo) QueryNodePool(from, to int64) ([]NodePoolBucketRow, error) {
	rows, err := r.db.Query(`SELECT bucket_start_unix, total_nodes, healthy_nodes, egress_ip_count
		FROM metric_node_pool_bucket WHERE bucket_start_unix >= ? AND bucket_start_unix <= ?
		ORDER BY bucket_start_unix`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []NodePoolBucketRow
	for rows.Next() {
		var row NodePoolBucketRow
		if err := rows.Scan(&row.BucketStartUnix, &row.TotalNodes, &row.HealthyNodes, &row.EgressIPCount); err != nil {
			continue
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// computePercentiles computes P1, P5, P50 from a slice of nanosecond values, returning milliseconds.
func computePercentiles(samples []int64) (p1, p5, p50 float64) {
	if len(samples) == 0 {
		return 0, 0, 0
	}
	sorted := make([]int64, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	nsToMs := func(ns int64) float64 { return float64(ns) / 1e6 }

	percentile := func(p float64) float64 {
		idx := int(p * float64(len(sorted)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		return nsToMs(sorted[idx])
	}

	return percentile(0.01), percentile(0.05), percentile(0.50)
}

// AccessLatencyBucketRow holds a single access latency histogram bucket result.
type AccessLatencyBucketRow struct {
	BucketStartUnix int64  `json:"bucket_start_unix"`
	PlatformID      string `json:"platform_id"`
	BucketsJSON     string `json:"buckets_json"`
}

// QueryAccessLatency returns access latency histogram buckets in a time range.
func (r *MetricsRepo) QueryAccessLatency(from, to int64, platformID string) ([]AccessLatencyBucketRow, error) {
	q := `SELECT bucket_start_unix, platform_id, buckets_json
		FROM metric_access_latency_bucket WHERE bucket_start_unix >= ? AND bucket_start_unix <= ?`
	args := []interface{}{from, to}
	if platformID != "" {
		q += " AND platform_id = ?"
		args = append(args, platformID)
	} else {
		// Empty platformID means global scope only.
		q += " AND platform_id IS NULL"
	}
	q += " ORDER BY bucket_start_unix"
	rows, err := r.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []AccessLatencyBucketRow
	for rows.Next() {
		var row AccessLatencyBucketRow
		var pid sql.NullString
		if err := rows.Scan(&row.BucketStartUnix, &pid, &row.BucketsJSON); err != nil {
			continue
		}
		if pid.Valid {
			row.PlatformID = pid.String
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// LeaseLifetimeBucketRow holds a single lease lifetime histogram bucket result.
type LeaseLifetimeBucketRow struct {
	BucketStartUnix int64   `json:"bucket_start_unix"`
	PlatformID      string  `json:"platform_id"`
	SampleCount     int     `json:"sample_count"`
	P1Ms            float64 `json:"p1_ms"`
	P5Ms            float64 `json:"p5_ms"`
	P50Ms           float64 `json:"p50_ms"`
}

// QueryLeaseLifetime returns lease lifetime buckets in a time range.
func (r *MetricsRepo) QueryLeaseLifetime(from, to int64, platformID string) ([]LeaseLifetimeBucketRow, error) {
	q := `SELECT bucket_start_unix, platform_id, sample_count, p1_ms, p5_ms, p50_ms
		FROM metric_lease_lifetime_bucket WHERE bucket_start_unix >= ? AND bucket_start_unix <= ?`
	args := []interface{}{from, to}
	if platformID != "" {
		q += " AND platform_id = ?"
		args = append(args, platformID)
	}
	q += " ORDER BY bucket_start_unix"
	rows, err := r.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []LeaseLifetimeBucketRow
	for rows.Next() {
		var row LeaseLifetimeBucketRow
		if err := rows.Scan(&row.BucketStartUnix, &row.PlatformID, &row.SampleCount, &row.P1Ms, &row.P5Ms, &row.P50Ms); err != nil {
			continue
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
