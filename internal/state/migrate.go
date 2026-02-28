package state

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	migratedb "github.com/golang-migrate/migrate/v4/database"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

const (
	stateMigrationsPath = "migrations/state"
	cacheMigrationsPath = "migrations/cache"

	// Keep these version markers in sync with SQL files under migrations/state/.
	// stateLegacyBaselineVersion must remain fixed to the highest migration
	// version covered by compatibility detection for pre-migrate databases.
	stateVersionBaseSchema              = 1
	stateVersionAddEmptyAccountBehavior = 2
	stateVersionAddFixedAccountHeader   = 3
	stateVersionNormalizeMissAction     = 4
	stateLegacyBaselineVersion          = stateVersionAddFixedAccountHeader
)

//go:embed migrations/state/*.sql migrations/cache/*.sql
var migrationsFS embed.FS

type preMigrateHook func(db *sql.DB, driver migratedb.Driver) error

// MigrateStateDB applies state.db migrations.
func MigrateStateDB(db *sql.DB) error {
	return migrateSQLiteDB(db, stateMigrationsPath, migrateDefaultTable, prepareLegacyStateBaseline)
}

// MigrateCacheDB applies cache.db migrations.
func MigrateCacheDB(db *sql.DB) error {
	return migrateSQLiteDB(db, cacheMigrationsPath, migrateDefaultTable, nil)
}

const migrateDefaultTable = "schema_migrations"

func migrateSQLiteDB(db *sql.DB, fsPath, migrationsTable string, preHook preMigrateHook) error {
	if db == nil {
		return fmt.Errorf("migrate %s: nil db", fsPath)
	}

	sourceDriver, err := iofs.New(migrationsFS, fsPath)
	if err != nil {
		return fmt.Errorf("migrate %s: init source: %w", fsPath, err)
	}

	dbDriver, err := migratesqlite.WithInstance(db, &migratesqlite.Config{
		MigrationsTable: migrationsTable,
	})
	if err != nil {
		return fmt.Errorf("migrate %s: init db driver: %w", fsPath, err)
	}

	if preHook != nil {
		if err := preHook(db, dbDriver); err != nil {
			return fmt.Errorf("migrate %s: prehook: %w", fsPath, err)
		}
	}

	m, err := migrate.NewWithInstance("iofs", sourceDriver, "sqlite", dbDriver)
	if err != nil {
		return fmt.Errorf("migrate %s: init migrator: %w", fsPath, err)
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate %s: up: %w", fsPath, err)
	}
	return nil
}

// prepareLegacyStateBaseline aligns migration version metadata for databases
// created before golang-migrate was introduced.
func prepareLegacyStateBaseline(db *sql.DB, driver migratedb.Driver) error {
	hasVersion, err := hasMigrationVersion(db, migrateDefaultTable)
	if err != nil {
		return err
	}
	if hasVersion {
		return nil
	}

	hasPlatforms, err := hasTable(db, "platforms")
	if err != nil {
		return err
	}
	if !hasPlatforms {
		return nil
	}

	hasEmptyBehavior, err := hasTableColumn(db, "platforms", "reverse_proxy_empty_account_behavior")
	if err != nil {
		return err
	}
	hasFixedHeader, err := hasTableColumn(db, "platforms", "reverse_proxy_fixed_account_header")
	if err != nil {
		return err
	}

	switch {
	case hasEmptyBehavior && hasFixedHeader:
		return setMigrationVersion(driver, stateLegacyBaselineVersion)
	case hasEmptyBehavior && !hasFixedHeader:
		return setMigrationVersion(driver, stateVersionAddEmptyAccountBehavior)
	case !hasEmptyBehavior && hasFixedHeader:
		// This mixed state should not happen in normal upgrades. Repair it once.
		if err := ensureTableColumn(
			db,
			"platforms",
			"reverse_proxy_empty_account_behavior",
			`reverse_proxy_empty_account_behavior TEXT NOT NULL DEFAULT 'RANDOM'`,
		); err != nil {
			return err
		}
		return setMigrationVersion(driver, stateLegacyBaselineVersion)
	default:
		// No baseline metadata: migrate from base schema.
		return nil
	}
}

func hasMigrationVersion(db *sql.DB, table string) (bool, error) {
	var count int
	if err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count); err != nil {
		return false, fmt.Errorf("read %s: %w", table, err)
	}
	return count > 0, nil
}

func setMigrationVersion(driver migratedb.Driver, version int) error {
	if err := driver.SetVersion(version, false); err != nil {
		return fmt.Errorf("set migration version=%d: %w", version, err)
	}
	return nil
}

func hasTable(db *sql.DB, table string) (bool, error) {
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lookup table %s: %w", table, err)
	}
	return true, nil
}
