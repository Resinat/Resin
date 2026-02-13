package state

import (
	"database/sql"
	"fmt"

	"github.com/resin-proxy/resin/internal/model"
)

// CacheRepo wraps cache.db and provides batch read/write for weak-persist data.
type CacheRepo struct {
	db *sql.DB
}

// newCacheRepo creates a CacheRepo for the given cache.db connection.
func newCacheRepo(db *sql.DB) *CacheRepo {
	return &CacheRepo{db: db}
}

// --- nodes_static ---

// BulkUpsertNodesStatic batch-inserts or updates node static records.
func (r *CacheRepo) BulkUpsertNodesStatic(nodes []model.NodeStatic) error {
	return r.bulkExec(
		upsertNodesStaticSQL,
		len(nodes),
		func(stmt *sql.Stmt, i int) error {
			n := nodes[i]
			_, err := stmt.Exec(n.Hash, n.RawOptionsJSON, n.CreatedAtNs)
			return err
		},
	)
}

// BulkDeleteNodesStatic batch-deletes node static records by hash.
func (r *CacheRepo) BulkDeleteNodesStatic(hashes []string) error {
	return r.bulkExec(
		deleteNodesStaticSQL,
		len(hashes),
		func(stmt *sql.Stmt, i int) error {
			_, err := stmt.Exec(hashes[i])
			return err
		},
	)
}

// LoadAllNodesStatic reads all node static records.
func (r *CacheRepo) LoadAllNodesStatic() ([]model.NodeStatic, error) {
	rows, err := r.db.Query("SELECT hash, raw_options_json, created_at_ns FROM nodes_static")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []model.NodeStatic
	for rows.Next() {
		var n model.NodeStatic
		if err := rows.Scan(&n.Hash, &n.RawOptionsJSON, &n.CreatedAtNs); err != nil {
			return nil, err
		}
		result = append(result, n)
	}
	return result, rows.Err()
}

// --- nodes_dynamic ---

// BulkUpsertNodesDynamic batch-inserts or updates node dynamic records.
func (r *CacheRepo) BulkUpsertNodesDynamic(nodes []model.NodeDynamic) error {
	return r.bulkExec(
		upsertNodesDynamicSQL,
		len(nodes),
		func(stmt *sql.Stmt, i int) error {
			n := nodes[i]
			_, err := stmt.Exec(n.Hash, n.FailureCount, n.CircuitOpenSince, n.EgressIP, n.EgressUpdatedAtNs)
			return err
		},
	)
}

// BulkDeleteNodesDynamic batch-deletes node dynamic records by hash.
func (r *CacheRepo) BulkDeleteNodesDynamic(hashes []string) error {
	return r.bulkExec(
		deleteNodesDynamicSQL,
		len(hashes),
		func(stmt *sql.Stmt, i int) error {
			_, err := stmt.Exec(hashes[i])
			return err
		},
	)
}

// LoadAllNodesDynamic reads all node dynamic records.
func (r *CacheRepo) LoadAllNodesDynamic() ([]model.NodeDynamic, error) {
	rows, err := r.db.Query("SELECT hash, failure_count, circuit_open_since, egress_ip, egress_updated_at_ns FROM nodes_dynamic")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []model.NodeDynamic
	for rows.Next() {
		var n model.NodeDynamic
		if err := rows.Scan(&n.Hash, &n.FailureCount, &n.CircuitOpenSince, &n.EgressIP, &n.EgressUpdatedAtNs); err != nil {
			return nil, err
		}
		result = append(result, n)
	}
	return result, rows.Err()
}

// --- node_latency ---

// BulkUpsertNodeLatency batch-inserts or updates node latency records.
func (r *CacheRepo) BulkUpsertNodeLatency(entries []model.NodeLatency) error {
	return r.bulkExec(
		upsertNodeLatencySQL,
		len(entries),
		func(stmt *sql.Stmt, i int) error {
			e := entries[i]
			_, err := stmt.Exec(e.NodeHash, e.Domain, e.EwmaNs, e.LastUpdatedNs)
			return err
		},
	)
}

// BulkDeleteNodeLatency batch-deletes node latency records by composite key.
func (r *CacheRepo) BulkDeleteNodeLatency(keys []model.NodeLatencyKey) error {
	return r.bulkExec(
		deleteNodeLatencySQL,
		len(keys),
		func(stmt *sql.Stmt, i int) error {
			_, err := stmt.Exec(keys[i].NodeHash, keys[i].Domain)
			return err
		},
	)
}

// LoadAllNodeLatency reads all node latency records.
func (r *CacheRepo) LoadAllNodeLatency() ([]model.NodeLatency, error) {
	rows, err := r.db.Query("SELECT node_hash, domain, ewma_ns, last_updated_ns FROM node_latency")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []model.NodeLatency
	for rows.Next() {
		var e model.NodeLatency
		if err := rows.Scan(&e.NodeHash, &e.Domain, &e.EwmaNs, &e.LastUpdatedNs); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

// --- leases ---

// BulkUpsertLeases batch-inserts or updates lease records.
func (r *CacheRepo) BulkUpsertLeases(leases []model.Lease) error {
	return r.bulkExec(
		upsertLeasesSQL,
		len(leases),
		func(stmt *sql.Stmt, i int) error {
			l := leases[i]
			_, err := stmt.Exec(l.PlatformID, l.Account, l.NodeHash, l.EgressIP, l.ExpiryNs, l.LastAccessedNs)
			return err
		},
	)
}

// BulkDeleteLeases batch-deletes lease records by composite key.
func (r *CacheRepo) BulkDeleteLeases(keys []model.LeaseKey) error {
	return r.bulkExec(
		deleteLeasesSQL,
		len(keys),
		func(stmt *sql.Stmt, i int) error {
			_, err := stmt.Exec(keys[i].PlatformID, keys[i].Account)
			return err
		},
	)
}

// LoadAllLeases reads all lease records.
func (r *CacheRepo) LoadAllLeases() ([]model.Lease, error) {
	rows, err := r.db.Query("SELECT platform_id, account, node_hash, egress_ip, expiry_ns, last_accessed_ns FROM leases")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []model.Lease
	for rows.Next() {
		var l model.Lease
		if err := rows.Scan(&l.PlatformID, &l.Account, &l.NodeHash, &l.EgressIP, &l.ExpiryNs, &l.LastAccessedNs); err != nil {
			return nil, err
		}
		result = append(result, l)
	}
	return result, rows.Err()
}

// --- subscription_nodes ---

// BulkUpsertSubscriptionNodes batch-inserts or updates subscription-node links.
func (r *CacheRepo) BulkUpsertSubscriptionNodes(nodes []model.SubscriptionNode) error {
	return r.bulkExec(
		upsertSubscriptionNodesSQL,
		len(nodes),
		func(stmt *sql.Stmt, i int) error {
			sn := nodes[i]
			_, err := stmt.Exec(sn.SubscriptionID, sn.NodeHash, sn.TagsJSON)
			return err
		},
	)
}

// BulkDeleteSubscriptionNodes batch-deletes subscription-node links by composite key.
func (r *CacheRepo) BulkDeleteSubscriptionNodes(keys []model.SubscriptionNodeKey) error {
	return r.bulkExec(
		deleteSubscriptionNodesSQL,
		len(keys),
		func(stmt *sql.Stmt, i int) error {
			_, err := stmt.Exec(keys[i].SubscriptionID, keys[i].NodeHash)
			return err
		},
	)
}

// LoadAllSubscriptionNodes reads all subscription-node links.
func (r *CacheRepo) LoadAllSubscriptionNodes() ([]model.SubscriptionNode, error) {
	rows, err := r.db.Query("SELECT subscription_id, node_hash, tags_json FROM subscription_nodes")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []model.SubscriptionNode
	for rows.Next() {
		var sn model.SubscriptionNode
		if err := rows.Scan(&sn.SubscriptionID, &sn.NodeHash, &sn.TagsJSON); err != nil {
			return nil, err
		}
		result = append(result, sn)
	}
	return result, rows.Err()
}

// --- internal helpers ---

// bulkExecTx runs a prepared statement within an existing transaction for n rows.
func bulkExecTx(tx *sql.Tx, query string, n int, execFn func(stmt *sql.Stmt, i int) error) error {
	if n == 0 {
		return nil
	}

	stmt, err := tx.Prepare(query)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	for i := 0; i < n; i++ {
		if err := execFn(stmt, i); err != nil {
			return fmt.Errorf("exec row %d: %w", i, err)
		}
	}
	return nil
}

// bulkExec runs a prepared statement in its own transaction for n rows.
// Used by individual BulkUpsert*/BulkDelete* methods (tests, bootstrap).
func (r *CacheRepo) bulkExec(query string, n int, execFn func(stmt *sql.Stmt, i int) error) error {
	if n == 0 {
		return nil
	}

	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if err := bulkExecTx(tx, query, n, execFn); err != nil {
		return err
	}
	return tx.Commit()
}

// FlushOps holds all upsert/delete slices for a single-transaction cache flush.
type FlushOps struct {
	UpsertNodesStatic       []model.NodeStatic
	DeleteNodesStatic       []string
	UpsertSubscriptionNodes []model.SubscriptionNode
	DeleteSubscriptionNodes []model.SubscriptionNodeKey
	UpsertNodesDynamic      []model.NodeDynamic
	DeleteNodesDynamic      []string
	UpsertNodeLatency       []model.NodeLatency
	DeleteNodeLatency       []model.NodeLatencyKey
	UpsertLeases            []model.Lease
	DeleteLeases            []model.LeaseKey
}

// FlushTx executes all upserts and deletes in a single transaction.
//
// Upsert order: nodes_static → subscription_nodes → nodes_dynamic → node_latency → leases
// Delete order: leases → node_latency → nodes_dynamic → subscription_nodes → nodes_static
func (r *CacheRepo) FlushTx(ops FlushOps) error {
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("begin flush tx: %w", err)
	}
	defer tx.Rollback()

	// Upserts in dependency order.
	steps := []struct {
		name  string
		query string
		n     int
		exec  func(*sql.Stmt, int) error
	}{
		{"upsert_nodes_static", upsertNodesStaticSQL, len(ops.UpsertNodesStatic), func(s *sql.Stmt, i int) error {
			n := ops.UpsertNodesStatic[i]
			_, err := s.Exec(n.Hash, n.RawOptionsJSON, n.CreatedAtNs)
			return err
		}},
		{"upsert_subscription_nodes", upsertSubscriptionNodesSQL, len(ops.UpsertSubscriptionNodes), func(s *sql.Stmt, i int) error {
			sn := ops.UpsertSubscriptionNodes[i]
			_, err := s.Exec(sn.SubscriptionID, sn.NodeHash, sn.TagsJSON)
			return err
		}},
		{"upsert_nodes_dynamic", upsertNodesDynamicSQL, len(ops.UpsertNodesDynamic), func(s *sql.Stmt, i int) error {
			n := ops.UpsertNodesDynamic[i]
			_, err := s.Exec(n.Hash, n.FailureCount, n.CircuitOpenSince, n.EgressIP, n.EgressUpdatedAtNs)
			return err
		}},
		{"upsert_node_latency", upsertNodeLatencySQL, len(ops.UpsertNodeLatency), func(s *sql.Stmt, i int) error {
			e := ops.UpsertNodeLatency[i]
			_, err := s.Exec(e.NodeHash, e.Domain, e.EwmaNs, e.LastUpdatedNs)
			return err
		}},
		{"upsert_leases", upsertLeasesSQL, len(ops.UpsertLeases), func(s *sql.Stmt, i int) error {
			l := ops.UpsertLeases[i]
			_, err := s.Exec(l.PlatformID, l.Account, l.NodeHash, l.EgressIP, l.ExpiryNs, l.LastAccessedNs)
			return err
		}},
		// Deletes in reverse dependency order.
		{"delete_leases", deleteLeasesSQL, len(ops.DeleteLeases), func(s *sql.Stmt, i int) error {
			_, err := s.Exec(ops.DeleteLeases[i].PlatformID, ops.DeleteLeases[i].Account)
			return err
		}},
		{"delete_node_latency", deleteNodeLatencySQL, len(ops.DeleteNodeLatency), func(s *sql.Stmt, i int) error {
			_, err := s.Exec(ops.DeleteNodeLatency[i].NodeHash, ops.DeleteNodeLatency[i].Domain)
			return err
		}},
		{"delete_nodes_dynamic", deleteNodesDynamicSQL, len(ops.DeleteNodesDynamic), func(s *sql.Stmt, i int) error {
			_, err := s.Exec(ops.DeleteNodesDynamic[i])
			return err
		}},
		{"delete_subscription_nodes", deleteSubscriptionNodesSQL, len(ops.DeleteSubscriptionNodes), func(s *sql.Stmt, i int) error {
			_, err := s.Exec(ops.DeleteSubscriptionNodes[i].SubscriptionID, ops.DeleteSubscriptionNodes[i].NodeHash)
			return err
		}},
		{"delete_nodes_static", deleteNodesStaticSQL, len(ops.DeleteNodesStatic), func(s *sql.Stmt, i int) error {
			_, err := s.Exec(ops.DeleteNodesStatic[i])
			return err
		}},
	}

	for _, step := range steps {
		if err := bulkExecTx(tx, step.query, step.n, step.exec); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}

	return tx.Commit()
}

// SQL constants for FlushTx. Extracted to avoid string duplication.
const (
	upsertNodesStaticSQL = `INSERT INTO nodes_static (hash, raw_options_json, created_at_ns)
		 VALUES (?, ?, ?)
		 ON CONFLICT(hash) DO UPDATE SET
			raw_options_json = excluded.raw_options_json,
			created_at_ns    = excluded.created_at_ns`

	upsertNodesDynamicSQL = `INSERT INTO nodes_dynamic (hash, failure_count, circuit_open_since, egress_ip, egress_updated_at_ns)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(hash) DO UPDATE SET
			failure_count      = excluded.failure_count,
			circuit_open_since = excluded.circuit_open_since,
			egress_ip          = excluded.egress_ip,
			egress_updated_at_ns = excluded.egress_updated_at_ns`

	upsertNodeLatencySQL = `INSERT INTO node_latency (node_hash, domain, ewma_ns, last_updated_ns)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(node_hash, domain) DO UPDATE SET
			ewma_ns         = excluded.ewma_ns,
			last_updated_ns = excluded.last_updated_ns`

	upsertLeasesSQL = `INSERT INTO leases (platform_id, account, node_hash, egress_ip, expiry_ns, last_accessed_ns)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(platform_id, account) DO UPDATE SET
			node_hash       = excluded.node_hash,
			egress_ip       = excluded.egress_ip,
			expiry_ns       = excluded.expiry_ns,
			last_accessed_ns = excluded.last_accessed_ns`

	upsertSubscriptionNodesSQL = `INSERT INTO subscription_nodes (subscription_id, node_hash, tags_json)
		 VALUES (?, ?, ?)
		 ON CONFLICT(subscription_id, node_hash) DO UPDATE SET
			tags_json = excluded.tags_json`

	deleteNodesStaticSQL       = "DELETE FROM nodes_static WHERE hash = ?"
	deleteNodesDynamicSQL      = "DELETE FROM nodes_dynamic WHERE hash = ?"
	deleteNodeLatencySQL       = "DELETE FROM node_latency WHERE node_hash = ? AND domain = ?"
	deleteLeasesSQL            = "DELETE FROM leases WHERE platform_id = ? AND account = ?"
	deleteSubscriptionNodesSQL = "DELETE FROM subscription_nodes WHERE subscription_id = ? AND node_hash = ?"
)
