// Package state implements the persistence layer: SQLite repos, StateEngine,
// dirty-set flush, consistency repair, and bootstrap.
package state

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// CreateStateDDL is the DDL for state.db (strong persistence).
const CreateStateDDL = `
CREATE TABLE IF NOT EXISTS system_config (
	id              INTEGER PRIMARY KEY CHECK (id = 1),
	config_json     TEXT    NOT NULL,
	version         INTEGER NOT NULL,
	updated_at_ns   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS platforms (
	id                       TEXT PRIMARY KEY,
	name                     TEXT NOT NULL UNIQUE,
	sticky_ttl_ns            INTEGER NOT NULL,
	regex_filters_json       TEXT NOT NULL DEFAULT '[]',
	region_filters_json      TEXT NOT NULL DEFAULT '[]',
	reverse_proxy_miss_action TEXT NOT NULL DEFAULT 'RANDOM',
	reverse_proxy_empty_account_behavior TEXT NOT NULL DEFAULT 'RANDOM',
	reverse_proxy_fixed_account_header   TEXT NOT NULL DEFAULT '',
	allocation_policy        TEXT NOT NULL DEFAULT 'BALANCED',
	updated_at_ns            INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS subscriptions (
	id                TEXT PRIMARY KEY,
	name              TEXT NOT NULL,
	source_type       TEXT NOT NULL DEFAULT 'remote',
	url               TEXT NOT NULL,
	content           TEXT NOT NULL DEFAULT '',
	update_interval_ns INTEGER NOT NULL,
	enabled           INTEGER NOT NULL DEFAULT 1,
	ephemeral         INTEGER NOT NULL DEFAULT 0,
	ephemeral_node_evict_delay_ns INTEGER NOT NULL,
	created_at_ns     INTEGER NOT NULL,
	updated_at_ns     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS account_header_rules (
	url_prefix    TEXT PRIMARY KEY,
	headers_json  TEXT NOT NULL,
	updated_at_ns INTEGER NOT NULL
);
`

// CreateCacheDDL is the DDL for cache.db (weak persistence).
const CreateCacheDDL = `
CREATE TABLE IF NOT EXISTS nodes_static (
	hash             TEXT PRIMARY KEY,
	raw_options_json TEXT NOT NULL,
	created_at_ns    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS nodes_dynamic (
	hash               TEXT PRIMARY KEY,
	failure_count      INTEGER NOT NULL DEFAULT 0,
	circuit_open_since INTEGER NOT NULL DEFAULT 0,
	egress_ip          TEXT NOT NULL DEFAULT '',
	egress_region      TEXT NOT NULL DEFAULT '',
	egress_updated_at_ns INTEGER NOT NULL DEFAULT 0,
	last_latency_probe_attempt_ns INTEGER NOT NULL DEFAULT 0,
	last_authority_latency_probe_attempt_ns INTEGER NOT NULL DEFAULT 0,
	last_egress_update_attempt_ns INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS node_latency (
	node_hash       TEXT NOT NULL,
	domain          TEXT NOT NULL,
	ewma_ns         INTEGER NOT NULL,
	last_updated_ns INTEGER NOT NULL,
	PRIMARY KEY (node_hash, domain)
);

CREATE TABLE IF NOT EXISTS leases (
	platform_id     TEXT NOT NULL,
	account         TEXT NOT NULL,
	node_hash       TEXT NOT NULL,
	egress_ip       TEXT NOT NULL DEFAULT '',
	created_at_ns   INTEGER NOT NULL DEFAULT 0,
	expiry_ns       INTEGER NOT NULL,
	last_accessed_ns INTEGER NOT NULL,
	PRIMARY KEY (platform_id, account)
);

CREATE TABLE IF NOT EXISTS subscription_nodes (
	subscription_id TEXT NOT NULL,
	node_hash       TEXT NOT NULL,
	tags_json       TEXT NOT NULL DEFAULT '[]',
	PRIMARY KEY (subscription_id, node_hash)
);
`

// OpenDB opens (or creates) a SQLite database at path with recommended pragmas:
// WAL journal mode, synchronous=NORMAL, foreign_keys=ON, busy_timeout=5000.
func OpenDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db %s: %w", path, err)
	}

	// Single-writer: only one connection needed.
	db.SetMaxOpenConns(1)

	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("exec %q on %s: %w", p, path, err)
		}
	}

	return db, nil
}

// InitDB executes DDL statements on the given database.
func InitDB(db *sql.DB, ddl string) error {
	_, err := db.Exec(ddl)
	return err
}

// EnsureStateSchemaMigrations applies lightweight additive migrations for
// state.db created by older versions.
func EnsureStateSchemaMigrations(db *sql.DB) error {
	if db == nil {
		return nil
	}
	if err := ensureTableColumn(
		db,
		"platforms",
		"reverse_proxy_empty_account_behavior",
		`reverse_proxy_empty_account_behavior TEXT NOT NULL DEFAULT 'RANDOM'`,
	); err != nil {
		return err
	}
	if err := ensureTableColumn(
		db,
		"platforms",
		"reverse_proxy_fixed_account_header",
		`reverse_proxy_fixed_account_header TEXT NOT NULL DEFAULT ''`,
	); err != nil {
		return err
	}
	return nil
}

func ensureTableColumn(db *sql.DB, table, column, columnDDL string) error {
	exists, err := hasTableColumn(db, table, column)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", table, columnDDL)
	if _, err := db.Exec(stmt); err != nil {
		return fmt.Errorf("migrate %s.%s: %w", table, column, err)
	}
	return nil
}

func hasTableColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, fmt.Errorf("inspect table %s: %w", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid       int
			name      string
			colType   string
			notNull   int
			defaultV  sql.NullString
			primaryID int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultV, &primaryID); err != nil {
			return false, fmt.Errorf("scan table_info(%s): %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate table_info(%s): %w", table, err)
	}
	return false, nil
}
