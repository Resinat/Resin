export type NodeTag = {
  subscription_id: string;
  subscription_name: string;
  tag: string;
};

export type NodeSummary = {
  node_hash: string;
  created_at: string;
  has_outbound: boolean;
  last_error?: string;
  circuit_open_since?: string;
  failure_count: number;
  egress_ip?: string;
  region?: string;
  last_egress_update?: string;
  last_latency_probe_attempt?: string;
  last_authority_latency_probe_attempt?: string;
  last_egress_update_attempt?: string;
  tags: NodeTag[];
};

export type PageResponse<T> = {
  items: T[];
  total: number;
  limit: number;
  offset: number;
  unique_egress_ips: number;
};

export type NodeSortBy = "tag" | "created_at" | "failure_count" | "region";
export type SortOrder = "asc" | "desc";

export type NodeListFilters = {
  platform_id?: string;
  subscription_id?: string;
  region?: string;
  egress_ip?: string;
  probed_since?: string;
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
  latency_ewma_ms: number;
};

export type LatencyProbeResult = {
  latency_ewma_ms: number;
};
