export type NodeTag = {
  subscription_id: string;
  subscription_name: string;
  tag: string;
};

export type NodeProxyUrl = {
  type: "http" | "https" | "socks5";
  url: string;
};

export type NodeSummary = {
  node_hash: string;
  created_at: string;
  enabled: boolean;
  manually_disabled?: boolean;
  display_tag?: string;
  has_outbound: boolean;
  last_error?: string;
  circuit_open_since?: string;
  failure_count: number;
  egress_ip?: string;
  reference_latency_ms?: number;
  region?: string;
  last_egress_update?: string;
  last_latency_probe_attempt?: string;
  last_authority_latency_probe_attempt?: string;
  last_egress_update_attempt?: string;
  lease_count: number;
  tags: NodeTag[];
  outbound?: unknown;
  proxy_urls?: NodeProxyUrl[];
};

export type PageResponse<T> = {
  items: T[];
  total: number;
  limit: number;
  offset: number;
  unique_egress_ips: number;
  unique_healthy_egress_ips: number;
};

export type NodeSortBy =
  | "tag"
  | "created_at"
  | "failure_count"
  | "region"
  | "lease_count"
  | "reference_latency_ms";
export type SortOrder = "asc" | "desc";

export type NodeListFilters = {
  platform_id?: string;
  subscription_id?: string;
  tag_keyword?: string;
  node_type?: string;
  region?: string;
  egress_ip?: string;
  probed_since?: string;
  enabled?: boolean;
  manually_disabled?: boolean;
  circuit_open?: boolean;
  has_outbound?: boolean;
};

export type NodeListQuery = NodeListFilters & {
  sort_by?: NodeSortBy;
  sort_order?: SortOrder;
  limit?: number;
  offset?: number;
};

export type EgressProbeResult = {
  egress_ip: string;
  region?: string;
  latency_ewma_ms: number;
};

export type LatencyProbeResult = {
  latency_ewma_ms: number;
};

export type NodeLease = {
  platform_id: string;
  platform_name: string;
  account: string;
  node_hash: string;
  egress_ip: string;
  created_at: string;
  expiry: string;
  last_accessed: string;
};
