// Package state implements the persistence layer: SQLite repos, StateEngine,
// dirty-set flush, consistency repair, and bootstrap.
package state

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

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

// InitDB executes raw DDL statements on the given database.
// Used by non-state SQLite stores (for example metrics and request logs).
func InitDB(db *sql.DB, ddl string) error {
	_, err := db.Exec(ddl)
	return err
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
