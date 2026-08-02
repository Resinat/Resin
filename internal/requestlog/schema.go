// Package requestlog implements the structured request log subsystem.
// Logs are written asynchronously to rolling SQLite databases.
package requestlog

import (
	"database/sql"
	"fmt"
)

const requestLogTablesDDL = `
CREATE TABLE IF NOT EXISTS request_logs (
	id                    TEXT PRIMARY KEY,
	ts_ns                 INTEGER NOT NULL,
	proxy_type            INTEGER NOT NULL,
	client_ip             TEXT NOT NULL DEFAULT '',
	platform_id           TEXT NOT NULL DEFAULT '',
	platform_name         TEXT NOT NULL DEFAULT '',
	account               TEXT NOT NULL DEFAULT '',
	target_host           TEXT NOT NULL DEFAULT '',
	target_url            TEXT NOT NULL DEFAULT '',
	node_hash             TEXT NOT NULL DEFAULT '',
	node_tag              TEXT NOT NULL DEFAULT '',
	egress_ip             TEXT NOT NULL DEFAULT '',
	duration_ns           INTEGER NOT NULL DEFAULT 0,
	first_byte_duration_ns INTEGER NOT NULL DEFAULT 0,
	net_ok                INTEGER NOT NULL DEFAULT 0,
	http_method           TEXT NOT NULL DEFAULT '',
	http_status           INTEGER NOT NULL DEFAULT 0,
	resin_error           TEXT NOT NULL DEFAULT '',
	upstream_stage        TEXT NOT NULL DEFAULT '',
	upstream_err_kind     TEXT NOT NULL DEFAULT '',
	upstream_errno        TEXT NOT NULL DEFAULT '',
	upstream_err_msg      TEXT NOT NULL DEFAULT '',
	ingress_bytes         INTEGER NOT NULL DEFAULT 0,
	egress_bytes          INTEGER NOT NULL DEFAULT 0,
	payload_present       INTEGER NOT NULL DEFAULT 0,
	req_headers_len       INTEGER NOT NULL DEFAULT 0,
	req_body_len          INTEGER NOT NULL DEFAULT 0,
	resp_headers_len      INTEGER NOT NULL DEFAULT 0,
	resp_body_len         INTEGER NOT NULL DEFAULT 0,
	req_headers_truncated  INTEGER NOT NULL DEFAULT 0,
	req_body_truncated     INTEGER NOT NULL DEFAULT 0,
	resp_headers_truncated INTEGER NOT NULL DEFAULT 0,
	resp_body_truncated    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS request_log_payloads (
	log_id        TEXT PRIMARY KEY REFERENCES request_logs(id) ON DELETE CASCADE,
	req_headers   BLOB,
	req_body      BLOB,
	resp_headers  BLOB,
	resp_body     BLOB
);
`

const requestLogIndexesDDL = `
CREATE INDEX IF NOT EXISTS idx_request_logs_ts_id
	ON request_logs(ts_ns DESC, id ASC);
CREATE INDEX IF NOT EXISTS idx_request_logs_proxy_type_ts_id
	ON request_logs(proxy_type, ts_ns DESC, id ASC);
CREATE INDEX IF NOT EXISTS idx_request_logs_account_ts_id
	ON request_logs(account, ts_ns DESC, id ASC) WHERE account <> '';
CREATE INDEX IF NOT EXISTS idx_request_logs_platform_name ON request_logs(platform_name);
CREATE INDEX IF NOT EXISTS idx_request_logs_plat_acct    ON request_logs(platform_id, account);
CREATE INDEX IF NOT EXISTS idx_request_logs_target_host  ON request_logs(target_host);
CREATE INDEX IF NOT EXISTS idx_request_logs_egress_ip    ON request_logs(egress_ip);
`

const obsoleteRequestLogIndexesDDL = `
DROP INDEX IF EXISTS idx_request_logs_ts_ns;
DROP INDEX IF EXISTS idx_request_logs_proxy_type;
DROP INDEX IF EXISTS idx_request_logs_platform_id;
`

// CreateDDL defines the schema for each rolling request-log database.
const CreateDDL = requestLogTablesDDL + requestLogIndexesDDL

func ensureRequestLogSchema(db *sql.DB) error {
	if err := ensureRequestLogColumn(db, "request_logs", "first_byte_duration_ns", "first_byte_duration_ns INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin request log index migration: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(requestLogIndexesDDL); err != nil {
		return fmt.Errorf("create request log indexes: %w", err)
	}
	if _, err := tx.Exec(obsoleteRequestLogIndexesDDL); err != nil {
		return fmt.Errorf("drop obsolete request log indexes: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit request log index migration: %w", err)
	}
	return nil
}

func ensureRequestLogColumn(db *sql.DB, table, column, columnDDL string) error {
	exists, err := hasRequestLogColumn(db, table, column)
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

func hasRequestLogColumn(db *sql.DB, table, column string) (bool, error) {
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
