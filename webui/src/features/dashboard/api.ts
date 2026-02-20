import { apiRequest } from "../../lib/api-client";
import type {
  DashboardGlobalData,
  DashboardPlatformData,
  HistoryAccessLatencyResponse,
  HistoryLeaseLifetimeResponse,
  HistoryResponse,
  HistoryNodePoolItem,
  HistoryProbesItem,
  HistoryRequestsItem,
  HistoryTrafficItem,
  RealtimeConnectionsItem,
  RealtimeLeasesItem,
  RealtimeLeasesResponse,
  RealtimeSeriesResponse,
  RealtimeThroughputItem,
  SnapshotNodeLatencyDistribution,
  SnapshotNodePool,
  SnapshotPlatformNodePool,
  TimeWindow,
} from "./types";

const basePath = "/api/v1/metrics";

function withWindow(path: string, window: TimeWindow, params: Record<string, string> = {}): string {
  const query = new URLSearchParams({
    from: window.from,
    to: window.to,
    ...params,
  });
  return `${path}?${query.toString()}`;
}

function toNumber(raw: unknown): number {
  const value = Number(raw);
  if (!Number.isFinite(value)) {
    return 0;
  }
  return value;
}

function toString(raw: unknown): string {
  return typeof raw === "string" ? raw : "";
}

function normalizeRealtimeThroughputItem(raw: RealtimeThroughputItem): RealtimeThroughputItem {
  return {
    ts: toString(raw.ts),
    ingress_bps: toNumber(raw.ingress_bps),
    egress_bps: toNumber(raw.egress_bps),
  };
}

function normalizeRealtimeConnectionsItem(raw: RealtimeConnectionsItem): RealtimeConnectionsItem {
  return {
    ts: toString(raw.ts),
    inbound_connections: toNumber(raw.inbound_connections),
    outbound_connections: toNumber(raw.outbound_connections),
  };
}

function normalizeRealtimeLeasesItem(raw: RealtimeLeasesItem): RealtimeLeasesItem {
  return {
    ts: toString(raw.ts),
    active_leases: toNumber(raw.active_leases),
  };
}

function normalizeHistoryTrafficItem(raw: HistoryTrafficItem): HistoryTrafficItem {
  return {
    bucket_start: toString(raw.bucket_start),
    bucket_end: toString(raw.bucket_end),
    ingress_bytes: toNumber(raw.ingress_bytes),
    egress_bytes: toNumber(raw.egress_bytes),
  };
}

function normalizeHistoryRequestsItem(raw: HistoryRequestsItem): HistoryRequestsItem {
  return {
    bucket_start: toString(raw.bucket_start),
    bucket_end: toString(raw.bucket_end),
    total_requests: toNumber(raw.total_requests),
    success_requests: toNumber(raw.success_requests),
    success_rate: toNumber(raw.success_rate),
  };
}

function normalizeHistoryProbesItem(raw: HistoryProbesItem): HistoryProbesItem {
  return {
    bucket_start: toString(raw.bucket_start),
    bucket_end: toString(raw.bucket_end),
    total_count: toNumber(raw.total_count),
  };
}

function normalizeHistoryNodePoolItem(raw: HistoryNodePoolItem): HistoryNodePoolItem {
  return {
    bucket_start: toString(raw.bucket_start),
    bucket_end: toString(raw.bucket_end),
    total_nodes: toNumber(raw.total_nodes),
    healthy_nodes: toNumber(raw.healthy_nodes),
    egress_ip_count: toNumber(raw.egress_ip_count),
  };
}

function normalizeHistoryLeaseLifetimeItem(raw: HistoryLeaseLifetimeResponse["items"][number]): HistoryLeaseLifetimeResponse["items"][number] {
  return {
    bucket_start: toString(raw.bucket_start),
    bucket_end: toString(raw.bucket_end),
    sample_count: toNumber(raw.sample_count),
    p1_ms: toNumber(raw.p1_ms),
    p5_ms: toNumber(raw.p5_ms),
    p50_ms: toNumber(raw.p50_ms),
  };
}

function normalizeLatencyDistribution(raw: SnapshotNodeLatencyDistribution): SnapshotNodeLatencyDistribution {
  return {
    generated_at: toString(raw.generated_at),
    scope: raw.scope === "platform" ? "platform" : "global",
    platform_id: raw.platform_id ? toString(raw.platform_id) : undefined,
    bin_width_ms: toNumber(raw.bin_width_ms),
    overflow_ms: toNumber(raw.overflow_ms),
    sample_count: toNumber(raw.sample_count),
    buckets: Array.isArray(raw.buckets)
      ? raw.buckets.map((bucket) => ({
          le_ms: toNumber(bucket.le_ms),
          count: toNumber(bucket.count),
        }))
      : [],
    overflow_count: toNumber(raw.overflow_count),
  };
}

async function getRealtimeThroughput(window: TimeWindow): Promise<RealtimeSeriesResponse<RealtimeThroughputItem>> {
  const data = await apiRequest<RealtimeSeriesResponse<RealtimeThroughputItem>>(
    withWindow(`${basePath}/realtime/throughput`, window),
  );
  return {
    step_seconds: toNumber(data.step_seconds),
    items: Array.isArray(data.items) ? data.items.map(normalizeRealtimeThroughputItem) : [],
  };
}

async function getRealtimeConnections(window: TimeWindow): Promise<RealtimeSeriesResponse<RealtimeConnectionsItem>> {
  const data = await apiRequest<RealtimeSeriesResponse<RealtimeConnectionsItem>>(
    withWindow(`${basePath}/realtime/connections`, window),
  );
  return {
    step_seconds: toNumber(data.step_seconds),
    items: Array.isArray(data.items) ? data.items.map(normalizeRealtimeConnectionsItem) : [],
  };
}

async function getRealtimeLeases(platformId: string, window: TimeWindow): Promise<RealtimeLeasesResponse> {
  const data = await apiRequest<RealtimeLeasesResponse>(
    withWindow(`${basePath}/realtime/leases`, window, { platform_id: platformId }),
  );
  return {
    platform_id: toString(data.platform_id),
    step_seconds: toNumber(data.step_seconds),
    items: Array.isArray(data.items) ? data.items.map(normalizeRealtimeLeasesItem) : [],
  };
}

async function getHistoryTraffic(window: TimeWindow): Promise<HistoryResponse<HistoryTrafficItem>> {
  const data = await apiRequest<HistoryResponse<HistoryTrafficItem>>(withWindow(`${basePath}/history/traffic`, window));
  return {
    bucket_seconds: toNumber(data.bucket_seconds),
    items: Array.isArray(data.items) ? data.items.map(normalizeHistoryTrafficItem) : [],
  };
}

async function getHistoryRequests(window: TimeWindow): Promise<HistoryResponse<HistoryRequestsItem>> {
  const data = await apiRequest<HistoryResponse<HistoryRequestsItem>>(withWindow(`${basePath}/history/requests`, window));
  return {
    bucket_seconds: toNumber(data.bucket_seconds),
    items: Array.isArray(data.items) ? data.items.map(normalizeHistoryRequestsItem) : [],
  };
}

async function getHistoryAccessLatency(window: TimeWindow): Promise<HistoryAccessLatencyResponse> {
  const data = await apiRequest<HistoryAccessLatencyResponse>(withWindow(`${basePath}/history/access-latency`, window));
  return {
    bucket_seconds: toNumber(data.bucket_seconds),
    bin_width_ms: toNumber(data.bin_width_ms),
    overflow_ms: toNumber(data.overflow_ms),
    items: Array.isArray(data.items)
      ? data.items.map((item) => ({
          bucket_start: toString(item.bucket_start),
          bucket_end: toString(item.bucket_end),
          sample_count: toNumber(item.sample_count),
          buckets: Array.isArray(item.buckets)
            ? item.buckets.map((bucket) => ({
                le_ms: toNumber(bucket.le_ms),
                count: toNumber(bucket.count),
              }))
            : [],
          overflow_count: toNumber(item.overflow_count),
        }))
      : [],
  };
}

async function getHistoryProbes(window: TimeWindow): Promise<HistoryResponse<HistoryProbesItem>> {
  const data = await apiRequest<HistoryResponse<HistoryProbesItem>>(withWindow(`${basePath}/history/probes`, window));
  return {
    bucket_seconds: toNumber(data.bucket_seconds),
    items: Array.isArray(data.items) ? data.items.map(normalizeHistoryProbesItem) : [],
  };
}

async function getHistoryNodePool(window: TimeWindow): Promise<HistoryResponse<HistoryNodePoolItem>> {
  const data = await apiRequest<HistoryResponse<HistoryNodePoolItem>>(withWindow(`${basePath}/history/node-pool`, window));
  return {
    bucket_seconds: toNumber(data.bucket_seconds),
    items: Array.isArray(data.items) ? data.items.map(normalizeHistoryNodePoolItem) : [],
  };
}

async function getHistoryLeaseLifetime(platformId: string, window: TimeWindow): Promise<HistoryLeaseLifetimeResponse> {
  const data = await apiRequest<HistoryLeaseLifetimeResponse>(
    withWindow(`${basePath}/history/lease-lifetime`, window, { platform_id: platformId }),
  );
  return {
    platform_id: toString(data.platform_id),
    bucket_seconds: toNumber(data.bucket_seconds),
    items: Array.isArray(data.items) ? data.items.map(normalizeHistoryLeaseLifetimeItem) : [],
  };
}

async function getSnapshotNodePool(): Promise<SnapshotNodePool> {
  const data = await apiRequest<SnapshotNodePool>(`${basePath}/snapshots/node-pool`);
  return {
    generated_at: toString(data.generated_at),
    total_nodes: toNumber(data.total_nodes),
    healthy_nodes: toNumber(data.healthy_nodes),
    egress_ip_count: toNumber(data.egress_ip_count),
  };
}

async function getSnapshotPlatformNodePool(platformId: string): Promise<SnapshotPlatformNodePool> {
  const query = new URLSearchParams({ platform_id: platformId });
  const data = await apiRequest<SnapshotPlatformNodePool>(`${basePath}/snapshots/platform-node-pool?${query.toString()}`);
  return {
    generated_at: toString(data.generated_at),
    platform_id: toString(data.platform_id),
    routable_node_count: toNumber(data.routable_node_count),
    egress_ip_count: toNumber(data.egress_ip_count),
  };
}

async function getSnapshotLatency(platformId?: string): Promise<SnapshotNodeLatencyDistribution> {
  if (!platformId) {
    const data = await apiRequest<SnapshotNodeLatencyDistribution>(`${basePath}/snapshots/node-latency-distribution`);
    return normalizeLatencyDistribution(data);
  }

  const query = new URLSearchParams({ platform_id: platformId });
  const data = await apiRequest<SnapshotNodeLatencyDistribution>(
    `${basePath}/snapshots/node-latency-distribution?${query.toString()}`,
  );
  return normalizeLatencyDistribution(data);
}

export async function getDashboardGlobalData(window: TimeWindow): Promise<DashboardGlobalData> {
  const [
    realtime_throughput,
    realtime_connections,
    history_traffic,
    history_requests,
    history_access_latency,
    history_probes,
    history_node_pool,
    snapshot_node_pool,
    snapshot_latency_global,
  ] = await Promise.all([
    getRealtimeThroughput(window),
    getRealtimeConnections(window),
    getHistoryTraffic(window),
    getHistoryRequests(window),
    getHistoryAccessLatency(window),
    getHistoryProbes(window),
    getHistoryNodePool(window),
    getSnapshotNodePool(),
    getSnapshotLatency(),
  ]);

  return {
    realtime_throughput,
    realtime_connections,
    history_traffic,
    history_requests,
    history_access_latency,
    history_probes,
    history_node_pool,
    snapshot_node_pool,
    snapshot_latency_global,
  };
}

export async function getDashboardPlatformData(platformId: string, window: TimeWindow): Promise<DashboardPlatformData> {
  const [realtime_leases, history_lease_lifetime, snapshot_platform_node_pool, snapshot_latency_platform] = await Promise.all([
    getRealtimeLeases(platformId, window),
    getHistoryLeaseLifetime(platformId, window),
    getSnapshotPlatformNodePool(platformId),
    getSnapshotLatency(platformId),
  ]);

  return {
    realtime_leases,
    history_lease_lifetime,
    snapshot_platform_node_pool,
    snapshot_latency_platform,
  };
}
