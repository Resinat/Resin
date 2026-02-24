// Package requestlog implements the structured request log subsystem.
// Logs are written asynchronously to rolling SQLite databases.
package requestlog

// CreateDDL defines the schema for request log databases.
// Each rolling DB gets its own request_logs + request_log_payloads tables.
const CreateDDL = `
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

CREATE INDEX IF NOT EXISTS idx_request_logs_ts_ns        ON request_logs(ts_ns);
CREATE INDEX IF NOT EXISTS idx_request_logs_proxy_type   ON request_logs(proxy_type);
CREATE INDEX IF NOT EXISTS idx_request_logs_platform_id  ON request_logs(platform_id);
CREATE INDEX IF NOT EXISTS idx_request_logs_platform_name ON request_logs(platform_name);
CREATE INDEX IF NOT EXISTS idx_request_logs_plat_acct    ON request_logs(platform_id, account);
CREATE INDEX IF NOT EXISTS idx_request_logs_target_host  ON request_logs(target_host);
CREATE INDEX IF NOT EXISTS idx_request_logs_egress_ip    ON request_logs(egress_ip);
`
