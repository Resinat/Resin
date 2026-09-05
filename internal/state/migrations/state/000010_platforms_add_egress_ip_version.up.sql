CREATE TABLE IF NOT EXISTS platforms (
    id                                   TEXT PRIMARY KEY,
    name                                 TEXT NOT NULL UNIQUE,
    sticky_ttl_ns                        INTEGER NOT NULL,
    regex_filters_json                   TEXT NOT NULL DEFAULT '[]',
    region_filters_json                  TEXT NOT NULL DEFAULT '[]',
    reverse_proxy_miss_action            TEXT NOT NULL DEFAULT 'TREAT_AS_EMPTY',
    reverse_proxy_empty_account_behavior TEXT NOT NULL DEFAULT 'RANDOM',
    reverse_proxy_fixed_account_header   TEXT NOT NULL DEFAULT '',
    allocation_policy                    TEXT NOT NULL DEFAULT 'BALANCED',
    passive_circuit_breaker_disabled     INTEGER NOT NULL DEFAULT 0,
    updated_at_ns                        INTEGER NOT NULL
);
ALTER TABLE platforms
ADD COLUMN egress_ip_version TEXT NOT NULL DEFAULT '';
