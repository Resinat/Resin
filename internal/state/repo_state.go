package state

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/resin-proxy/resin/internal/config"
	"github.com/resin-proxy/resin/internal/model"
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
// If the name collides with a different platform's name, the UNIQUE constraint
// error is returned to the caller.
func (r *StateRepo) UpsertPlatform(p model.Platform) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, err := r.db.Exec(`
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
	return err
}

// DeletePlatform removes a platform by ID.
func (r *StateRepo) DeletePlatform(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, err := r.db.Exec("DELETE FROM platforms WHERE id = ?", id)
	return err
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
	r.mu.Lock()
	defer r.mu.Unlock()

	_, err := r.db.Exec(`
		INSERT INTO subscriptions (id, name, url, update_interval_ns, enabled, last_updated_ns,
		                           last_checked_ns, ephemeral, last_error, created_at_ns, updated_at_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name               = excluded.name,
			url                = excluded.url,
			update_interval_ns = excluded.update_interval_ns,
			enabled            = excluded.enabled,
			last_updated_ns    = excluded.last_updated_ns,
			last_checked_ns    = excluded.last_checked_ns,
			ephemeral          = excluded.ephemeral,
			last_error         = excluded.last_error,
			updated_at_ns      = excluded.updated_at_ns
	`, s.ID, s.Name, s.URL, s.UpdateIntervalNs, s.Enabled, s.LastUpdatedNs,
		s.LastCheckedNs, s.Ephemeral, s.LastError, s.CreatedAtNs, s.UpdatedAtNs)
	return err
}

// DeleteSubscription removes a subscription by ID.
func (r *StateRepo) DeleteSubscription(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, err := r.db.Exec("DELETE FROM subscriptions WHERE id = ?", id)
	return err
}

// ListSubscriptions returns all subscriptions.
func (r *StateRepo) ListSubscriptions() ([]model.Subscription, error) {
	rows, err := r.db.Query(`SELECT id, name, url, update_interval_ns, enabled, last_updated_ns,
		last_checked_ns, ephemeral, last_error, created_at_ns, updated_at_ns FROM subscriptions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []model.Subscription
	for rows.Next() {
		var s model.Subscription
		if err := rows.Scan(&s.ID, &s.Name, &s.URL, &s.UpdateIntervalNs, &s.Enabled,
			&s.LastUpdatedNs, &s.LastCheckedNs, &s.Ephemeral, &s.LastError,
			&s.CreatedAtNs, &s.UpdatedAtNs); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// --- account_header_rules ---

// UpsertAccountHeaderRule inserts or updates a rule by url_prefix.
func (r *StateRepo) UpsertAccountHeaderRule(rule model.AccountHeaderRule) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, err := r.db.Exec(`
		INSERT INTO account_header_rules (url_prefix, headers_json, updated_at_ns)
		VALUES (?, ?, ?)
		ON CONFLICT(url_prefix) DO UPDATE SET
			headers_json  = excluded.headers_json,
			updated_at_ns = excluded.updated_at_ns
	`, rule.URLPrefix, rule.HeadersJSON, rule.UpdatedAtNs)
	return err
}

// DeleteAccountHeaderRule removes a rule by url_prefix.
func (r *StateRepo) DeleteAccountHeaderRule(prefix string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, err := r.db.Exec("DELETE FROM account_header_rules WHERE url_prefix = ?", prefix)
	return err
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
