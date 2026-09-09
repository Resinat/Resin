CREATE TABLE IF NOT EXISTS nodes_dynamic__old_schema (
	hash                                    TEXT PRIMARY KEY,
	failure_count                           INTEGER NOT NULL DEFAULT 0,
	circuit_open_since                      INTEGER NOT NULL DEFAULT 0,
	egress_ip                               TEXT NOT NULL DEFAULT '',
	egress_region                           TEXT NOT NULL DEFAULT '',
	egress_updated_at_ns                    INTEGER NOT NULL DEFAULT 0,
	last_latency_probe_attempt_ns           INTEGER NOT NULL DEFAULT 0,
	last_authority_latency_probe_attempt_ns INTEGER NOT NULL DEFAULT 0,
	last_egress_update_attempt_ns           INTEGER NOT NULL DEFAULT 0
);

INSERT INTO nodes_dynamic__old_schema (
	hash, failure_count, circuit_open_since, egress_ip, egress_region, egress_updated_at_ns,
	last_latency_probe_attempt_ns, last_authority_latency_probe_attempt_ns, last_egress_update_attempt_ns
)
SELECT
	hash, failure_count, circuit_open_since, egress_ip, egress_region, egress_updated_at_ns,
	last_latency_probe_attempt_ns, last_authority_latency_probe_attempt_ns, last_egress_update_attempt_ns
FROM nodes_dynamic;

DROP TABLE nodes_dynamic;

ALTER TABLE nodes_dynamic__old_schema RENAME TO nodes_dynamic;
