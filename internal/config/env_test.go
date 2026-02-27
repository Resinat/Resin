package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

// setEnvs sets multiple env vars and returns a cleanup function.
func setEnvs(t *testing.T, envs map[string]string) {
	t.Helper()
	for k, v := range envs {
		t.Setenv(k, v)
	}
}

// requiredEnvs returns the minimum env vars needed for LoadEnvConfig to succeed.
func requiredEnvs() map[string]string {
	return map[string]string{
		"RESIN_ADMIN_TOKEN": "admin-secret",
		"RESIN_PROXY_TOKEN": "proxy-secret",
	}
}

func TestLoadEnvConfig_Defaults(t *testing.T) {
	setEnvs(t, requiredEnvs())

	cfg, err := LoadEnvConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Directories
	assertEqual(t, "CacheDir", cfg.CacheDir, "/var/cache/resin")
	assertEqual(t, "StateDir", cfg.StateDir, "/var/lib/resin")
	assertEqual(t, "LogDir", cfg.LogDir, "/var/log/resin")
	assertEqual(t, "ListenAddress", cfg.ListenAddress, "0.0.0.0")

	// Ports
	assertEqual(t, "ResinPort", cfg.ResinPort, 2260)
	assertEqual(t, "APIMaxBodyBytes", cfg.APIMaxBodyBytes, 1<<20)

	// Core
	assertEqual(t, "MaxLatencyTableEntries", cfg.MaxLatencyTableEntries, 128)
	assertEqual(t, "ProbeConcurrency", cfg.ProbeConcurrency, 1000)
	assertEqual(t, "GeoIPUpdateSchedule", cfg.GeoIPUpdateSchedule, "0 7 * * *")
	assertEqual(t, "DefaultPlatformStickyTTL", cfg.DefaultPlatformStickyTTL, 7*24*time.Hour)
	assertEqual(t, "DefaultPlatformRegexFiltersLength", len(cfg.DefaultPlatformRegexFilters), 0)
	assertEqual(t, "DefaultPlatformRegionFiltersLength", len(cfg.DefaultPlatformRegionFilters), 0)
	assertEqual(t, "DefaultPlatformReverseProxyMissAction", cfg.DefaultPlatformReverseProxyMissAction, "RANDOM")
	assertEqual(
		t,
		"DefaultPlatformReverseProxyEmptyAccountBehavior",
		cfg.DefaultPlatformReverseProxyEmptyAccountBehavior,
		"ACCOUNT_HEADER_RULE",
	)
	assertEqual(
		t,
		"DefaultPlatformReverseProxyFixedAccountHeader",
		cfg.DefaultPlatformReverseProxyFixedAccountHeader,
		"Authorization",
	)
	assertEqual(t, "DefaultPlatformAllocationPolicy", cfg.DefaultPlatformAllocationPolicy, "BALANCED")
	assertEqual(t, "ProbeTimeout", cfg.ProbeTimeout, 15*time.Second)
	assertEqual(t, "ResourceFetchTimeout", cfg.ResourceFetchTimeout, 30*time.Second)
	assertEqual(t, "ProxyTransportMaxIdleConns", cfg.ProxyTransportMaxIdleConns, 1024)
	assertEqual(t, "ProxyTransportMaxIdleConnsPerHost", cfg.ProxyTransportMaxIdleConnsPerHost, 64)
	assertEqual(t, "ProxyTransportIdleConnTimeout", cfg.ProxyTransportIdleConnTimeout, 90*time.Second)

	// Request log
	assertEqual(t, "RequestLogQueueSize", cfg.RequestLogQueueSize, 8192)
	assertEqual(t, "RequestLogQueueFlushBatchSize", cfg.RequestLogQueueFlushBatchSize, 4096)
	assertEqual(t, "RequestLogDBMaxMB", cfg.RequestLogDBMaxMB, 512)
	assertEqual(t, "RequestLogDBRetainCount", cfg.RequestLogDBRetainCount, 5)

	// Metrics
	assertEqual(t, "MetricThroughputIntervalSeconds", cfg.MetricThroughputIntervalSeconds, 2)
	assertEqual(t, "MetricThroughputRetentionSeconds", cfg.MetricThroughputRetentionSeconds, 3600)
	assertEqual(t, "MetricBucketSeconds", cfg.MetricBucketSeconds, 3600)
	assertEqual(t, "MetricConnectionsIntervalSeconds", cfg.MetricConnectionsIntervalSeconds, 15)
	assertEqual(t, "MetricConnectionsRetentionSeconds", cfg.MetricConnectionsRetentionSeconds, 18000)
	assertEqual(t, "MetricLeasesIntervalSeconds", cfg.MetricLeasesIntervalSeconds, 5)
	assertEqual(t, "MetricLeasesRetentionSeconds", cfg.MetricLeasesRetentionSeconds, 18000)
	assertEqual(t, "MetricLatencyBinWidthMS", cfg.MetricLatencyBinWidthMS, 100)
	assertEqual(t, "MetricLatencyBinOverflowMS", cfg.MetricLatencyBinOverflowMS, 3000)
}

func TestLoadEnvConfig_EnvOverrides(t *testing.T) {
	envs := requiredEnvs()
	envs["RESIN_CACHE_DIR"] = "/tmp/cache"
	envs["RESIN_LISTEN_ADDRESS"] = "127.0.0.1"
	envs["RESIN_PORT"] = "8080"
	envs["RESIN_API_MAX_BODY_BYTES"] = "2097152"
	envs["RESIN_PROBE_CONCURRENCY"] = "500"
	envs["RESIN_GEOIP_UPDATE_SCHEDULE"] = "0 0 * * *"
	envs["RESIN_DEFAULT_PLATFORM_STICKY_TTL"] = "2h"
	envs["RESIN_DEFAULT_PLATFORM_REGEX_FILTERS"] = `["^Provider/.*"]`
	envs["RESIN_DEFAULT_PLATFORM_REGION_FILTERS"] = `["us","hk"]`
	envs["RESIN_DEFAULT_PLATFORM_REVERSE_PROXY_MISS_ACTION"] = "REJECT"
	envs["RESIN_DEFAULT_PLATFORM_REVERSE_PROXY_EMPTY_ACCOUNT_BEHAVIOR"] = "FIXED_HEADER"
	envs["RESIN_DEFAULT_PLATFORM_REVERSE_PROXY_FIXED_ACCOUNT_HEADER"] = "x-platform-account"
	envs["RESIN_DEFAULT_PLATFORM_ALLOCATION_POLICY"] = "PREFER_LOW_LATENCY"
	envs["RESIN_PROBE_TIMEOUT"] = "20s"
	envs["RESIN_RESOURCE_FETCH_TIMEOUT"] = "45s"
	envs["RESIN_PROXY_TRANSPORT_MAX_IDLE_CONNS"] = "2048"
	envs["RESIN_PROXY_TRANSPORT_MAX_IDLE_CONNS_PER_HOST"] = "128"
	envs["RESIN_PROXY_TRANSPORT_IDLE_CONN_TIMEOUT"] = "2m"
	envs["RESIN_REQUEST_LOG_QUEUE_FLUSH_INTERVAL"] = "10m"
	setEnvs(t, envs)

	cfg, err := LoadEnvConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertEqual(t, "CacheDir", cfg.CacheDir, "/tmp/cache")
	assertEqual(t, "ListenAddress", cfg.ListenAddress, "127.0.0.1")
	assertEqual(t, "ResinPort", cfg.ResinPort, 8080)
	assertEqual(t, "APIMaxBodyBytes", cfg.APIMaxBodyBytes, 2097152)
	assertEqual(t, "ProbeConcurrency", cfg.ProbeConcurrency, 500)
	assertEqual(t, "GeoIPUpdateSchedule", cfg.GeoIPUpdateSchedule, "0 0 * * *")
	assertEqual(t, "DefaultPlatformStickyTTL", cfg.DefaultPlatformStickyTTL, 2*time.Hour)
	assertEqual(t, "DefaultPlatformRegexFiltersLength", len(cfg.DefaultPlatformRegexFilters), 1)
	assertEqual(t, "DefaultPlatformRegexFilters[0]", cfg.DefaultPlatformRegexFilters[0], "^Provider/.*")
	assertEqual(t, "DefaultPlatformRegionFiltersLength", len(cfg.DefaultPlatformRegionFilters), 2)
	assertEqual(t, "DefaultPlatformRegionFilters[0]", cfg.DefaultPlatformRegionFilters[0], "us")
	assertEqual(t, "DefaultPlatformRegionFilters[1]", cfg.DefaultPlatformRegionFilters[1], "hk")
	assertEqual(t, "DefaultPlatformReverseProxyMissAction", cfg.DefaultPlatformReverseProxyMissAction, "REJECT")
	assertEqual(
		t,
		"DefaultPlatformReverseProxyEmptyAccountBehavior",
		cfg.DefaultPlatformReverseProxyEmptyAccountBehavior,
		"FIXED_HEADER",
	)
	assertEqual(
		t,
		"DefaultPlatformReverseProxyFixedAccountHeader",
		cfg.DefaultPlatformReverseProxyFixedAccountHeader,
		"X-Platform-Account",
	)
	assertEqual(t, "DefaultPlatformAllocationPolicy", cfg.DefaultPlatformAllocationPolicy, "PREFER_LOW_LATENCY")
	assertEqual(t, "ProbeTimeout", cfg.ProbeTimeout, 20*time.Second)
	assertEqual(t, "ResourceFetchTimeout", cfg.ResourceFetchTimeout, 45*time.Second)
	assertEqual(t, "ProxyTransportMaxIdleConns", cfg.ProxyTransportMaxIdleConns, 2048)
	assertEqual(t, "ProxyTransportMaxIdleConnsPerHost", cfg.ProxyTransportMaxIdleConnsPerHost, 128)
	assertEqual(t, "ProxyTransportIdleConnTimeout", cfg.ProxyTransportIdleConnTimeout, 2*time.Minute)
	if cfg.RequestLogQueueFlushInterval.String() != "10m0s" {
		t.Errorf("RequestLogQueueFlushInterval: got %v, want 10m", cfg.RequestLogQueueFlushInterval)
	}
}

func TestLoadEnvConfig_DefaultPlatformFixedHeaderMultiline(t *testing.T) {
	envs := requiredEnvs()
	envs["RESIN_DEFAULT_PLATFORM_REVERSE_PROXY_EMPTY_ACCOUNT_BEHAVIOR"] = "FIXED_HEADER"
	envs["RESIN_DEFAULT_PLATFORM_REVERSE_PROXY_FIXED_ACCOUNT_HEADER"] = " authorization \nx-account-id\nX-Account-Id "
	setEnvs(t, envs)

	cfg, err := LoadEnvConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertEqual(
		t,
		"DefaultPlatformReverseProxyFixedAccountHeader",
		cfg.DefaultPlatformReverseProxyFixedAccountHeader,
		"Authorization\nX-Account-Id",
	)
}

func TestLoadEnvConfig_MissingAdminToken(t *testing.T) {
	t.Setenv("RESIN_PROXY_TOKEN", "proxy-secret")
	// Ensure RESIN_ADMIN_TOKEN is not set
	os.Unsetenv("RESIN_ADMIN_TOKEN")

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for missing RESIN_ADMIN_TOKEN")
	}
	assertContains(t, err.Error(), "RESIN_ADMIN_TOKEN must be defined (can be empty)")
}

func TestLoadEnvConfig_MissingProxyToken(t *testing.T) {
	t.Setenv("RESIN_ADMIN_TOKEN", "admin-secret")
	os.Unsetenv("RESIN_PROXY_TOKEN")

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for missing RESIN_PROXY_TOKEN")
	}
	assertContains(t, err.Error(), "RESIN_PROXY_TOKEN must be defined (can be empty)")
}

func TestLoadEnvConfig_EmptyTokensAllowedWhenDefined(t *testing.T) {
	t.Setenv("RESIN_ADMIN_TOKEN", "")
	t.Setenv("RESIN_PROXY_TOKEN", "")

	cfg, err := LoadEnvConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEqual(t, "AdminToken", cfg.AdminToken, "")
	assertEqual(t, "ProxyToken", cfg.ProxyToken, "")
}

func TestLoadEnvConfig_ProxyTokenForbiddenChars(t *testing.T) {
	tests := []struct {
		name  string
		token string
	}{
		{"colon", "bad:token"},
		{"at", "bad@token"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RESIN_ADMIN_TOKEN", "admin-secret")
			t.Setenv("RESIN_PROXY_TOKEN", tc.token)

			_, err := LoadEnvConfig()
			if err == nil {
				t.Fatal("expected error for forbidden char in RESIN_PROXY_TOKEN")
			}
			assertContains(t, err.Error(), "must not contain")
		})
	}
}

func TestLoadEnvConfig_ProxyTokenReservedKeywords(t *testing.T) {
	tests := []string{"api", "healthz", "ui"}
	for _, token := range tests {
		t.Run(token, func(t *testing.T) {
			t.Setenv("RESIN_ADMIN_TOKEN", "admin-secret")
			t.Setenv("RESIN_PROXY_TOKEN", token)

			_, err := LoadEnvConfig()
			if err == nil {
				t.Fatal("expected error for reserved RESIN_PROXY_TOKEN")
			}
			assertContains(t, err.Error(), "reserved keyword")
		})
	}
}

func TestLoadEnvConfig_EmptyListenAddress(t *testing.T) {
	envs := requiredEnvs()
	envs["RESIN_LISTEN_ADDRESS"] = "   "
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for empty listen address")
	}
	assertContains(t, err.Error(), "RESIN_LISTEN_ADDRESS")
}

func TestLoadEnvConfig_InvalidPort(t *testing.T) {
	envs := requiredEnvs()
	envs["RESIN_PORT"] = "99999"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for port out of range")
	}
	assertContains(t, err.Error(), "RESIN_PORT")
}

func TestLoadEnvConfig_InvalidPortNotNumber(t *testing.T) {
	envs := requiredEnvs()
	envs["RESIN_PORT"] = "abc"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for non-numeric port")
	}
	assertContains(t, err.Error(), "RESIN_PORT")
}

func TestLoadEnvConfig_ZeroPort(t *testing.T) {
	envs := requiredEnvs()
	envs["RESIN_PORT"] = "0"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for zero port")
	}
	assertContains(t, err.Error(), "RESIN_PORT")
}

func TestLoadEnvConfig_InvalidAPIMaxBodyBytes(t *testing.T) {
	envs := requiredEnvs()
	envs["RESIN_API_MAX_BODY_BYTES"] = "0"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for non-positive API max body bytes")
	}
	assertContains(t, err.Error(), "RESIN_API_MAX_BODY_BYTES")
}

func TestLoadEnvConfig_QueueSizeTooSmall(t *testing.T) {
	envs := requiredEnvs()
	envs["RESIN_REQUEST_LOG_QUEUE_SIZE"] = "100"
	envs["RESIN_REQUEST_LOG_QUEUE_FLUSH_BATCH_SIZE"] = "100"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for queue size < 2x batch size")
	}
	assertContains(t, err.Error(), "at least 2x")
}

func TestLoadEnvConfig_InvalidDuration(t *testing.T) {
	envs := requiredEnvs()
	envs["RESIN_REQUEST_LOG_QUEUE_FLUSH_INTERVAL"] = "not-a-duration"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for invalid duration")
	}
	assertContains(t, err.Error(), "RESIN_REQUEST_LOG_QUEUE_FLUSH_INTERVAL")
}

func TestLoadEnvConfig_NegativeValue(t *testing.T) {
	envs := requiredEnvs()
	envs["RESIN_PROBE_CONCURRENCY"] = "-5"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for negative value")
	}
	assertContains(t, err.Error(), "RESIN_PROBE_CONCURRENCY")
}

func TestLoadEnvConfig_InvalidGeoIPSchedule(t *testing.T) {
	envs := requiredEnvs()
	envs["RESIN_GEOIP_UPDATE_SCHEDULE"] = "not-a-cron"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for invalid geoip schedule")
	}
	assertContains(t, err.Error(), "RESIN_GEOIP_UPDATE_SCHEDULE")
}

func TestLoadEnvConfig_InvalidDefaultPlatformAllocationPolicy(t *testing.T) {
	envs := requiredEnvs()
	envs["RESIN_DEFAULT_PLATFORM_ALLOCATION_POLICY"] = "UNKNOWN"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for invalid default platform allocation policy")
	}
	assertContains(t, err.Error(), "RESIN_DEFAULT_PLATFORM_ALLOCATION_POLICY")
}

func TestLoadEnvConfig_InvalidDefaultPlatformEmptyAccountBehavior(t *testing.T) {
	envs := requiredEnvs()
	envs["RESIN_DEFAULT_PLATFORM_REVERSE_PROXY_EMPTY_ACCOUNT_BEHAVIOR"] = "UNKNOWN"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for invalid default platform empty-account behavior")
	}
	assertContains(t, err.Error(), "RESIN_DEFAULT_PLATFORM_REVERSE_PROXY_EMPTY_ACCOUNT_BEHAVIOR")
}

func TestLoadEnvConfig_FixedHeaderModeRequiresHeader(t *testing.T) {
	envs := requiredEnvs()
	envs["RESIN_DEFAULT_PLATFORM_REVERSE_PROXY_EMPTY_ACCOUNT_BEHAVIOR"] = "FIXED_HEADER"
	envs["RESIN_DEFAULT_PLATFORM_REVERSE_PROXY_FIXED_ACCOUNT_HEADER"] = "   "
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error when fixed-header mode has empty header")
	}
	assertContains(t, err.Error(), "RESIN_DEFAULT_PLATFORM_REVERSE_PROXY_FIXED_ACCOUNT_HEADER")
}

func TestLoadEnvConfig_InvalidDefaultPlatformRegex(t *testing.T) {
	envs := requiredEnvs()
	envs["RESIN_DEFAULT_PLATFORM_REGEX_FILTERS"] = `["("]`
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for invalid default platform regex")
	}
	assertContains(t, err.Error(), "RESIN_DEFAULT_PLATFORM_REGEX_FILTERS")
}

func TestLoadEnvConfig_InvalidProbeTimeout(t *testing.T) {
	envs := requiredEnvs()
	envs["RESIN_PROBE_TIMEOUT"] = "0s"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for invalid probe timeout")
	}
	assertContains(t, err.Error(), "RESIN_PROBE_TIMEOUT")
}

func TestLoadEnvConfig_InvalidProxyTransportSettings(t *testing.T) {
	envs := requiredEnvs()
	envs["RESIN_PROXY_TRANSPORT_MAX_IDLE_CONNS"] = "16"
	envs["RESIN_PROXY_TRANSPORT_MAX_IDLE_CONNS_PER_HOST"] = "32"
	envs["RESIN_PROXY_TRANSPORT_IDLE_CONN_TIMEOUT"] = "0s"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected error for invalid proxy transport settings")
	}
	assertContains(t, err.Error(), "RESIN_PROXY_TRANSPORT_IDLE_CONN_TIMEOUT")
	assertContains(t, err.Error(), "RESIN_PROXY_TRANSPORT_MAX_IDLE_CONNS_PER_HOST")
}

// --- test helpers ---

func assertEqual[T comparable](t *testing.T, name string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v, want %v", name, got, want)
	}
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected %q to contain %q", s, substr)
	}
}
