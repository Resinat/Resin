package state

import (
	"database/sql"
	"fmt"
)

// RepairConsistency runs orphan-cleanup SQL on cache.db, cross-referencing
// state.db via ATTACH. All DELETEs execute in a single transaction to avoid
// half-repaired state on crash.
//
// Cleanup order (by dependency):
//  1. subscription_nodes: remove entries whose subscription_id is missing from
//     state.subscriptions OR whose node_hash is missing from nodes_static.
//  2. nodes_static: remove entries with no remaining reference in subscription_nodes.
//  3. nodes_dynamic: remove entries whose hash is missing from nodes_static.
//  4. node_latency: remove entries whose node_hash is missing from nodes_static.
//  5. leases: remove entries whose platform_id is missing from state.platforms
//     OR whose node_hash is missing from nodes_static.
func RepairConsistency(stateDBPath string, cacheDB *sql.DB) error {
	// ATTACH state.db so we can cross-query.
	attachSQL := fmt.Sprintf("ATTACH DATABASE %q AS state_db", stateDBPath)
	if _, err := cacheDB.Exec(attachSQL); err != nil {
		return fmt.Errorf("attach state_db: %w", err)
	}
	defer cacheDB.Exec("DETACH DATABASE state_db")

	tx, err := cacheDB.Begin()
	if err != nil {
		return fmt.Errorf("begin repair tx: %w", err)
	}
	defer tx.Rollback()

	stmts := []string{
		// 1. subscription_nodes: orphan subscription or orphan node
		`DELETE FROM subscription_nodes
		 WHERE subscription_id NOT IN (SELECT id FROM state_db.subscriptions)
		    OR node_hash NOT IN (SELECT hash FROM nodes_static)`,

		// 2. nodes_static: no subscription references
		`DELETE FROM nodes_static
		 WHERE hash NOT IN (SELECT node_hash FROM subscription_nodes)`,

		// 3. nodes_dynamic: orphan to nodes_static
		`DELETE FROM nodes_dynamic
		 WHERE hash NOT IN (SELECT hash FROM nodes_static)`,

		// 4. node_latency: orphan to nodes_static
		`DELETE FROM node_latency
		 WHERE node_hash NOT IN (SELECT hash FROM nodes_static)`,

		// 5. leases: orphan platform or orphan node
		`DELETE FROM leases
		 WHERE platform_id NOT IN (SELECT id FROM state_db.platforms)
		    OR node_hash NOT IN (SELECT hash FROM nodes_static)`,
	}

	for i, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("repair step %d: %w", i+1, err)
		}
	}

	return tx.Commit()
}
