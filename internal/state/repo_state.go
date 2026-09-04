package state

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Resinat/Resin/internal/config"
	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/platform"
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

func encodeStringSliceJSON(values []string) (string, error) {
	if values == nil {
		values = []string{}
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func decodeStringSliceJSON(raw string) ([]string, error) {
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
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
	p.Name = platform.NormalizePlatformName(p.Name)
	if err := platform.ValidatePlatformName(p.Name); err != nil {
		return fmt.Errorf("platform name: %w", err)
	}

	// Validate strongly-typed filters before persistence.
	if _, err := platform.CompileRegexFilters(p.RegexFilters); err != nil {
		return err
	}
	if err := platform.ValidateRegionFilters(p.RegionFilters); err != nil {
		return err
	}
	missAction := platform.NormalizeReverseProxyMissAction(p.ReverseProxyMissAction)
	if missAction == "" {
		return fmt.Errorf("reverse_proxy_miss_action: invalid value %q", p.ReverseProxyMissAction)
	}
	p.ReverseProxyMissAction = string(missAction)
	if !platform.AllocationPolicy(p.AllocationPolicy).IsValid() {
		return fmt.Errorf("allocation_policy: invalid value %q", p.AllocationPolicy)
	}
	behavior := platform.ReverseProxyEmptyAccountBehavior(strings.TrimSpace(p.ReverseProxyEmptyAccountBehavior))
	if behavior == "" {
		behavior = platform.ReverseProxyEmptyAccountBehaviorRandom
	}
	if !behavior.IsValid() {
		return fmt.Errorf("reverse_proxy_empty_account_behavior: invalid value %q", p.ReverseProxyEmptyAccountBehavior)
	}
	p.ReverseProxyEmptyAccountBehavior = string(behavior)
	normalizedFixedHeaders, fixedHeaders, err := platform.NormalizeFixedAccountHeaders(p.ReverseProxyFixedAccountHeader)
	if err != nil {
		return fmt.Errorf("reverse_proxy_fixed_account_header: %w", err)
	}
	p.ReverseProxyFixedAccountHeader = normalizedFixedHeaders
	if behavior == platform.ReverseProxyEmptyAccountBehaviorFixedHeader && len(fixedHeaders) == 0 {
		return fmt.Errorf(
			"reverse_proxy_fixed_account_header: required when reverse_proxy_empty_account_behavior is %s",
			platform.ReverseProxyEmptyAccountBehaviorFixedHeader,
		)
	}
	regexFiltersJSON, err := encodeStringSliceJSON(p.RegexFilters)
	if err != nil {
		return fmt.Errorf("encode platform %s regex_filters: %w", p.ID, err)
	}
	regionFiltersJSON, err := encodeStringSliceJSON(p.RegionFilters)
	if err != nil {
		return fmt.Errorf("encode platform %s region_filters: %w", p.ID, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	_, err = r.db.Exec(`
		INSERT INTO platforms (id, name, sticky_ttl_ns, regex_filters_json, region_filters_json,
		                       reverse_proxy_miss_action, reverse_proxy_empty_account_behavior,
		                       reverse_proxy_fixed_account_header, allocation_policy,
		                       passive_circuit_breaker_disabled, updated_at_ns)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name                     = excluded.name,
			sticky_ttl_ns            = excluded.sticky_ttl_ns,
			regex_filters_json       = excluded.regex_filters_json,
			region_filters_json      = excluded.region_filters_json,
			reverse_proxy_miss_action = excluded.reverse_proxy_miss_action,
			reverse_proxy_empty_account_behavior = excluded.reverse_proxy_empty_account_behavior,
			reverse_proxy_fixed_account_header   = excluded.reverse_proxy_fixed_account_header,
			allocation_policy        = excluded.allocation_policy,
			passive_circuit_breaker_disabled = excluded.passive_circuit_breaker_disabled,
			updated_at_ns            = excluded.updated_at_ns
	`, p.ID, p.Name, p.StickyTTLNs, regexFiltersJSON, regionFiltersJSON,
		p.ReverseProxyMissAction, p.ReverseProxyEmptyAccountBehavior, p.ReverseProxyFixedAccountHeader,
		p.AllocationPolicy, p.PassiveCircuitBreakerDisabled, p.UpdatedAtNs)
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

// GetPlatformName returns platform name by ID without decoding filter columns.
func (r *StateRepo) GetPlatformName(id string) (string, error) {
	row := r.db.QueryRow(`SELECT name FROM platforms WHERE id = ?`, id)
	var name string
	if err := row.Scan(&name); err != nil {
		if err == sql.ErrNoRows {
			return "", ErrNotFound
		}
		return "", err
	}
	return name, nil
}

// GetPlatform returns one platform by ID.
func (r *StateRepo) GetPlatform(id string) (*model.Platform, error) {
	row := r.db.QueryRow(`SELECT id, name, sticky_ttl_ns, regex_filters_json, region_filters_json,
			reverse_proxy_miss_action, reverse_proxy_empty_account_behavior,
			reverse_proxy_fixed_account_header, allocation_policy,
			passive_circuit_breaker_disabled, updated_at_ns
			FROM platforms WHERE id = ?`, id)

	var p model.Platform
	var regexFiltersJSON, regionFiltersJSON string
	var passiveCircuitBreakerDisabled int
	if err := row.Scan(&p.ID, &p.Name, &p.StickyTTLNs, &regexFiltersJSON,
		&regionFiltersJSON, &p.ReverseProxyMissAction, &p.ReverseProxyEmptyAccountBehavior,
		&p.ReverseProxyFixedAccountHeader, &p.AllocationPolicy, &passiveCircuitBreakerDisabled, &p.UpdatedAtNs); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.PassiveCircuitBreakerDisabled = passiveCircuitBreakerDisabled != 0
	regexFilters, err := decodeStringSliceJSON(regexFiltersJSON)
	if err != nil {
		return nil, fmt.Errorf("decode platform %s regex_filters_json: %w", p.ID, err)
	}
	regionFilters, err := decodeStringSliceJSON(regionFiltersJSON)
	if err != nil {
		return nil, fmt.Errorf("decode platform %s region_filters_json: %w", p.ID, err)
	}
	p.RegexFilters = regexFilters
	p.RegionFilters = regionFilters
	return &p, nil
}

// ListPlatforms returns all platforms.
func (r *StateRepo) ListPlatforms() ([]model.Platform, error) {
	rows, err := r.db.Query("SELECT id, name, sticky_ttl_ns, regex_filters_json, region_filters_json, reverse_proxy_miss_action, reverse_proxy_empty_account_behavior, reverse_proxy_fixed_account_header, allocation_policy, passive_circuit_breaker_disabled, updated_at_ns FROM platforms")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []model.Platform
	for rows.Next() {
		var p model.Platform
		var regexFiltersJSON, regionFiltersJSON string
		var passiveCircuitBreakerDisabled int
		if err := rows.Scan(&p.ID, &p.Name, &p.StickyTTLNs, &regexFiltersJSON,
			&regionFiltersJSON, &p.ReverseProxyMissAction, &p.ReverseProxyEmptyAccountBehavior,
			&p.ReverseProxyFixedAccountHeader, &p.AllocationPolicy, &passiveCircuitBreakerDisabled, &p.UpdatedAtNs); err != nil {
			return nil, err
		}
		p.PassiveCircuitBreakerDisabled = passiveCircuitBreakerDisabled != 0
		regexFilters, err := decodeStringSliceJSON(regexFiltersJSON)
		if err != nil {
			return nil, fmt.Errorf("decode platform %s regex_filters_json: %w", p.ID, err)
		}
		regionFilters, err := decodeStringSliceJSON(regionFiltersJSON)
		if err != nil {
			return nil, fmt.Errorf("decode platform %s region_filters_json: %w", p.ID, err)
		}
		p.RegexFilters = regexFilters
		p.RegionFilters = regionFilters
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
	if s.SourceType == "" {
		s.SourceType = "remote"
	}
	if s.SourceType != "remote" && s.SourceType != "local" {
		return fmt.Errorf("source_type: must be remote or local, got %q", s.SourceType)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	_, err := r.db.Exec(`
			INSERT INTO subscriptions (id, name, source_type, url, content, update_interval_ns, enabled,
			                           ephemeral, incremental_alive_nodes, ephemeral_node_evict_delay_ns,
			                           public_token,
			                           usage_upload_bytes, usage_download_bytes, usage_total_bytes,
			                           usage_expire_unix, usage_updated_at_ns, created_at_ns, updated_at_ns)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				name               = excluded.name,
				source_type        = excluded.source_type,
				url                = excluded.url,
				content            = excluded.content,
				update_interval_ns = excluded.update_interval_ns,
				enabled            = excluded.enabled,
				ephemeral          = excluded.ephemeral,
				incremental_alive_nodes = excluded.incremental_alive_nodes,
				ephemeral_node_evict_delay_ns = excluded.ephemeral_node_evict_delay_ns,
				public_token        = CASE WHEN excluded.public_token <> '' THEN excluded.public_token ELSE subscriptions.public_token END,
				usage_upload_bytes = excluded.usage_upload_bytes,
				usage_download_bytes = excluded.usage_download_bytes,
				usage_total_bytes = excluded.usage_total_bytes,
				usage_expire_unix = excluded.usage_expire_unix,
				usage_updated_at_ns = excluded.usage_updated_at_ns,
				updated_at_ns      = excluded.updated_at_ns
		`, s.ID, s.Name, s.SourceType, s.URL, s.Content, s.UpdateIntervalNs, s.Enabled,
		s.Ephemeral, s.IncrementalAliveNodes, s.EphemeralNodeEvictDelayNs, s.PublicToken,
		s.UsageUploadBytes, s.UsageDownloadBytes, s.UsageTotalBytes,
		s.UsageExpireUnix, s.UsageUpdatedAtNs, s.CreatedAtNs, s.UpdatedAtNs)
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
	rows, err := r.db.Query(`SELECT id, name, source_type, url, content, update_interval_ns, enabled,
		ephemeral, incremental_alive_nodes, ephemeral_node_evict_delay_ns, public_token,
		usage_upload_bytes, usage_download_bytes, usage_total_bytes, usage_expire_unix, usage_updated_at_ns,
		created_at_ns, updated_at_ns FROM subscriptions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []model.Subscription
	for rows.Next() {
		var s model.Subscription
		if err := rows.Scan(&s.ID, &s.Name, &s.SourceType, &s.URL, &s.Content, &s.UpdateIntervalNs, &s.Enabled,
			&s.Ephemeral, &s.IncrementalAliveNodes, &s.EphemeralNodeEvictDelayNs, &s.PublicToken,
			&s.UsageUploadBytes, &s.UsageDownloadBytes, &s.UsageTotalBytes, &s.UsageExpireUnix, &s.UsageUpdatedAtNs,
			&s.CreatedAtNs, &s.UpdatedAtNs); err != nil {
			return nil, err
		}
		if s.SourceType == "" {
			s.SourceType = "remote"
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

// --- endpoints ---

// InsertEndpoint persists a new custom endpoint.
func (r *StateRepo) InsertEndpoint(endpoint model.Endpoint) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, err := r.db.Exec(`
		INSERT INTO endpoints (
			id, port, enabled, allow_management, allow_proxy, require_proxy_auth_info,
			allow_http_forward, allow_http_reverse, allow_socks5, created_at_ns, updated_at_ns
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, endpoint.ID, endpoint.Port, endpoint.Enabled, endpoint.AllowManagement, endpoint.AllowProxy,
		endpoint.RequireProxyAuthInfo, endpoint.AllowHTTPForward, endpoint.AllowHTTPReverse,
		endpoint.AllowSOCKS5, endpoint.CreatedAtNs, endpoint.UpdatedAtNs)
	if isSQLiteUniqueConstraint(err) {
		return fmt.Errorf("%w: endpoint id or port already exists", ErrConflict)
	}
	return err
}

// UpdateEndpoint replaces the mutable fields of an existing custom endpoint.
func (r *StateRepo) UpdateEndpoint(endpoint model.Endpoint) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec(`
		UPDATE endpoints SET
			port = ?,
			enabled = ?,
			allow_management = ?,
			allow_proxy = ?,
			require_proxy_auth_info = ?,
			allow_http_forward = ?,
			allow_http_reverse = ?,
			allow_socks5 = ?,
			updated_at_ns = ?
		WHERE id = ?
	`, endpoint.Port, endpoint.Enabled, endpoint.AllowManagement, endpoint.AllowProxy,
		endpoint.RequireProxyAuthInfo, endpoint.AllowHTTPForward, endpoint.AllowHTTPReverse,
		endpoint.AllowSOCKS5, endpoint.UpdatedAtNs, endpoint.ID)
	if isSQLiteUniqueConstraint(err) {
		return fmt.Errorf("%w: endpoint port already exists", ErrConflict)
	}
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteEndpoint removes a custom endpoint by ID.
func (r *StateRepo) DeleteEndpoint(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec("DELETE FROM endpoints WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetEndpoint returns one persisted custom endpoint.
func (r *StateRepo) GetEndpoint(id string) (*model.Endpoint, error) {
	row := r.db.QueryRow(`
		SELECT id, port, enabled, allow_management, allow_proxy, require_proxy_auth_info,
		       allow_http_forward, allow_http_reverse, allow_socks5, created_at_ns, updated_at_ns
		FROM endpoints WHERE id = ?
	`, id)
	endpoint, err := scanEndpoint(row.Scan)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &endpoint, nil
}

// ListEndpoints returns all persisted custom endpoints ordered by port.
func (r *StateRepo) ListEndpoints() ([]model.Endpoint, error) {
	rows, err := r.db.Query(`
		SELECT id, port, enabled, allow_management, allow_proxy, require_proxy_auth_info,
		       allow_http_forward, allow_http_reverse, allow_socks5, created_at_ns, updated_at_ns
		FROM endpoints ORDER BY port ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]model.Endpoint, 0)
	for rows.Next() {
		endpoint, scanErr := scanEndpoint(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, endpoint)
	}
	return result, rows.Err()
}

type endpointScanner func(dest ...any) error

func scanEndpoint(scan endpointScanner) (model.Endpoint, error) {
	var endpoint model.Endpoint
	var enabled, allowManagement, allowProxy, requireProxyAuthInfo int
	var allowHTTPForward, allowHTTPReverse, allowSOCKS5 int
	err := scan(
		&endpoint.ID,
		&endpoint.Port,
		&enabled,
		&allowManagement,
		&allowProxy,
		&requireProxyAuthInfo,
		&allowHTTPForward,
		&allowHTTPReverse,
		&allowSOCKS5,
		&endpoint.CreatedAtNs,
		&endpoint.UpdatedAtNs,
	)
	if err != nil {
		return model.Endpoint{}, err
	}
	endpoint.Enabled = enabled != 0
	endpoint.AllowManagement = allowManagement != 0
	endpoint.AllowProxy = allowProxy != 0
	endpoint.RequireProxyAuthInfo = requireProxyAuthInfo != 0
	endpoint.AllowHTTPForward = allowHTTPForward != 0
	endpoint.AllowHTTPReverse = allowHTTPReverse != 0
	endpoint.AllowSOCKS5 = allowSOCKS5 != 0
	return endpoint, nil
}

// --- account_header_rules ---

// EnsureAccountHeaderRule inserts a rule by url_prefix only when it does not
// already exist and reports whether the row was newly created.
func (r *StateRepo) EnsureAccountHeaderRule(rule model.AccountHeaderRule) (bool, error) {
	headersJSON, err := encodeStringSliceJSON(rule.Headers)
	if err != nil {
		return false, fmt.Errorf("encode account header rule %q headers: %w", rule.URLPrefix, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.db.Exec(`
		INSERT INTO account_header_rules (url_prefix, headers_json, updated_at_ns)
		VALUES (?, ?, ?)
		ON CONFLICT(url_prefix) DO NOTHING
	`, rule.URLPrefix, headersJSON, rule.UpdatedAtNs)
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}

// UpsertAccountHeaderRuleWithCreated inserts or updates a rule by url_prefix and
// reports whether the row was newly created.
func (r *StateRepo) UpsertAccountHeaderRuleWithCreated(rule model.AccountHeaderRule) (bool, error) {
	headersJSON, err := encodeStringSliceJSON(rule.Headers)
	if err != nil {
		return false, fmt.Errorf("encode account header rule %q headers: %w", rule.URLPrefix, err)
	}

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
	`, rule.URLPrefix, headersJSON, rule.UpdatedAtNs)
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
			`, headersJSON, rule.UpdatedAtNs, rule.URLPrefix); err != nil {
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
		var headersJSON string
		if err := rows.Scan(&rule.URLPrefix, &headersJSON, &rule.UpdatedAtNs); err != nil {
			return nil, err
		}
		headers, err := decodeStringSliceJSON(headersJSON)
		if err != nil {
			return nil, fmt.Errorf("decode account header rule %q headers_json: %w", rule.URLPrefix, err)
		}
		rule.Headers = headers
		result = append(result, rule)
	}
	return result, rows.Err()
}
