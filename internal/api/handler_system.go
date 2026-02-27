package api

import (
	"net/http"
	"sync/atomic"

	"github.com/Resinat/Resin/internal/config"
	"github.com/Resinat/Resin/internal/service"
)

type systemEnvConfigResponse struct {
	CacheDir                                        string          `json:"cache_dir"`
	StateDir                                        string          `json:"state_dir"`
	LogDir                                          string          `json:"log_dir"`
	ListenAddress                                   string          `json:"listen_address"`
	ResinPort                                       int             `json:"resin_port"`
	APIMaxBodyBytes                                 int             `json:"api_max_body_bytes"`
	MaxLatencyTableEntries                          int             `json:"max_latency_table_entries"`
	ProbeConcurrency                                int             `json:"probe_concurrency"`
	GeoIPUpdateSchedule                             string          `json:"geoip_update_schedule"`
	DefaultPlatformStickyTTL                        config.Duration `json:"default_platform_sticky_ttl"`
	DefaultPlatformRegexFilters                     []string        `json:"default_platform_regex_filters"`
	DefaultPlatformRegionFilters                    []string        `json:"default_platform_region_filters"`
	DefaultPlatformReverseProxyMissAction           string          `json:"default_platform_reverse_proxy_miss_action"`
	DefaultPlatformReverseProxyEmptyAccountBehavior string          `json:"default_platform_reverse_proxy_empty_account_behavior"`
	DefaultPlatformReverseProxyFixedAccountHeader   string          `json:"default_platform_reverse_proxy_fixed_account_header"`
	DefaultPlatformAllocationPolicy                 string          `json:"default_platform_allocation_policy"`
	ProbeTimeout                                    config.Duration `json:"probe_timeout"`
	ResourceFetchTimeout                            config.Duration `json:"resource_fetch_timeout"`
	ProxyTransportMaxIdleConns                      int             `json:"proxy_transport_max_idle_conns"`
	ProxyTransportMaxIdleConnsPerHost               int             `json:"proxy_transport_max_idle_conns_per_host"`
	ProxyTransportIdleConnTimeout                   config.Duration `json:"proxy_transport_idle_conn_timeout"`
	RequestLogQueueSize                             int             `json:"request_log_queue_size"`
	RequestLogQueueFlushBatchSize                   int             `json:"request_log_queue_flush_batch_size"`
	RequestLogQueueFlushInterval                    config.Duration `json:"request_log_queue_flush_interval"`
	RequestLogDBMaxMB                               int             `json:"request_log_db_max_mb"`
	RequestLogDBRetainCount                         int             `json:"request_log_db_retain_count"`
	MetricThroughputIntervalSeconds                 int             `json:"metric_throughput_interval_seconds"`
	MetricThroughputRetentionSeconds                int             `json:"metric_throughput_retention_seconds"`
	MetricBucketSeconds                             int             `json:"metric_bucket_seconds"`
	MetricConnectionsIntervalSeconds                int             `json:"metric_connections_interval_seconds"`
	MetricConnectionsRetentionSeconds               int             `json:"metric_connections_retention_seconds"`
	MetricLeasesIntervalSeconds                     int             `json:"metric_leases_interval_seconds"`
	MetricLeasesRetentionSeconds                    int             `json:"metric_leases_retention_seconds"`
	MetricLatencyBinWidthMS                         int             `json:"metric_latency_bin_width_ms"`
	MetricLatencyBinOverflowMS                      int             `json:"metric_latency_bin_overflow_ms"`
	AdminTokenSet                                   bool            `json:"admin_token_set"`
	ProxyTokenSet                                   bool            `json:"proxy_token_set"`
	AdminTokenWeak                                  bool            `json:"admin_token_weak"`
	ProxyTokenWeak                                  bool            `json:"proxy_token_weak"`
}

// HandleSystemInfo returns a handler for GET /api/v1/system/info.
func HandleSystemInfo(info service.SystemInfo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, info)
	}
}

// HandleSystemConfig returns a handler for GET /api/v1/system/config.
func HandleSystemConfig(runtimeCfg *atomic.Pointer[config.RuntimeConfig]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if runtimeCfg == nil {
			WriteJSON(w, http.StatusOK, nil)
			return
		}
		WriteJSON(w, http.StatusOK, runtimeCfg.Load())
	}
}

// HandleSystemDefaultConfig returns a handler for GET /api/v1/system/config/default.
func HandleSystemDefaultConfig() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, config.NewDefaultRuntimeConfig())
	}
}

// HandleSystemEnvConfig returns a handler for GET /api/v1/system/config/env.
func HandleSystemEnvConfig(envCfg *config.EnvConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, systemEnvConfigSnapshot(envCfg))
	}
}

// HandlePatchSystemConfig returns a handler for PATCH /api/v1/system/config.
func HandlePatchSystemConfig(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := readRawBodyOrWriteInvalid(w, r)
		if !ok {
			return
		}
		result, err := cp.PatchRuntimeConfig(body)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, result)
	}
}

func systemEnvConfigSnapshot(envCfg *config.EnvConfig) *systemEnvConfigResponse {
	if envCfg == nil {
		return nil
	}
	adminTokenSet := envCfg.AdminToken != ""
	proxyTokenSet := envCfg.ProxyToken != ""
	return &systemEnvConfigResponse{
		CacheDir:                              envCfg.CacheDir,
		StateDir:                              envCfg.StateDir,
		LogDir:                                envCfg.LogDir,
		ListenAddress:                         envCfg.ListenAddress,
		ResinPort:                             envCfg.ResinPort,
		APIMaxBodyBytes:                       envCfg.APIMaxBodyBytes,
		MaxLatencyTableEntries:                envCfg.MaxLatencyTableEntries,
		ProbeConcurrency:                      envCfg.ProbeConcurrency,
		GeoIPUpdateSchedule:                   envCfg.GeoIPUpdateSchedule,
		DefaultPlatformStickyTTL:              config.Duration(envCfg.DefaultPlatformStickyTTL),
		DefaultPlatformRegexFilters:           append([]string(nil), envCfg.DefaultPlatformRegexFilters...),
		DefaultPlatformRegionFilters:          append([]string(nil), envCfg.DefaultPlatformRegionFilters...),
		DefaultPlatformReverseProxyMissAction: envCfg.DefaultPlatformReverseProxyMissAction,
		DefaultPlatformReverseProxyEmptyAccountBehavior: envCfg.DefaultPlatformReverseProxyEmptyAccountBehavior,
		DefaultPlatformReverseProxyFixedAccountHeader:   envCfg.DefaultPlatformReverseProxyFixedAccountHeader,
		DefaultPlatformAllocationPolicy:                 envCfg.DefaultPlatformAllocationPolicy,
		ProbeTimeout:                                    config.Duration(envCfg.ProbeTimeout),
		ResourceFetchTimeout:                            config.Duration(envCfg.ResourceFetchTimeout),
		ProxyTransportMaxIdleConns:                      envCfg.ProxyTransportMaxIdleConns,
		ProxyTransportMaxIdleConnsPerHost:               envCfg.ProxyTransportMaxIdleConnsPerHost,
		ProxyTransportIdleConnTimeout:                   config.Duration(envCfg.ProxyTransportIdleConnTimeout),
		RequestLogQueueSize:                             envCfg.RequestLogQueueSize,
		RequestLogQueueFlushBatchSize:                   envCfg.RequestLogQueueFlushBatchSize,
		RequestLogQueueFlushInterval:                    config.Duration(envCfg.RequestLogQueueFlushInterval),
		RequestLogDBMaxMB:                               envCfg.RequestLogDBMaxMB,
		RequestLogDBRetainCount:                         envCfg.RequestLogDBRetainCount,
		MetricThroughputIntervalSeconds:                 envCfg.MetricThroughputIntervalSeconds,
		MetricThroughputRetentionSeconds:                envCfg.MetricThroughputRetentionSeconds,
		MetricBucketSeconds:                             envCfg.MetricBucketSeconds,
		MetricConnectionsIntervalSeconds:                envCfg.MetricConnectionsIntervalSeconds,
		MetricConnectionsRetentionSeconds:               envCfg.MetricConnectionsRetentionSeconds,
		MetricLeasesIntervalSeconds:                     envCfg.MetricLeasesIntervalSeconds,
		MetricLeasesRetentionSeconds:                    envCfg.MetricLeasesRetentionSeconds,
		MetricLatencyBinWidthMS:                         envCfg.MetricLatencyBinWidthMS,
		MetricLatencyBinOverflowMS:                      envCfg.MetricLatencyBinOverflowMS,
		AdminTokenSet:                                   adminTokenSet,
		ProxyTokenSet:                                   proxyTokenSet,
		AdminTokenWeak:                                  adminTokenSet && config.IsWeakToken(envCfg.AdminToken),
		ProxyTokenWeak:                                  proxyTokenSet && config.IsWeakToken(envCfg.ProxyToken),
	}
}
