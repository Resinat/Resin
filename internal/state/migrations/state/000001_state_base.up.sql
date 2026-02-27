CREATE TABLE IF NOT EXISTS system_config (
	id              INTEGER PRIMARY KEY CHECK (id = 1),
	config_json     TEXT    NOT NULL,
	version         INTEGER NOT NULL,
	updated_at_ns   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS platforms (
	id                        TEXT PRIMARY KEY,
	name                      TEXT NOT NULL UNIQUE,
	sticky_ttl_ns             INTEGER NOT NULL,
	regex_filters_json        TEXT NOT NULL DEFAULT '[]',
	region_filters_json       TEXT NOT NULL DEFAULT '[]',
	reverse_proxy_miss_action TEXT NOT NULL DEFAULT 'RANDOM',
	allocation_policy         TEXT NOT NULL DEFAULT 'BALANCED',
	updated_at_ns             INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS subscriptions (
	id                           TEXT PRIMARY KEY,
	name                         TEXT NOT NULL,
	source_type                  TEXT NOT NULL DEFAULT 'remote',
	url                          TEXT NOT NULL,
	content                      TEXT NOT NULL DEFAULT '',
	update_interval_ns           INTEGER NOT NULL,
	enabled                      INTEGER NOT NULL DEFAULT 1,
	ephemeral                    INTEGER NOT NULL DEFAULT 0,
	ephemeral_node_evict_delay_ns INTEGER NOT NULL,
	created_at_ns                INTEGER NOT NULL,
	updated_at_ns                INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS account_header_rules (
	url_prefix    TEXT PRIMARY KEY,
	headers_json  TEXT NOT NULL,
	updated_at_ns INTEGER NOT NULL
);
