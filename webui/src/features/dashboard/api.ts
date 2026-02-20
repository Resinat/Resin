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

function withWindow(path: string, window: TimeWindow, params?: Record<string, string>): string {
  const query = new URLSearchParams({
    from: window.from,
    to: window.to,
    ...(params ?? {}),
  });
  return `${path}?${query.toString()}`;
}

type BucketSeriesItem = {
  bucket_start: string;
};

type RealtimeSeriesItem = {
  ts: string;
};

type MergeableHistoryResponse<T extends BucketSeriesItem> = {
  bucket_seconds: number;
  items: T[];
};

type MergeableRealtimeResponse<T extends RealtimeSeriesItem> = {
  step_seconds: number;
  items: T[];
};

function toTimestamp(input: string): number | null {
  const value = Date.parse(input);
  if (Number.isNaN(value)) {
    return null;
  }
  return value;
}

function pickPositiveOrFallback(primary: number, fallback?: number): number {
  if (Number.isFinite(primary) && primary > 0) {
    return primary;
  }
  if (Number.isFinite(fallback) && typeof fallback === "number" && fallback > 0) {
    return fallback;
  }
  return 0;
}

function latestBucketStart(items: BucketSeriesItem[] | undefined): number | null {
  if (!items?.length) {
    return null;
  }
  let latest: number | null = null;
  for (const item of items) {
    const ts = toTimestamp(item.bucket_start);
    if (ts === null) {
      continue;
    }
    if (latest === null || ts > latest) {
      latest = ts;
    }
  }
  return latest;
}

function latestRealtimeTimestamp(items: RealtimeSeriesItem[] | undefined): number | null {
  if (!items?.length) {
    return null;
  }
  let latest: number | null = null;
  for (const item of items) {
    const ts = toTimestamp(item.ts);
    if (ts === null) {
      continue;
    }
    if (latest === null || ts > latest) {
      latest = ts;
    }
  }
  return latest;
}

function buildIncrementalWindow(
  window: TimeWindow,
  previousItems: BucketSeriesItem[] | undefined,
  previousBucketSeconds: number | undefined,
): TimeWindow {
  const windowFrom = toTimestamp(window.from);
  const windowTo = toTimestamp(window.to);
  if (windowFrom === null || windowTo === null || windowFrom >= windowTo) {
    return window;
  }

  const latest = latestBucketStart(previousItems);
  if (latest !== null) {
    let from = Math.max(windowFrom, latest);
    if (from >= windowTo) {
      from = Math.max(windowFrom, windowTo - 1);
    }
    return {
      from: new Date(from).toISOString(),
      to: window.to,
    };
  }

  if (Number.isFinite(previousBucketSeconds) && typeof previousBucketSeconds === "number" && previousBucketSeconds > 0) {
    const lookbackMs = previousBucketSeconds * 1000;
    const from = Math.max(windowFrom, windowTo - lookbackMs);
    return {
      from: new Date(from).toISOString(),
      to: window.to,
    };
  }

  return window;
}

function buildRealtimeIncrementalWindow(
  window: TimeWindow,
  previousItems: RealtimeSeriesItem[] | undefined,
  previousStepSeconds: number | undefined,
): TimeWindow {
  const windowFrom = toTimestamp(window.from);
  const windowTo = toTimestamp(window.to);
  if (windowFrom === null || windowTo === null || windowFrom >= windowTo) {
    return window;
  }

  const latest = latestRealtimeTimestamp(previousItems);
  if (latest !== null) {
    let from = Math.max(windowFrom, latest);
    if (from >= windowTo) {
      from = Math.max(windowFrom, windowTo - 1);
    }
    return {
      from: new Date(from).toISOString(),
      to: window.to,
    };
  }

  if (Number.isFinite(previousStepSeconds) && typeof previousStepSeconds === "number" && previousStepSeconds > 0) {
    const lookbackMs = previousStepSeconds * 1000;
    const from = Math.max(windowFrom, windowTo - lookbackMs);
    return {
      from: new Date(from).toISOString(),
      to: window.to,
    };
  }

  return window;
}

function mergeWindowedHistoryItems<T extends BucketSeriesItem>(
  window: TimeWindow,
  previousItems: T[] | undefined,
  nextItems: T[],
): T[] {
  const merged = new Map<string, T>();
  for (const item of previousItems ?? []) {
    merged.set(item.bucket_start, item);
  }
  for (const item of nextItems) {
    merged.set(item.bucket_start, item);
  }

  const windowFrom = toTimestamp(window.from);
  const windowTo = toTimestamp(window.to);
  const hasBoundedWindow = windowFrom !== null && windowTo !== null && windowFrom < windowTo;

  return Array.from(merged.values())
    .filter((item) => {
      if (!hasBoundedWindow) {
        return true;
      }
      const ts = toTimestamp(item.bucket_start);
      if (ts === null) {
        return true;
      }
      return ts >= windowFrom && ts <= windowTo;
    })
    .sort((left, right) => {
      const leftTs = toTimestamp(left.bucket_start);
      const rightTs = toTimestamp(right.bucket_start);
      if (leftTs !== null && rightTs !== null && leftTs !== rightTs) {
        return leftTs - rightTs;
      }
      if (leftTs !== null && rightTs === null) {
        return -1;
      }
      if (leftTs === null && rightTs !== null) {
        return 1;
      }
      return left.bucket_start.localeCompare(right.bucket_start);
    });
}

function mergeWindowedRealtimeItems<T extends RealtimeSeriesItem>(
  window: TimeWindow,
  previousItems: T[] | undefined,
  nextItems: T[],
): T[] {
  const merged = new Map<string, T>();
  for (const item of previousItems ?? []) {
    merged.set(item.ts, item);
  }
  for (const item of nextItems) {
    merged.set(item.ts, item);
  }

  const windowFrom = toTimestamp(window.from);
  const windowTo = toTimestamp(window.to);
  const hasBoundedWindow = windowFrom !== null && windowTo !== null && windowFrom < windowTo;

  return Array.from(merged.values())
    .filter((item) => {
      if (!hasBoundedWindow) {
        return true;
      }
      const ts = toTimestamp(item.ts);
      if (ts === null) {
        return true;
      }
      return ts >= windowFrom && ts <= windowTo;
    })
    .sort((left, right) => {
      const leftTs = toTimestamp(left.ts);
      const rightTs = toTimestamp(right.ts);
      if (leftTs !== null && rightTs !== null && leftTs !== rightTs) {
        return leftTs - rightTs;
      }
      if (leftTs !== null && rightTs === null) {
        return -1;
      }
      if (leftTs === null && rightTs !== null) {
        return 1;
      }
      return left.ts.localeCompare(right.ts);
    });
}

function mergeHistoryResponse<T extends BucketSeriesItem>(
  window: TimeWindow,
  previous: MergeableHistoryResponse<T> | undefined,
  next: MergeableHistoryResponse<T>,
): MergeableHistoryResponse<T> {
  return {
    bucket_seconds: pickPositiveOrFallback(next.bucket_seconds, previous?.bucket_seconds),
    items: mergeWindowedHistoryItems(window, previous?.items, next.items),
  };
}

function mergeRealtimeResponse<T extends RealtimeSeriesItem>(
  window: TimeWindow,
  previous: MergeableRealtimeResponse<T> | undefined,
  next: MergeableRealtimeResponse<T>,
): MergeableRealtimeResponse<T> {
  return {
    step_seconds: pickPositiveOrFallback(next.step_seconds, previous?.step_seconds),
    items: mergeWindowedRealtimeItems(window, previous?.items, next.items),
  };
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

async function getRealtimeLeases(window: TimeWindow, platformId?: string): Promise<RealtimeLeasesResponse> {
  const params = platformId ? { platform_id: platformId } : undefined;
  const data = await apiRequest<RealtimeLeasesResponse>(withWindow(`${basePath}/realtime/leases`, window, params));
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

export type DashboardGlobalRealtimeData = Pick<DashboardGlobalData, "realtime_throughput" | "realtime_connections" | "realtime_leases">;
export type DashboardGlobalHistoryData = Pick<
  DashboardGlobalData,
  "history_traffic" | "history_requests" | "history_access_latency" | "history_probes" | "history_node_pool"
>;
export type DashboardGlobalSnapshotData = Pick<DashboardGlobalData, "snapshot_node_pool" | "snapshot_latency_global">;
export type DashboardPlatformRealtimeData = Pick<DashboardPlatformData, "realtime_leases">;
export type DashboardPlatformHistoryData = Pick<DashboardPlatformData, "history_lease_lifetime">;
export type DashboardPlatformSnapshotData = Pick<DashboardPlatformData, "snapshot_platform_node_pool" | "snapshot_latency_platform">;

export async function getDashboardGlobalRealtimeData(
  window: TimeWindow,
  previous?: DashboardGlobalRealtimeData,
): Promise<DashboardGlobalRealtimeData> {
  const throughputWindow = buildRealtimeIncrementalWindow(
    window,
    previous?.realtime_throughput.items,
    previous?.realtime_throughput.step_seconds,
  );
  const connectionsWindow = buildRealtimeIncrementalWindow(
    window,
    previous?.realtime_connections.items,
    previous?.realtime_connections.step_seconds,
  );
  const leasesWindow = buildRealtimeIncrementalWindow(
    window,
    previous?.realtime_leases.items,
    previous?.realtime_leases.step_seconds,
  );

  const [nextThroughput, nextConnections, nextLeases] = await Promise.all([
    getRealtimeThroughput(throughputWindow),
    getRealtimeConnections(connectionsWindow),
    getRealtimeLeases(leasesWindow),
  ]);

  return {
    realtime_throughput: mergeRealtimeResponse(window, previous?.realtime_throughput, nextThroughput),
    realtime_connections: mergeRealtimeResponse(window, previous?.realtime_connections, nextConnections),
    realtime_leases: {
      platform_id: toString(nextLeases.platform_id) || previous?.realtime_leases.platform_id || "",
      ...mergeRealtimeResponse(window, previous?.realtime_leases, nextLeases),
    },
  };
}

export async function getDashboardGlobalHistoryData(
  window: TimeWindow,
  previous?: DashboardGlobalHistoryData,
): Promise<DashboardGlobalHistoryData> {
  const trafficWindow = buildIncrementalWindow(window, previous?.history_traffic.items, previous?.history_traffic.bucket_seconds);
  const requestsWindow = buildIncrementalWindow(
    window,
    previous?.history_requests.items,
    previous?.history_requests.bucket_seconds,
  );
  const accessLatencyWindow = buildIncrementalWindow(
    window,
    previous?.history_access_latency.items,
    previous?.history_access_latency.bucket_seconds,
  );
  const probesWindow = buildIncrementalWindow(window, previous?.history_probes.items, previous?.history_probes.bucket_seconds);
  const nodePoolWindow = buildIncrementalWindow(
    window,
    previous?.history_node_pool.items,
    previous?.history_node_pool.bucket_seconds,
  );

  const [nextTraffic, nextRequests, nextAccessLatency, nextProbes, nextNodePool] = await Promise.all([
    getHistoryTraffic(trafficWindow),
    getHistoryRequests(requestsWindow),
    getHistoryAccessLatency(accessLatencyWindow),
    getHistoryProbes(probesWindow),
    getHistoryNodePool(nodePoolWindow),
  ]);

  return {
    history_traffic: mergeHistoryResponse(window, previous?.history_traffic, nextTraffic),
    history_requests: mergeHistoryResponse(window, previous?.history_requests, nextRequests),
    history_access_latency: {
      bucket_seconds: pickPositiveOrFallback(
        nextAccessLatency.bucket_seconds,
        previous?.history_access_latency.bucket_seconds,
      ),
      bin_width_ms: pickPositiveOrFallback(nextAccessLatency.bin_width_ms, previous?.history_access_latency.bin_width_ms),
      overflow_ms: pickPositiveOrFallback(nextAccessLatency.overflow_ms, previous?.history_access_latency.overflow_ms),
      items: mergeWindowedHistoryItems(window, previous?.history_access_latency.items, nextAccessLatency.items),
    },
    history_probes: mergeHistoryResponse(window, previous?.history_probes, nextProbes),
    history_node_pool: mergeHistoryResponse(window, previous?.history_node_pool, nextNodePool),
  };
}

export async function getDashboardGlobalSnapshotData(): Promise<DashboardGlobalSnapshotData> {
  const [snapshot_node_pool, snapshot_latency_global] = await Promise.all([getSnapshotNodePool(), getSnapshotLatency()]);
  return {
    snapshot_node_pool,
    snapshot_latency_global,
  };
}

export async function getDashboardPlatformRealtimeData(platformId: string, window: TimeWindow): Promise<DashboardPlatformRealtimeData> {
  const realtime_leases = await getRealtimeLeases(window, platformId);
  return {
    realtime_leases,
  };
}

export async function getDashboardPlatformHistoryData(
  platformId: string,
  window: TimeWindow,
  previous?: DashboardPlatformHistoryData,
): Promise<DashboardPlatformHistoryData> {
  const leaseWindow = buildIncrementalWindow(
    window,
    previous?.history_lease_lifetime.items,
    previous?.history_lease_lifetime.bucket_seconds,
  );
  const nextLeaseLifetime = await getHistoryLeaseLifetime(platformId, leaseWindow);
  return {
    history_lease_lifetime: {
      platform_id: toString(nextLeaseLifetime.platform_id) || previous?.history_lease_lifetime.platform_id || platformId,
      bucket_seconds: pickPositiveOrFallback(
        nextLeaseLifetime.bucket_seconds,
        previous?.history_lease_lifetime.bucket_seconds,
      ),
      items: mergeWindowedHistoryItems(window, previous?.history_lease_lifetime.items, nextLeaseLifetime.items),
    },
  };
}

export async function getDashboardPlatformSnapshotData(platformId: string): Promise<DashboardPlatformSnapshotData> {
  const [snapshot_platform_node_pool, snapshot_latency_platform] = await Promise.all([
    getSnapshotPlatformNodePool(platformId),
    getSnapshotLatency(platformId),
  ]);

  return {
    snapshot_platform_node_pool,
    snapshot_latency_platform,
  };
}

export async function getDashboardGlobalData(window: TimeWindow): Promise<DashboardGlobalData> {
  const [realtime, history, snapshot] = await Promise.all([
    getDashboardGlobalRealtimeData(window),
    getDashboardGlobalHistoryData(window),
    getDashboardGlobalSnapshotData(),
  ]);

  return {
    ...realtime,
    ...history,
    ...snapshot,
  };
}

export async function getDashboardPlatformData(platformId: string, window: TimeWindow): Promise<DashboardPlatformData> {
  const [realtime, history, snapshot] = await Promise.all([
    getDashboardPlatformRealtimeData(platformId, window),
    getDashboardPlatformHistoryData(platformId, window),
    getDashboardPlatformSnapshotData(platformId),
  ]);

  return {
    ...realtime,
    ...history,
    ...snapshot,
  };
}
