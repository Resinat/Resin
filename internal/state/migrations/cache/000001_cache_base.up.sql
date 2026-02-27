CREATE TABLE IF NOT EXISTS nodes_static (
	hash             TEXT PRIMARY KEY,
	raw_options_json TEXT NOT NULL,
	created_at_ns    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS nodes_dynamic (
	hash                                TEXT PRIMARY KEY,
	failure_count                       INTEGER NOT NULL DEFAULT 0,
	circuit_open_since                  INTEGER NOT NULL DEFAULT 0,
	egress_ip                           TEXT NOT NULL DEFAULT '',
	egress_region                       TEXT NOT NULL DEFAULT '',
	egress_updated_at_ns                INTEGER NOT NULL DEFAULT 0,
	last_latency_probe_attempt_ns       INTEGER NOT NULL DEFAULT 0,
	last_authority_latency_probe_attempt_ns INTEGER NOT NULL DEFAULT 0,
	last_egress_update_attempt_ns       INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS node_latency (
	node_hash       TEXT NOT NULL,
	domain          TEXT NOT NULL,
	ewma_ns         INTEGER NOT NULL,
	last_updated_ns INTEGER NOT NULL,
	PRIMARY KEY (node_hash, domain)
);

CREATE TABLE IF NOT EXISTS leases (
	platform_id      TEXT NOT NULL,
	account          TEXT NOT NULL,
	node_hash        TEXT NOT NULL,
	egress_ip        TEXT NOT NULL DEFAULT '',
	created_at_ns    INTEGER NOT NULL DEFAULT 0,
	expiry_ns        INTEGER NOT NULL,
	last_accessed_ns INTEGER NOT NULL,
	PRIMARY KEY (platform_id, account)
);

CREATE TABLE IF NOT EXISTS subscription_nodes (
	subscription_id TEXT NOT NULL,
	node_hash       TEXT NOT NULL,
	tags_json       TEXT NOT NULL DEFAULT '[]',
	PRIMARY KEY (subscription_id, node_hash)
);
