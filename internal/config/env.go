// Package config handles environment-based configuration loading and runtime config models.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// EnvConfig holds all environment-variable-driven settings (not hot-updatable).
type EnvConfig struct {
	// Directories
	CacheDir string
	StateDir string
	LogDir   string

	// Ports
	APIPort          int
	ForwardProxyPort int
	ReverseProxyPort int
	APIMaxBodyBytes  int

	// Core
	MaxLatencyTableEntries                int
	ProbeConcurrency                      int
	GeoIPUpdateSchedule                   string
	DefaultPlatformStickyTTL              time.Duration
	DefaultPlatformRegexFilters           []string
	DefaultPlatformRegionFilters          []string
	DefaultPlatformReverseProxyMissAction string
	DefaultPlatformAllocationPolicy       string
	ProbeTimeout                          time.Duration
	ResourceFetchTimeout                  time.Duration

	// Request log
	RequestLogQueueSize           int
	RequestLogQueueFlushBatchSize int
	RequestLogQueueFlushInterval  time.Duration
	RequestLogDBMaxMB             int
	RequestLogDBRetainCount       int

	// Auth
	AdminToken string
	ProxyToken string

	// Metrics
	MetricThroughputIntervalSeconds   int
	MetricThroughputRetentionSeconds  int
	MetricBucketSeconds               int
	MetricConnectionsIntervalSeconds  int
	MetricConnectionsRetentionSeconds int
	MetricLeasesIntervalSeconds       int
	MetricLeasesRetentionSeconds      int
	MetricLatencyBinWidthMS           int
	MetricLatencyBinOverflowMS        int
}

// LoadEnvConfig reads environment variables and returns a validated EnvConfig.
// Returns an error if any required variable is missing or any value is invalid.
func LoadEnvConfig() (*EnvConfig, error) {
	cfg := &EnvConfig{}
	var errs []string

	// --- Directories ---
	cfg.CacheDir = envStr("RESIN_CACHE_DIR", "/var/cache/resin")
	cfg.StateDir = envStr("RESIN_STATE_DIR", "/var/lib/resin")
	cfg.LogDir = envStr("RESIN_LOG_DIR", "/var/log/resin")

	// --- Ports ---
	cfg.APIPort = envInt("RESIN_API_PORT", 2620, &errs)
	cfg.ForwardProxyPort = envInt("RESIN_FORWARD_PROXY_PORT", 2621, &errs)
	cfg.ReverseProxyPort = envInt("RESIN_REVERSE_PROXY_PORT", 2622, &errs)
	cfg.APIMaxBodyBytes = envInt("RESIN_API_MAX_BODY_BYTES", 1<<20, &errs)

	// --- Core ---
	cfg.MaxLatencyTableEntries = envInt("RESIN_MAX_LATENCY_TABLE_ENTRIES", 128, &errs)
	cfg.ProbeConcurrency = envInt("RESIN_PROBE_CONCURRENCY", 1000, &errs)
	cfg.GeoIPUpdateSchedule = envStr("RESIN_GEOIP_UPDATE_SCHEDULE", "0 5 12 * *")
	cfg.DefaultPlatformStickyTTL = envDuration("RESIN_DEFAULT_PLATFORM_STICKY_TTL", 7*24*time.Hour, &errs)
	cfg.DefaultPlatformRegexFilters = envStringSlice("RESIN_DEFAULT_PLATFORM_REGEX_FILTERS", []string{}, &errs)
	cfg.DefaultPlatformRegionFilters = envStringSlice("RESIN_DEFAULT_PLATFORM_REGION_FILTERS", []string{}, &errs)
	cfg.DefaultPlatformReverseProxyMissAction = envStr("RESIN_DEFAULT_PLATFORM_REVERSE_PROXY_MISS_ACTION", "RANDOM")
	cfg.DefaultPlatformAllocationPolicy = envStr("RESIN_DEFAULT_PLATFORM_ALLOCATION_POLICY", "BALANCED")
	cfg.ProbeTimeout = envDuration("RESIN_PROBE_TIMEOUT", 15*time.Second, &errs)
	cfg.ResourceFetchTimeout = envDuration("RESIN_RESOURCE_FETCH_TIMEOUT", 30*time.Second, &errs)

	// --- Request log ---
	cfg.RequestLogQueueSize = envInt("RESIN_REQUEST_LOG_QUEUE_SIZE", 8192, &errs)
	cfg.RequestLogQueueFlushBatchSize = envInt("RESIN_REQUEST_LOG_QUEUE_FLUSH_BATCH_SIZE", 4096, &errs)
	cfg.RequestLogQueueFlushInterval = envDuration("RESIN_REQUEST_LOG_QUEUE_FLUSH_INTERVAL", 5*time.Minute, &errs)
	cfg.RequestLogDBMaxMB = envInt("RESIN_REQUEST_LOG_DB_MAX_MB", 512, &errs)
	cfg.RequestLogDBRetainCount = envInt("RESIN_REQUEST_LOG_DB_RETAIN_COUNT", 5, &errs)

	// --- Auth (required) ---
	cfg.AdminToken = envStr("RESIN_ADMIN_TOKEN", "")
	cfg.ProxyToken = envStr("RESIN_PROXY_TOKEN", "")

	// --- Metrics ---
	cfg.MetricThroughputIntervalSeconds = envInt("RESIN_METRIC_THROUGHPUT_INTERVAL_SECONDS", 1, &errs)
	cfg.MetricThroughputRetentionSeconds = envInt("RESIN_METRIC_THROUGHPUT_RETENTION_SECONDS", 3600, &errs)
	cfg.MetricBucketSeconds = envInt("RESIN_METRIC_BUCKET_SECONDS", 3600, &errs)
	cfg.MetricConnectionsIntervalSeconds = envInt("RESIN_METRIC_CONNECTIONS_INTERVAL_SECONDS", 5, &errs)
	cfg.MetricConnectionsRetentionSeconds = envInt("RESIN_METRIC_CONNECTIONS_RETENTION_SECONDS", 18000, &errs)
	cfg.MetricLeasesIntervalSeconds = envInt("RESIN_METRIC_LEASES_INTERVAL_SECONDS", 5, &errs)
	cfg.MetricLeasesRetentionSeconds = envInt("RESIN_METRIC_LEASES_RETENTION_SECONDS", 18000, &errs)
	cfg.MetricLatencyBinWidthMS = envInt("RESIN_METRIC_LATENCY_BIN_WIDTH_MS", 100, &errs)
	cfg.MetricLatencyBinOverflowMS = envInt("RESIN_METRIC_LATENCY_BIN_OVERFLOW_MS", 3000, &errs)

	// --- Validation ---
	if cfg.AdminToken == "" {
		errs = append(errs, "RESIN_ADMIN_TOKEN is required")
	}
	if cfg.ProxyToken == "" {
		errs = append(errs, "RESIN_PROXY_TOKEN is required")
	} else {
		if strings.Contains(cfg.ProxyToken, ":") || strings.Contains(cfg.ProxyToken, "@") {
			errs = append(errs, "RESIN_PROXY_TOKEN must not contain ':' or '@'")
		}
	}

	validatePort("RESIN_API_PORT", cfg.APIPort, &errs)
	validatePort("RESIN_FORWARD_PROXY_PORT", cfg.ForwardProxyPort, &errs)
	validatePort("RESIN_REVERSE_PROXY_PORT", cfg.ReverseProxyPort, &errs)
	validatePositive("RESIN_API_MAX_BODY_BYTES", cfg.APIMaxBodyBytes, &errs)

	validatePositive("RESIN_MAX_LATENCY_TABLE_ENTRIES", cfg.MaxLatencyTableEntries, &errs)
	validatePositive("RESIN_PROBE_CONCURRENCY", cfg.ProbeConcurrency, &errs)
	if _, err := cron.ParseStandard(cfg.GeoIPUpdateSchedule); err != nil {
		errs = append(errs, fmt.Sprintf("RESIN_GEOIP_UPDATE_SCHEDULE: invalid cron expression %q: %v", cfg.GeoIPUpdateSchedule, err))
	}
	if cfg.DefaultPlatformStickyTTL <= 0 {
		errs = append(errs, "RESIN_DEFAULT_PLATFORM_STICKY_TTL must be positive")
	}
	for _, pattern := range cfg.DefaultPlatformRegexFilters {
		if _, err := regexp.Compile(pattern); err != nil {
			errs = append(errs, fmt.Sprintf("RESIN_DEFAULT_PLATFORM_REGEX_FILTERS: invalid regex %q: %v", pattern, err))
		}
	}
	for _, region := range cfg.DefaultPlatformRegionFilters {
		if !isLowerAlpha2(region) {
			errs = append(errs, fmt.Sprintf("RESIN_DEFAULT_PLATFORM_REGION_FILTERS: invalid region %q (must be lowercase ISO 3166-1 alpha-2)", region))
		}
	}
	switch cfg.DefaultPlatformReverseProxyMissAction {
	case "RANDOM", "REJECT":
	default:
		errs = append(errs, fmt.Sprintf("RESIN_DEFAULT_PLATFORM_REVERSE_PROXY_MISS_ACTION: invalid value %q (allowed: RANDOM, REJECT)", cfg.DefaultPlatformReverseProxyMissAction))
	}
	switch cfg.DefaultPlatformAllocationPolicy {
	case "BALANCED", "PREFER_LOW_LATENCY", "PREFER_IDLE_IP":
	default:
		errs = append(errs, fmt.Sprintf("RESIN_DEFAULT_PLATFORM_ALLOCATION_POLICY: invalid value %q (allowed: BALANCED, PREFER_LOW_LATENCY, PREFER_IDLE_IP)", cfg.DefaultPlatformAllocationPolicy))
	}
	if cfg.ProbeTimeout <= 0 {
		errs = append(errs, "RESIN_PROBE_TIMEOUT must be positive")
	}
	if cfg.ResourceFetchTimeout <= 0 {
		errs = append(errs, "RESIN_RESOURCE_FETCH_TIMEOUT must be positive")
	}
	validatePositive("RESIN_REQUEST_LOG_QUEUE_SIZE", cfg.RequestLogQueueSize, &errs)
	validatePositive("RESIN_REQUEST_LOG_QUEUE_FLUSH_BATCH_SIZE", cfg.RequestLogQueueFlushBatchSize, &errs)
	validatePositive("RESIN_REQUEST_LOG_DB_MAX_MB", cfg.RequestLogDBMaxMB, &errs)
	validatePositive("RESIN_REQUEST_LOG_DB_RETAIN_COUNT", cfg.RequestLogDBRetainCount, &errs)
	validatePositive("RESIN_METRIC_THROUGHPUT_INTERVAL_SECONDS", cfg.MetricThroughputIntervalSeconds, &errs)
	validatePositive("RESIN_METRIC_THROUGHPUT_RETENTION_SECONDS", cfg.MetricThroughputRetentionSeconds, &errs)
	validatePositive("RESIN_METRIC_BUCKET_SECONDS", cfg.MetricBucketSeconds, &errs)
	validatePositive("RESIN_METRIC_CONNECTIONS_INTERVAL_SECONDS", cfg.MetricConnectionsIntervalSeconds, &errs)
	validatePositive("RESIN_METRIC_CONNECTIONS_RETENTION_SECONDS", cfg.MetricConnectionsRetentionSeconds, &errs)
	validatePositive("RESIN_METRIC_LEASES_INTERVAL_SECONDS", cfg.MetricLeasesIntervalSeconds, &errs)
	validatePositive("RESIN_METRIC_LEASES_RETENTION_SECONDS", cfg.MetricLeasesRetentionSeconds, &errs)
	validatePositive("RESIN_METRIC_LATENCY_BIN_WIDTH_MS", cfg.MetricLatencyBinWidthMS, &errs)
	validatePositive("RESIN_METRIC_LATENCY_BIN_OVERFLOW_MS", cfg.MetricLatencyBinOverflowMS, &errs)

	if cfg.RequestLogQueueFlushInterval <= 0 {
		errs = append(errs, "RESIN_REQUEST_LOG_QUEUE_FLUSH_INTERVAL must be positive")
	}

	// Queue size must be >= 2x batch size
	if cfg.RequestLogQueueSize < 2*cfg.RequestLogQueueFlushBatchSize {
		errs = append(errs, "RESIN_REQUEST_LOG_QUEUE_SIZE must be at least 2x RESIN_REQUEST_LOG_QUEUE_FLUSH_BATCH_SIZE")
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("config validation failed:\n  %s", strings.Join(errs, "\n  "))
	}

	return cfg, nil
}

// --- helpers ---

func envStr(key, defaultVal string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return defaultVal
}

func envInt(key string, defaultVal int, errs *[]string) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		*errs = append(*errs, fmt.Sprintf("%s: invalid integer %q", key, v))
		return defaultVal
	}
	return n
}

func envDuration(key string, defaultVal time.Duration, errs *[]string) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		*errs = append(*errs, fmt.Sprintf("%s: invalid duration %q", key, v))
		return defaultVal
	}
	return d
}

func envStringSlice(key string, defaultVal []string, errs *[]string) []string {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	var out []string
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		*errs = append(*errs, fmt.Sprintf("%s: invalid JSON string array %q", key, v))
		return defaultVal
	}
	if out == nil {
		return []string{}
	}
	return out
}

func validatePort(name string, value int, errs *[]string) {
	if value < 1 || value > 65535 {
		*errs = append(*errs, fmt.Sprintf("%s: port must be 1-65535, got %d", name, value))
	}
}

func validatePositive(name string, value int, errs *[]string) {
	if value <= 0 {
		*errs = append(*errs, fmt.Sprintf("%s: must be positive, got %d", name, value))
	}
}

func isLowerAlpha2(s string) bool {
	if len(s) != 2 {
		return false
	}
	for _, c := range s {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}
