package state

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// persistenceCloser holds DB handles for cleanup. Implements io.Closer.
type persistenceCloser struct {
	stateDB *sql.DB
	cacheDB *sql.DB
}

func (c *persistenceCloser) Close() error {
	return errors.Join(c.stateDB.Close(), c.cacheDB.Close())
}

// PersistenceBootstrap initializes both databases, runs consistency repair,
// and returns a ready-to-use StateEngine plus an io.Closer for the DB handles.
//
// Steps:
//  1. Open/create state.db and cache.db with recommended pragmas.
//  2. Run DDL on both databases.
//  3. Run consistency repair (cross-db orphan cleanup).
//  4. Construct and return StateEngine.
func PersistenceBootstrap(stateDir, cacheDir string) (engine *StateEngine, closer io.Closer, err error) {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("create state dir %s: %w", stateDir, err)
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("create cache dir %s: %w", cacheDir, err)
	}

	stateDBPath := filepath.Join(stateDir, "state.db")
	cacheDBPath := filepath.Join(cacheDir, "cache.db")

	stateDB, err := OpenDB(stateDBPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open state.db: %w", err)
	}

	cacheDB, err := OpenDB(cacheDBPath)
	if err != nil {
		stateDB.Close()
		return nil, nil, fmt.Errorf("open cache.db: %w", err)
	}

	if err := InitDB(stateDB, CreateStateDDL); err != nil {
		stateDB.Close()
		cacheDB.Close()
		return nil, nil, fmt.Errorf("init state.db: %w", err)
	}

	if err := InitDB(cacheDB, CreateCacheDDL); err != nil {
		stateDB.Close()
		cacheDB.Close()
		return nil, nil, fmt.Errorf("init cache.db: %w", err)
	}

	if err := RepairConsistency(stateDBPath, cacheDB); err != nil {
		stateDB.Close()
		cacheDB.Close()
		return nil, nil, fmt.Errorf("repair consistency: %w", err)
	}

	stateRepo := newStateRepo(stateDB)
	cacheRepo := newCacheRepo(cacheDB)
	engine = newStateEngine(stateRepo, cacheRepo)

	return engine, &persistenceCloser{stateDB: stateDB, cacheDB: cacheDB}, nil
}
