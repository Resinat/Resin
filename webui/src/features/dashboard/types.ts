export type TimeWindow = {
  from: string;
  to: string;
};

export type RealtimeThroughputItem = {
  ts: string;
  ingress_bps: number;
  egress_bps: number;
};

export type RealtimeConnectionsItem = {
  ts: string;
  inbound_connections: number;
  outbound_connections: number;
};

export type RealtimeLeasesItem = {
  ts: string;
  active_leases: number;
};

export type RealtimeSeriesResponse<T> = {
  step_seconds: number;
  items: T[];
};

export type RealtimeLeasesResponse = RealtimeSeriesResponse<RealtimeLeasesItem> & {
  platform_id: string;
};

export type HistoryTrafficItem = {
  bucket_start: string;
  bucket_end: string;
  ingress_bytes: number;
  egress_bytes: number;
};

export type HistoryRequestsItem = {
  bucket_start: string;
  bucket_end: string;
  total_requests: number;
  success_requests: number;
  success_rate: number;
};

export type LatencyBucket = {
  le_ms: number;
  count: number;
};

export type HistoryAccessLatencyItem = {
  bucket_start: string;
  bucket_end: string;
  sample_count: number;
  buckets: LatencyBucket[];
  overflow_count: number;
};

export type HistoryProbesItem = {
  bucket_start: string;
  bucket_end: string;
  total_count: number;
};

export type HistoryNodePoolItem = {
  bucket_start: string;
  bucket_end: string;
  total_nodes: number;
  healthy_nodes: number;
  egress_ip_count: number;
};

export type HistoryLeaseLifetimeItem = {
  bucket_start: string;
  bucket_end: string;
  sample_count: number;
  p1_ms: number;
  p5_ms: number;
  p50_ms: number;
};

export type HistoryResponse<T> = {
  bucket_seconds: number;
  items: T[];
};

export type HistoryAccessLatencyResponse = HistoryResponse<HistoryAccessLatencyItem> & {
  bin_width_ms: number;
  overflow_ms: number;
};

export type HistoryLeaseLifetimeResponse = HistoryResponse<HistoryLeaseLifetimeItem> & {
  platform_id: string;
};

export type SnapshotNodePool = {
  generated_at: string;
  total_nodes: number;
  healthy_nodes: number;
  egress_ip_count: number;
};

export type SnapshotPlatformNodePool = {
  generated_at: string;
  platform_id: string;
  routable_node_count: number;
  egress_ip_count: number;
};

export type SnapshotNodeLatencyDistribution = {
  generated_at: string;
  scope: "global" | "platform";
  platform_id?: string;
  bin_width_ms: number;
  overflow_ms: number;
  sample_count: number;
  buckets: LatencyBucket[];
  overflow_count: number;
};

export type DashboardGlobalData = {
  realtime_throughput: RealtimeSeriesResponse<RealtimeThroughputItem>;
  realtime_connections: RealtimeSeriesResponse<RealtimeConnectionsItem>;
  history_traffic: HistoryResponse<HistoryTrafficItem>;
  history_requests: HistoryResponse<HistoryRequestsItem>;
  history_access_latency: HistoryAccessLatencyResponse;
  history_probes: HistoryResponse<HistoryProbesItem>;
  history_node_pool: HistoryResponse<HistoryNodePoolItem>;
  snapshot_node_pool: SnapshotNodePool;
  snapshot_latency_global: SnapshotNodeLatencyDistribution;
};

export type DashboardPlatformData = {
  realtime_leases: RealtimeLeasesResponse;
  history_lease_lifetime: HistoryLeaseLifetimeResponse;
  snapshot_platform_node_pool: SnapshotPlatformNodePool;
  snapshot_latency_platform: SnapshotNodeLatencyDistribution;
};
