package state

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/resin-proxy/resin/internal/config"
	"github.com/resin-proxy/resin/internal/model"
	"github.com/resin-proxy/resin/internal/platform"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// StateRepo wraps state.db and provides transactional CRUD for strong-persist data.
// All writes are serialized by an internal mutex.
type StateRepo struct {
	db *sql.DB
	mu sync.Mutex
}

// newStateRepo creates a StateRepo for the given state.db connection.
func newStateRepo(db *sql.DB) *StateRepo {
	return &StateRepo{db: db}
}

// --- system_config ---

// GetSystemConfig loads the runtime config and version from state.db.
// Returns nil config and version 0 if no row exists.
func (r *StateRepo) GetSystemConfig() (*config.RuntimeConfig, int, error) {
	row := r.db.QueryRow("SELECT config_json, version FROM system_config WHERE id = 1")
	var configJSON string
	var version int
	if err := row.Scan(&configJSON, &version); err != nil {
		if err == sql.ErrNoRows {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("scan system_config: %w", err)
	}
	cfg := &config.RuntimeConfig{}
	if err := json.Unmarshal([]byte(configJSON), cfg); err != nil {
		return nil, 0, fmt.Errorf("unmarshal system_config: %w", err)
	}
	return cfg, version, nil
}

// SaveSystemConfig persists the runtime config with the given version.
func (r *StateRepo) SaveSystemConfig(cfg *config.RuntimeConfig, version int, updatedAtNs int64) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal system_config: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	_, err = r.db.Exec(`
		INSERT INTO system_config (id, config_json, version, updated_at_ns)
		VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			config_json   = excluded.config_json,
			version       = excluded.version,
			updated_at_ns = excluded.updated_at_ns
	`, string(data), version, updatedAtNs)
	return err
}

// --- platforms ---

// UpsertPlatform inserts or updates a platform by ID.
// If the name collides with a different platform's name, ErrConflict is returned.
func (r *StateRepo) UpsertPlatform(p model.Platform) error {
	// Validate regex_filters_json.
	if _, err := platform.DecodeRegexFiltersJSON(p.ID, p.RegexFiltersJSON); err != nil {
		return err
	}

	// Validate region_filters_json.
	regions, err := platform.DecodeRegionFiltersJSON(p.ID, p.RegionFiltersJSON)
	if err != nil {
		return err
	}
	if err := platform.ValidateRegionFilters(regions); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	_, err = r.db.Exec(`
		INSERT INTO platforms (id, name, sticky_ttl_ns, regex_filters_json, region_filters_json,
		                       reverse_proxy_miss_action, allocation_policy, updated_at_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name                     = excluded.name,
			sticky_ttl_ns            = excluded.sticky_ttl_ns,
			regex_filters_json       = excluded.regex_filters_json,
			region_filters_json      = excluded.region_filters_json,
			reverse_proxy_miss_action = excluded.reverse_proxy_miss_action,
			allocation_policy        = excluded.allocation_policy,
			updated_at_ns            = excluded.updated_at_ns
	`, p.ID, p.Name, p.StickyTTLNs, p.RegexFiltersJSON, p.RegionFiltersJSON,
		p.ReverseProxyMissAction, p.AllocationPolicy, p.UpdatedAtNs)
	if err != nil {
		if isSQLiteUniqueConstraint(err) {
			return fmt.Errorf("%w: platform name already exists", ErrConflict)
		}
		return err
	}
	return err
}

func isSQLiteUniqueConstraint(err error) bool {
	var sqlErr *sqlite.Error
	if !errors.As(err, &sqlErr) {
		return false
	}
	switch sqlErr.Code() {
	case sqlite3.SQLITE_CONSTRAINT_UNIQUE:
		return true
	}
	return false
}

// DeletePlatform removes a platform by ID.
func (r *StateRepo) DeletePlatform(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec("DELETE FROM platforms WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetPlatform returns one platform by ID.
func (r *StateRepo) GetPlatform(id string) (*model.Platform, error) {
	row := r.db.QueryRow(`SELECT id, name, sticky_ttl_ns, regex_filters_json, region_filters_json,
		reverse_proxy_miss_action, allocation_policy, updated_at_ns
		FROM platforms WHERE id = ?`, id)

	var p model.Platform
	if err := row.Scan(&p.ID, &p.Name, &p.StickyTTLNs, &p.RegexFiltersJSON,
		&p.RegionFiltersJSON, &p.ReverseProxyMissAction, &p.AllocationPolicy, &p.UpdatedAtNs); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

// ListPlatforms returns all platforms.
func (r *StateRepo) ListPlatforms() ([]model.Platform, error) {
	rows, err := r.db.Query("SELECT id, name, sticky_ttl_ns, regex_filters_json, region_filters_json, reverse_proxy_miss_action, allocation_policy, updated_at_ns FROM platforms")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []model.Platform
	for rows.Next() {
		var p model.Platform
		if err := rows.Scan(&p.ID, &p.Name, &p.StickyTTLNs, &p.RegexFiltersJSON,
			&p.RegionFiltersJSON, &p.ReverseProxyMissAction, &p.AllocationPolicy, &p.UpdatedAtNs); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

// --- subscriptions ---

// UpsertSubscription inserts or updates a subscription by ID.
// On update, created_at_ns is preserved (not overwritten).
func (r *StateRepo) UpsertSubscription(s model.Subscription) error {
	// Validate minimum update interval (30 seconds).
	const minInterval = int64(30 * time.Second)
	if s.UpdateIntervalNs < minInterval {
		return fmt.Errorf("update_interval_ns: must be >= %d (30s), got %d", minInterval, s.UpdateIntervalNs)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	_, err := r.db.Exec(`
		INSERT INTO subscriptions (id, name, url, update_interval_ns, enabled,
		                           ephemeral, created_at_ns, updated_at_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name               = excluded.name,
			url                = excluded.url,
			update_interval_ns = excluded.update_interval_ns,
			enabled            = excluded.enabled,
			ephemeral          = excluded.ephemeral,
			updated_at_ns      = excluded.updated_at_ns
	`, s.ID, s.Name, s.URL, s.UpdateIntervalNs, s.Enabled,
		s.Ephemeral, s.CreatedAtNs, s.UpdatedAtNs)
	return err
}

// DeleteSubscription removes a subscription by ID.
func (r *StateRepo) DeleteSubscription(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec("DELETE FROM subscriptions WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListSubscriptions returns all subscriptions.
func (r *StateRepo) ListSubscriptions() ([]model.Subscription, error) {
	rows, err := r.db.Query(`SELECT id, name, url, update_interval_ns, enabled,
		ephemeral, created_at_ns, updated_at_ns FROM subscriptions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []model.Subscription
	for rows.Next() {
		var s model.Subscription
		if err := rows.Scan(&s.ID, &s.Name, &s.URL, &s.UpdateIntervalNs, &s.Enabled,
			&s.Ephemeral, &s.CreatedAtNs, &s.UpdatedAtNs); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// --- account_header_rules ---

// UpsertAccountHeaderRuleWithCreated inserts or updates a rule by url_prefix and
// reports whether the row was newly created.
func (r *StateRepo) UpsertAccountHeaderRuleWithCreated(rule model.AccountHeaderRule) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	tx, err := r.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	insertRes, err := tx.Exec(`
		INSERT INTO account_header_rules (url_prefix, headers_json, updated_at_ns)
		VALUES (?, ?, ?)
		ON CONFLICT(url_prefix) DO NOTHING
	`, rule.URLPrefix, rule.HeadersJSON, rule.UpdatedAtNs)
	if err != nil {
		return false, err
	}

	inserted := false
	if n, _ := insertRes.RowsAffected(); n > 0 {
		inserted = true
	} else {
		// Existing row: apply update path.
		if _, err := tx.Exec(`
			UPDATE account_header_rules
			SET headers_json = ?, updated_at_ns = ?
			WHERE url_prefix = ?
		`, rule.HeadersJSON, rule.UpdatedAtNs, rule.URLPrefix); err != nil {
			return false, err
		}
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}
	return inserted, nil
}

// DeleteAccountHeaderRule removes a rule by url_prefix.
func (r *StateRepo) DeleteAccountHeaderRule(prefix string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec("DELETE FROM account_header_rules WHERE url_prefix = ?", prefix)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListAccountHeaderRules returns all rules.
func (r *StateRepo) ListAccountHeaderRules() ([]model.AccountHeaderRule, error) {
	rows, err := r.db.Query("SELECT url_prefix, headers_json, updated_at_ns FROM account_header_rules")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []model.AccountHeaderRule
	for rows.Next() {
		var rule model.AccountHeaderRule
		if err := rows.Scan(&rule.URLPrefix, &rule.HeadersJSON, &rule.UpdatedAtNs); err != nil {
			return nil, err
		}
		result = append(result, rule)
	}
	return result, rows.Err()
}
