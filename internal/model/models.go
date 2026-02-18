// Package model defines domain structs shared across the persistence layer.
package model

// Platform represents a routing platform.
type Platform struct {
	ID                     string `json:"id"`
	Name                   string `json:"name"`
	StickyTTLNs            int64  `json:"sticky_ttl_ns"`
	RegexFiltersJSON       string `json:"regex_filters_json"`
	RegionFiltersJSON      string `json:"region_filters_json"`
	ReverseProxyMissAction string `json:"reverse_proxy_miss_action"`
	AllocationPolicy       string `json:"allocation_policy"`
	UpdatedAtNs            int64  `json:"updated_at_ns"`
}

// Subscription represents a node subscription source.
type Subscription struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	URL              string `json:"url"`
	UpdateIntervalNs int64  `json:"update_interval_ns"`
	Enabled          bool   `json:"enabled"`
	Ephemeral        bool   `json:"ephemeral"`
	CreatedAtNs      int64  `json:"created_at_ns"`
	UpdatedAtNs      int64  `json:"updated_at_ns"`
}

// AccountHeaderRule defines header extraction rules for reverse proxy account matching.
type AccountHeaderRule struct {
	URLPrefix   string `json:"url_prefix"`
	HeadersJSON string `json:"headers_json"`
	UpdatedAtNs int64  `json:"updated_at_ns"`
}

// NodeStatic holds the immutable portion of a node's data.
type NodeStatic struct {
	Hash           string `json:"hash"`
	RawOptionsJSON string `json:"raw_options_json"`
	CreatedAtNs    int64  `json:"created_at_ns"`
}

// NodeDynamic holds the mutable runtime state of a node.
type NodeDynamic struct {
	Hash              string `json:"hash"`
	FailureCount      int    `json:"failure_count"`
	CircuitOpenSince  int64  `json:"circuit_open_since"`
	EgressIP          string `json:"egress_ip"`
	EgressUpdatedAtNs int64  `json:"egress_updated_at_ns"`
}

// NodeLatency holds per-domain latency statistics for a node.
type NodeLatency struct {
	NodeHash      string `json:"node_hash"`
	Domain        string `json:"domain"`
	EwmaNs        int64  `json:"ewma_ns"`
	LastUpdatedNs int64  `json:"last_updated_ns"`
}

// NodeLatencyKey is the composite primary key for node_latency.
type NodeLatencyKey struct {
	NodeHash string
	Domain   string
}

// Lease represents a sticky routing lease.
type Lease struct {
	PlatformID     string `json:"platform_id"`
	Account        string `json:"account"`
	NodeHash       string `json:"node_hash"`
	EgressIP       string `json:"egress_ip"`
	CreatedAtNs    int64  `json:"created_at_ns"`
	ExpiryNs       int64  `json:"expiry_ns"`
	LastAccessedNs int64  `json:"last_accessed_ns"`
}

// LeaseKey is the composite primary key for leases.
type LeaseKey struct {
	PlatformID string
	Account    string
}

// SubscriptionNode links a subscription to a node with tags.
type SubscriptionNode struct {
	SubscriptionID string `json:"subscription_id"`
	NodeHash       string `json:"node_hash"`
	TagsJSON       string `json:"tags_json"`
}

// SubscriptionNodeKey is the composite primary key for subscription_nodes.
type SubscriptionNodeKey struct {
	SubscriptionID string
	NodeHash       string
}
