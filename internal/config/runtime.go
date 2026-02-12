package config

import "time"

// DefaultPlatformConfig contains default settings for newly created platforms.
type DefaultPlatformConfig struct {
	StickyTTL            Duration `json:"sticky_ttl"`
	RegexFilters         []string `json:"regex_filters"`
	RegionFilters        []string `json:"region_filters"`
	ReverseProxyMissAction string `json:"reverse_proxy_miss_action"`
	AllocationPolicy     string   `json:"allocation_policy"`
}

// RuntimeConfig holds all hot-updatable global settings.
// These are persisted in the database and served via GET /system/config.
type RuntimeConfig struct {
	// Basic
	UserAgent string `json:"user_agent"`

	// Request log
	RequestLogEnabled                 bool `json:"request_log_enabled"`
	ReverseProxyLogDetailEnabled      bool `json:"reverse_proxy_log_detail_enabled"`
	ReverseProxyLogReqHeadersMaxBytes int  `json:"reverse_proxy_log_req_headers_max_bytes"`
	ReverseProxyLogReqBodyMaxBytes    int  `json:"reverse_proxy_log_req_body_max_bytes"`
	ReverseProxyLogRespHeadersMaxBytes int `json:"reverse_proxy_log_resp_headers_max_bytes"`
	ReverseProxyLogRespBodyMaxBytes   int  `json:"reverse_proxy_log_resp_body_max_bytes"`

	// Default platform
	DefaultPlatformConfig DefaultPlatformConfig `json:"default_platform_config"`

	// Health check
	MaxConsecutiveFailures         int      `json:"max_consecutive_failures"`
	MaxLatencyTestInterval         Duration `json:"max_latency_test_interval"`
	MaxAuthorityLatencyTestInterval Duration `json:"max_authority_latency_test_interval"`
	MaxEgressTestInterval          Duration `json:"max_egress_test_interval"`

	// GeoIP
	GeoIPUpdateSchedule string `json:"geoip_update_schedule"`

	// Probe
	LatencyTestURL           string   `json:"latency_test_url"`
	LatencyAuthorities       []string `json:"latency_authorities"`
	ProbeTimeout             Duration `json:"probe_timeout"`
	SubscriptionFetchTimeout Duration `json:"subscription_fetch_timeout"`

	// P2C
	P2CLatencyWindow   Duration `json:"p2c_latency_window"`
	LatencyDecayWindow Duration `json:"latency_decay_window"`

	// Persistence
	CacheFlushInterval        Duration `json:"cache_flush_interval"`
	CacheFlushDirtyThreshold  int      `json:"cache_flush_dirty_threshold"`
	EphemeralNodeEvictDelay   Duration `json:"ephemeral_node_evict_delay"`
}

// NewDefaultRuntimeConfig returns a RuntimeConfig populated with the default
// values specified in DESIGN.md §运行时全局设置项.
func NewDefaultRuntimeConfig() *RuntimeConfig {
	return &RuntimeConfig{
		UserAgent: "sing-box",

		RequestLogEnabled:                  false,
		ReverseProxyLogDetailEnabled:       false,
		ReverseProxyLogReqHeadersMaxBytes:  4096,
		ReverseProxyLogReqBodyMaxBytes:     1024,
		ReverseProxyLogRespHeadersMaxBytes: 1024,
		ReverseProxyLogRespBodyMaxBytes:    1024,

		DefaultPlatformConfig: DefaultPlatformConfig{
			StickyTTL:            Duration(7 * 24 * time.Hour), // 168h
			RegexFilters:         []string{},
			RegionFilters:        []string{},
			ReverseProxyMissAction: "RANDOM",
			AllocationPolicy:     "BALANCED",
		},

		MaxConsecutiveFailures:          3,
		MaxLatencyTestInterval:          Duration(5 * time.Minute),
		MaxAuthorityLatencyTestInterval: Duration(1 * time.Hour),
		MaxEgressTestInterval:           Duration(24 * time.Hour),

		GeoIPUpdateSchedule: "0 5 12 * *",

		LatencyTestURL:           "https://www.gstatic.com/generate_204",
		LatencyAuthorities:       []string{"gstatic.com", "google.com", "cloudflare.com", "github.com"},
		ProbeTimeout:             Duration(15 * time.Second),
		SubscriptionFetchTimeout: Duration(30 * time.Second),

		P2CLatencyWindow:   Duration(10 * time.Minute),
		LatencyDecayWindow: Duration(10 * time.Minute),

		CacheFlushInterval:       Duration(5 * time.Minute),
		CacheFlushDirtyThreshold: 1000,
		EphemeralNodeEvictDelay:  Duration(72 * time.Hour),
	}
}
