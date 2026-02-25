import { useQuery } from "@tanstack/react-query";
import { Activity, AlertTriangle, Clock3, Layers, ShieldCheck, Waypoints } from "lucide-react";
import { useMemo, useState } from "react";
import { Bar, BarChart, CartesianGrid, ComposedChart, Line, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { Badge } from "../../components/ui/Badge";
import { Card } from "../../components/ui/Card";
import { Select } from "../../components/ui/Select";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { ApiError, apiRequest } from "../../lib/api-client";
import type {
  HistoryAccessLatencyResponse,
  HistoryLeaseLifetimeResponse,
  HistoryRequestsItem,
  HistoryResponse,
  LatencyBucket,
  RealtimeLeasesResponse,
  SnapshotNodeLatencyDistribution,
  SnapshotPlatformNodePool,
  TimeWindow,
} from "../dashboard/types";
import type { Platform } from "./types";

type RangeKey = "1h" | "6h" | "24h";

type RangeOption = {
  key: RangeKey;
  label: string;
  ms: number;
};

type PlatformHistoryBundle = {
  requests: HistoryResponse<HistoryRequestsItem>;
  accessLatency: HistoryAccessLatencyResponse;
  leaseLifetime: HistoryLeaseLifetimeResponse;
};

type PlatformSnapshotBundle = {
  nodePool: SnapshotPlatformNodePool;
  nodeLatency: SnapshotNodeLatencyDistribution;
};

type TrendLineDefinition = {
  dataKey: string;
  name: string;
  color: string;
};

type TrendLineChartProps = {
  data: Array<Record<string, number | string>>;
  lines: TrendLineDefinition[];
  yTickFormatter?: (value: number) => string;
  emptyText: string;
};

type HistogramBarPoint = {
  lower_ms: number;
  upper_ms: number;
  label: string;
  count: number;
};

type LatencyHistogramProps = {
  buckets: LatencyBucket[];
  emptyText: string;
};

const RANGE_OPTIONS: RangeOption[] = [
  { key: "1h", label: "最近 1 小时", ms: 60 * 60 * 1000 },
  { key: "6h", label: "最近 6 小时", ms: 6 * 60 * 60 * 1000 },
  { key: "24h", label: "最近 24 小时", ms: 24 * 60 * 60 * 1000 },
];

const DEFAULT_REALTIME_REFRESH_MS = 15_000;
const MIN_REALTIME_REFRESH_MS = 1_000;
const DEFAULT_HISTORY_REFRESH_MS = 60_000;
const MIN_HISTORY_REFRESH_MS = 15_000;
const MAX_HISTORY_REFRESH_MS = 300_000;
const SNAPSHOT_REFRESH_MS = 5_000;
const MAX_TREND_POINTS = 360;
const MAX_HISTOGRAM_BUCKETS = 120;
const METRICS_BASE = "/api/v1/metrics";
const EMPTY_REALTIME_ITEMS: RealtimeLeasesResponse["items"] = [];
const EMPTY_HISTORY_REQUEST_ITEMS: HistoryResponse<HistoryRequestsItem>["items"] = [];
const EMPTY_ACCESS_LATENCY_ITEMS: HistoryAccessLatencyResponse["items"] = [];
const EMPTY_LEASE_LIFETIME_ITEMS: HistoryLeaseLifetimeResponse["items"] = [];

function fromApiError(error: unknown): string {
  if (error instanceof ApiError) {
    return `${error.code}: ${error.message}`;
  }
  if (error instanceof Error) {
    return error.message;
  }
  return "未知错误";
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

function numberLocale(): string {
  return isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN";
}

function formatCount(value: number): string {
  return new Intl.NumberFormat(numberLocale()).format(Math.round(value));
}

function formatPercent(value: number): string {
  return `${(value * 100).toFixed(1)}%`;
}

function formatShortNumber(value: number): string {
  const abs = Math.abs(value);
  if (abs >= 1_000_000_000) {
    return `${(value / 1_000_000_000).toFixed(1)}G`;
  }
  if (abs >= 1_000_000) {
    return `${(value / 1_000_000).toFixed(1)}M`;
  }
  if (abs >= 1_000) {
    return `${(value / 1_000).toFixed(1)}K`;
  }
  return `${Math.round(value)}`;
}

function formatPercentAxis(value: number): string {
  return `${Math.round(value)}%`;
}

function formatLatency(value: number): string {
  if (!Number.isFinite(value) || value < 0) {
    return "0ms";
  }
  if (value >= 1000) {
    const seconds = value / 1000;
    return `${seconds >= 10 ? seconds.toFixed(0) : seconds.toFixed(1)}s`;
  }
  return `${Math.round(value)}ms`;
}

function formatClock(iso: string): string {
  if (!iso) {
    return "--";
  }
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) {
    return "--";
  }
  return new Intl.DateTimeFormat(numberLocale(), {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(date);
}

function parseTimestamp(input: string, fallback: number): number {
  if (!input) {
    return fallback;
  }
  const value = Date.parse(input);
  if (Number.isNaN(value)) {
    return fallback;
  }
  return value;
}

function sortTimeSeriesByTimestamp<T>(items: T[], getTimestamp: (item: T) => string): T[] {
  return items
    .map((item, index) => ({
      item,
      index,
      sortKey: parseTimestamp(getTimestamp(item), index),
    }))
    .sort((left, right) => {
      if (left.sortKey === right.sortKey) {
        return left.index - right.index;
      }
      return left.sortKey - right.sortKey;
    })
    .map((entry) => entry.item);
}

function buildUniformSampleIndices(pointCount: number, maxPoints: number): number[] {
  if (pointCount <= 0 || maxPoints <= 0) {
    return [];
  }

  const capped = Math.min(pointCount, maxPoints);
  if (capped === pointCount) {
    return Array.from({ length: pointCount }, (_, index) => index);
  }
  if (capped === 1) {
    return [0];
  }
  if (capped === 2) {
    return [0, pointCount - 1];
  }

  const middleCount = capped - 2;
  const span = pointCount - 2;
  const selected = new Set<number>([0, pointCount - 1]);

  for (let i = 0; i < middleCount; i += 1) {
    const index = 1 + Math.floor((i * span) / middleCount);
    selected.add(Math.min(pointCount - 2, index));
  }

  return Array.from(selected).sort((a, b) => a - b);
}

function downsampleArray<T>(items: T[], maxPoints: number): T[] {
  if (items.length <= maxPoints) {
    return items;
  }
  const indices = buildUniformSampleIndices(items.length, maxPoints);
  return indices.map((index) => items[index] ?? items[items.length - 1]);
}

function sum(values: number[]): number {
  return values.reduce((acc, value) => acc + value, 0);
}

function successRate(total: number, success: number): number {
  if (total <= 0) {
    return 0;
  }
  return success / total;
}

function latestValue(values: number[]): number {
  if (!values.length) {
    return 0;
  }
  return values[values.length - 1] ?? 0;
}

function normalizePositiveSeconds(seconds: number | undefined): number | null {
  if (typeof seconds !== "number" || !Number.isFinite(seconds) || seconds <= 0) {
    return null;
  }
  return seconds;
}

function realtimeRefreshMs(stepSeconds: number | undefined): number {
  const step = normalizePositiveSeconds(stepSeconds);
  if (step === null) {
    return DEFAULT_REALTIME_REFRESH_MS;
  }
  return Math.max(MIN_REALTIME_REFRESH_MS, Math.round(step * 1000));
}

function historyRefreshMs(bucketSeconds: Array<number | undefined>): number {
  const buckets = bucketSeconds.map(normalizePositiveSeconds).filter((value): value is number => value !== null);
  if (!buckets.length) {
    return DEFAULT_HISTORY_REFRESH_MS;
  }
  const intervalMs = Math.round(Math.min(...buckets) * 1000);
  return Math.min(MAX_HISTORY_REFRESH_MS, Math.max(MIN_HISTORY_REFRESH_MS, intervalMs));
}

function getTimeWindow(rangeKey: RangeKey): TimeWindow {
  const option = RANGE_OPTIONS.find((item) => item.key === rangeKey) ?? RANGE_OPTIONS[1];
  const to = new Date();
  const from = new Date(to.getTime() - option.ms);
  return {
    from: from.toISOString(),
    to: to.toISOString(),
  };
}

function withWindow(path: string, window: TimeWindow, params?: Record<string, string>): string {
  const query = new URLSearchParams({
    from: window.from,
    to: window.to,
    ...(params ?? {}),
  });
  return `${path}?${query.toString()}`;
}

function normalizeRealtimeLeasesResponse(raw: RealtimeLeasesResponse): RealtimeLeasesResponse {
  return {
    platform_id: toString(raw.platform_id),
    step_seconds: toNumber(raw.step_seconds),
    items: Array.isArray(raw.items)
      ? raw.items.map((item) => ({
        ts: toString(item.ts),
        active_leases: toNumber(item.active_leases),
      }))
      : [],
  };
}

function normalizeHistoryRequestsResponse(raw: HistoryResponse<HistoryRequestsItem>): HistoryResponse<HistoryRequestsItem> {
  return {
    bucket_seconds: toNumber(raw.bucket_seconds),
    items: Array.isArray(raw.items)
      ? raw.items.map((item) => ({
        bucket_start: toString(item.bucket_start),
        bucket_end: toString(item.bucket_end),
        total_requests: toNumber(item.total_requests),
        success_requests: toNumber(item.success_requests),
        success_rate: toNumber(item.success_rate),
      }))
      : [],
  };
}

function normalizeHistoryAccessLatencyResponse(raw: HistoryAccessLatencyResponse): HistoryAccessLatencyResponse {
  return {
    bucket_seconds: toNumber(raw.bucket_seconds),
    bin_width_ms: toNumber(raw.bin_width_ms),
    overflow_ms: toNumber(raw.overflow_ms),
    items: Array.isArray(raw.items)
      ? raw.items.map((item) => ({
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

function normalizeHistoryLeaseLifetimeResponse(raw: HistoryLeaseLifetimeResponse): HistoryLeaseLifetimeResponse {
  return {
    platform_id: toString(raw.platform_id),
    bucket_seconds: toNumber(raw.bucket_seconds),
    items: Array.isArray(raw.items)
      ? raw.items.map((item) => ({
        bucket_start: toString(item.bucket_start),
        bucket_end: toString(item.bucket_end),
        sample_count: toNumber(item.sample_count),
        p1_ms: toNumber(item.p1_ms),
        p5_ms: toNumber(item.p5_ms),
        p50_ms: toNumber(item.p50_ms),
      }))
      : [],
  };
}

function normalizePlatformNodePoolSnapshot(raw: SnapshotPlatformNodePool): SnapshotPlatformNodePool {
  return {
    generated_at: toString(raw.generated_at),
    platform_id: toString(raw.platform_id),
    routable_node_count: toNumber(raw.routable_node_count),
    egress_ip_count: toNumber(raw.egress_ip_count),
  };
}

function normalizeNodeLatencySnapshot(raw: SnapshotNodeLatencyDistribution): SnapshotNodeLatencyDistribution {
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

async function fetchPlatformRealtimeLeases(platformId: string, window: TimeWindow): Promise<RealtimeLeasesResponse> {
  const data = await apiRequest<RealtimeLeasesResponse>(
    withWindow(`${METRICS_BASE}/realtime/leases`, window, { platform_id: platformId }),
  );
  return normalizeRealtimeLeasesResponse(data);
}

async function fetchPlatformHistoryRequests(
  platformId: string,
  window: TimeWindow,
): Promise<HistoryResponse<HistoryRequestsItem>> {
  const data = await apiRequest<HistoryResponse<HistoryRequestsItem>>(
    withWindow(`${METRICS_BASE}/history/requests`, window, { platform_id: platformId }),
  );
  return normalizeHistoryRequestsResponse(data);
}

async function fetchPlatformHistoryAccessLatency(
  platformId: string,
  window: TimeWindow,
): Promise<HistoryAccessLatencyResponse> {
  const data = await apiRequest<HistoryAccessLatencyResponse>(
    withWindow(`${METRICS_BASE}/history/access-latency`, window, { platform_id: platformId }),
  );
  return normalizeHistoryAccessLatencyResponse(data);
}

async function fetchPlatformHistoryLeaseLifetime(
  platformId: string,
  window: TimeWindow,
): Promise<HistoryLeaseLifetimeResponse> {
  const data = await apiRequest<HistoryLeaseLifetimeResponse>(
    withWindow(`${METRICS_BASE}/history/lease-lifetime`, window, { platform_id: platformId }),
  );
  return normalizeHistoryLeaseLifetimeResponse(data);
}

async function fetchPlatformSnapshotNodePool(platformId: string): Promise<SnapshotPlatformNodePool> {
  const query = new URLSearchParams({ platform_id: platformId });
  const data = await apiRequest<SnapshotPlatformNodePool>(`${METRICS_BASE}/snapshots/platform-node-pool?${query.toString()}`);
  return normalizePlatformNodePoolSnapshot(data);
}

async function fetchPlatformSnapshotNodeLatency(platformId: string): Promise<SnapshotNodeLatencyDistribution> {
  const query = new URLSearchParams({ platform_id: platformId });
  const data = await apiRequest<SnapshotNodeLatencyDistribution>(
    `${METRICS_BASE}/snapshots/node-latency-distribution?${query.toString()}`,
  );
  return normalizeNodeLatencySnapshot(data);
}

type TrendTooltipContentProps = {
  active?: boolean;
  payload?: any[];
  label?: string;
  lines: TrendLineDefinition[];
  valueFormatter: (value: number) => string;
};

function TrendTooltipContent({ active, payload, label, lines, valueFormatter }: TrendTooltipContentProps) {
  if (!active || !payload?.length) {
    return null;
  }

  return (
    <div className="trend-tooltip">
      <p className="trend-tooltip-time">{label ?? "--"}</p>
      <div className="trend-tooltip-list">
        {lines.map((line) => {
          const entry = payload.find((item) => item.dataKey === line.dataKey);
          const value = Number(entry?.value ?? 0);
          const safeValue = Number.isFinite(value) ? value : 0;

          return (
            <p key={line.dataKey} className="trend-tooltip-row">
              <span>
                <i style={{ background: line.color }} />
                {line.name}
              </span>
              <b>{valueFormatter(safeValue)}</b>
            </p>
          );
        })}
      </div>
    </div>
  );
}

function RequestQualityTooltipContent({ active, payload, label }: any) {
  if (!active || !payload?.length) {
    return null;
  }

  return (
    <div className="trend-tooltip">
      <p className="trend-tooltip-time">{label ?? "--"}</p>
      <div className="trend-tooltip-list">
        {payload.map((entry: any) => {
          const isRate = entry.dataKey === "success_rate";
          const valueStr = isRate ? `${Number(entry.value).toFixed(1)}%` : formatCount(Number(entry.value));

          return (
            <p key={entry.dataKey} className="trend-tooltip-row">
              <span>
                <i style={{ background: entry.color }} />
                {entry.name}
              </span>
              <b>{valueStr}</b>
            </p>
          );
        })}
      </div>
    </div>
  );
}

function HistogramTooltipContent({ active, payload }: any) {
  if (!active || !payload?.length) {
    return null;
  }

  const point = payload[0]?.payload as HistogramBarPoint | undefined;
  const count = Number(payload[0]?.value ?? point?.count ?? 0);
  const safeCount = Number.isFinite(count) ? count : 0;
  const lowerBound = typeof point?.lower_ms === "number" && Number.isFinite(point.lower_ms) ? point.lower_ms : 0;
  const upperBound = typeof point?.upper_ms === "number" && Number.isFinite(point.upper_ms) ? point.upper_ms : 0;

  return (
    <div className="histogram-tooltip">
      <p className="histogram-tooltip-title">{`${formatCount(lowerBound)}～${formatCount(upperBound)} ms`}</p>
      <p className="histogram-tooltip-value">{`节点数 ${formatCount(safeCount)}`}</p>
    </div>
  );
}

function EmptyChart({ text }: { text: string }) {
  return (
    <div className="empty-box dashboard-empty">
      <AlertTriangle size={14} />
      <p>{text}</p>
    </div>
  );
}

function TrendLineChart({ data, lines, yTickFormatter, emptyText }: TrendLineChartProps) {
  if (!data.length || !lines.length) {
    return <EmptyChart text={emptyText} />;
  }

  const formatYAxis = yTickFormatter ?? formatShortNumber;

  return (
    <div className="trend-chart">
      <div className="trend-svg">
        <ResponsiveContainer width="100%" height="100%">
          <ComposedChart data={data} margin={{ top: 6, right: 8, bottom: 4, left: 8 }}>
            <CartesianGrid stroke="rgba(65, 87, 121, 0.16)" strokeDasharray="2 4" vertical={false} />
            <XAxis
              dataKey="label"
              interval="preserveStartEnd"
              minTickGap={18}
              tickMargin={4}
              axisLine={false}
              tickLine={false}
              tick={{ fill: "#607191", fontSize: 11, fontWeight: 600 }}
            />
            <YAxis
              width="auto"
              tickMargin={4}
              axisLine={false}
              tickLine={false}
              tick={{ fill: "#657691", fontSize: 11, fontWeight: 600 }}
              tickFormatter={(value) => formatYAxis(toNumber(value))}
              domain={[0, "auto"]}
            />
            <Tooltip
              cursor={{ stroke: "rgba(15, 94, 216, 0.34)", strokeWidth: 1 }}
              wrapperStyle={{ outline: "none" }}
              content={<TrendTooltipContent lines={lines} valueFormatter={formatYAxis} />}
            />
            {lines.map((line) => (
              <Line
                key={line.dataKey}
                type="monotone"
                dataKey={line.dataKey}
                name={line.name}
                stroke={line.color}
                strokeWidth={1.8}
                dot={false}
                activeDot={{ r: 3, stroke: "#ffffff", strokeWidth: 1, fill: line.color }}
                isAnimationActive={false}
                connectNulls
              />
            ))}
          </ComposedChart>
        </ResponsiveContainer>
      </div>
    </div>
  );
}

function RequestQualityChart({
  data,
  emptyText,
}: {
  data: Array<{ label: string; total_requests: number; success_rate: number }>;
  emptyText: string;
}) {
  if (!data.length) {
    return <EmptyChart text={emptyText} />;
  }

  return (
    <div className="trend-chart">
      <div className="trend-svg">
        <ResponsiveContainer width="100%" height="100%">
          <ComposedChart data={data} margin={{ top: 6, right: 8, bottom: 4, left: 8 }}>
            <CartesianGrid stroke="rgba(65, 87, 121, 0.16)" strokeDasharray="2 4" vertical={false} />
            <XAxis
              dataKey="label"
              interval="preserveStartEnd"
              minTickGap={18}
              tickMargin={4}
              axisLine={false}
              tickLine={false}
              tick={{ fill: "#607191", fontSize: 11, fontWeight: 600 }}
            />
            <YAxis
              yAxisId="left"
              width="auto"
              tickMargin={4}
              axisLine={false}
              tickLine={false}
              tick={{ fill: "#657691", fontSize: 11, fontWeight: 600 }}
              tickFormatter={(value) => formatShortNumber(toNumber(value))}
              domain={[0, "auto"]}
            />
            <YAxis
              yAxisId="right"
              orientation="right"
              width="auto"
              tickMargin={4}
              axisLine={false}
              tickLine={false}
              tick={{ fill: "#657691", fontSize: 11, fontWeight: 600 }}
              tickFormatter={(value) => formatPercentAxis(toNumber(value))}
              domain={[0, 100]}
            />
            <Tooltip
              cursor={{ stroke: "rgba(15, 94, 216, 0.34)", strokeWidth: 1 }}
              wrapperStyle={{ outline: "none" }}
              content={<RequestQualityTooltipContent />}
            />
            <Bar
              yAxisId="left"
              dataKey="total_requests"
              name="请求总数"
              fill="rgba(37, 108, 233, 0.76)"
              radius={[4, 4, 0, 0]}
              maxBarSize={24}
              isAnimationActive={false}
            />
            <Line
              yAxisId="right"
              type="monotone"
              dataKey="success_rate"
              name="成功率"
              stroke="#0f9d8b"
              strokeWidth={1.8}
              dot={false}
              activeDot={{ r: 3, stroke: "#ffffff", strokeWidth: 1, fill: "#0f9d8b" }}
              isAnimationActive={false}
              connectNulls
            />
          </ComposedChart>
        </ResponsiveContainer>
      </div>
    </div>
  );
}

function compressHistogram(buckets: LatencyBucket[], limit = MAX_HISTOGRAM_BUCKETS): LatencyBucket[] {
  if (buckets.length <= limit) {
    return buckets;
  }

  const chunkSize = Math.ceil(buckets.length / limit);
  const grouped: LatencyBucket[] = [];

  for (let index = 0; index < buckets.length; index += chunkSize) {
    const chunk = buckets.slice(index, index + chunkSize);
    const total = chunk.reduce((acc, item) => acc + item.count, 0);
    grouped.push({
      le_ms: chunk[chunk.length - 1]?.le_ms ?? 0,
      count: total,
    });
  }

  return grouped;
}

function toHistogramBarPoints(buckets: LatencyBucket[]): HistogramBarPoint[] {
  const compact = compressHistogram(buckets);
  const inferredStep = compact.length >= 2 ? Math.max(1, compact[1].le_ms - compact[0].le_ms) : 0;
  const isLegacyUpperInclusive = compact.length > 0 && inferredStep > 0 && compact[0].le_ms === inferredStep;

  return compact.reduce<{ points: HistogramBarPoint[]; previousUpper: number }>(
    (acc, bucket) => {
      const lower = Math.max(0, acc.previousUpper + 1);
      const rawUpper = isLegacyUpperInclusive ? bucket.le_ms - 1 : bucket.le_ms;
      const upperInclusive = Math.max(lower, rawUpper);

      return {
        previousUpper: upperInclusive,
        points: [
          ...acc.points,
          {
            lower_ms: lower,
            upper_ms: upperInclusive,
            label: `${lower}`,
            count: Math.max(0, bucket.count),
          },
        ],
      };
    },
    { points: [], previousUpper: -1 },
  ).points;
}

function LatencyHistogram({ buckets, emptyText }: LatencyHistogramProps) {
  if (!buckets.length) {
    return <EmptyChart text={emptyText} />;
  }

  const data = toHistogramBarPoints(buckets);
  if (!data.length) {
    return <EmptyChart text={emptyText} />;
  }

  return (
    <div className="histogram-chart">
      <ResponsiveContainer width="100%" height="100%">
        <BarChart data={data} margin={{ top: 6, right: 8, bottom: 4, left: 8 }}>
          <CartesianGrid stroke="rgba(65, 87, 121, 0.16)" strokeDasharray="2 4" vertical={false} />
          <XAxis
            dataKey="label"
            interval="preserveStartEnd"
            minTickGap={14}
            tickMargin={4}
            axisLine={false}
            tickLine={false}
            tick={{ fill: "#607191", fontSize: 11, fontWeight: 600 }}
            tickFormatter={(value) => formatLatency(toNumber(value))}
          />
          <YAxis
            width="auto"
            allowDecimals={false}
            tickMargin={4}
            axisLine={false}
            tickLine={false}
            tick={{ fill: "#607191", fontSize: 11, fontWeight: 600 }}
            tickFormatter={(value) => formatShortNumber(toNumber(value))}
          />
          <Tooltip
            cursor={{ fill: "rgba(15, 94, 216, 0.08)" }}
            wrapperStyle={{ outline: "none" }}
            content={<HistogramTooltipContent />}
          />
          <Bar
            dataKey="count"
            fill="rgba(16, 118, 255, 0.86)"
            radius={[5, 5, 0, 0]}
            maxBarSize={28}
            isAnimationActive={false}
          />
        </BarChart>
      </ResponsiveContainer>
    </div>
  );
}

export function PlatformMonitorPanel({ platform }: { platform: Platform }) {
  const { locale } = useI18n();
  const [rangeKey, setRangeKey] = useState<RangeKey>("6h");

  const realtimeQuery = useQuery({
    queryKey: ["platform-monitor", "realtime-leases", platform.id, rangeKey],
    queryFn: async () => {
      const window = getTimeWindow(rangeKey);
      return fetchPlatformRealtimeLeases(platform.id, window);
    },
    refetchInterval: (query) => {
      const data = query.state.data as RealtimeLeasesResponse | undefined;
      return realtimeRefreshMs(data?.step_seconds);
    },
    placeholderData: (previous) => previous,
  });

  const historyQuery = useQuery({
    queryKey: ["platform-monitor", "history", platform.id, rangeKey],
    queryFn: async () => {
      const window = getTimeWindow(rangeKey);
      const [requests, accessLatency, leaseLifetime] = await Promise.all([
        fetchPlatformHistoryRequests(platform.id, window),
        fetchPlatformHistoryAccessLatency(platform.id, window),
        fetchPlatformHistoryLeaseLifetime(platform.id, window),
      ]);
      return {
        requests,
        accessLatency,
        leaseLifetime,
      } satisfies PlatformHistoryBundle;
    },
    refetchInterval: (query) => {
      const data = query.state.data as PlatformHistoryBundle | undefined;
      return historyRefreshMs([
        data?.requests.bucket_seconds,
        data?.accessLatency.bucket_seconds,
        data?.leaseLifetime.bucket_seconds,
      ]);
    },
    placeholderData: (previous) => previous,
  });

  const snapshotQuery = useQuery({
    queryKey: ["platform-monitor", "snapshots", platform.id],
    queryFn: async () => {
      const [nodePool, nodeLatency] = await Promise.all([
        fetchPlatformSnapshotNodePool(platform.id),
        fetchPlatformSnapshotNodeLatency(platform.id),
      ]);
      return {
        nodePool,
        nodeLatency,
      } satisfies PlatformSnapshotBundle;
    },
    refetchInterval: SNAPSHOT_REFRESH_MS,
    placeholderData: (previous) => previous,
  });

  const monitorError = realtimeQuery.error ?? historyQuery.error ?? snapshotQuery.error;

  const isInitialLoading =
    !realtimeQuery.data &&
    !historyQuery.data &&
    !snapshotQuery.data &&
    (realtimeQuery.isLoading || historyQuery.isLoading || snapshotQuery.isLoading);

  const realtimeItems = realtimeQuery.data?.items ?? EMPTY_REALTIME_ITEMS;
  const sortedRealtimeItems = useMemo(
    () => sortTimeSeriesByTimestamp(realtimeItems, (item) => item.ts),
    [realtimeItems],
  );

  const requestsItems = historyQuery.data?.requests.items ?? EMPTY_HISTORY_REQUEST_ITEMS;
  const sortedRequestsItems = useMemo(
    () => sortTimeSeriesByTimestamp(requestsItems, (item) => item.bucket_start),
    [requestsItems],
  );

  const accessLatencyItems = historyQuery.data?.accessLatency.items ?? EMPTY_ACCESS_LATENCY_ITEMS;
  const sortedAccessLatencyItems = useMemo(
    () => sortTimeSeriesByTimestamp(accessLatencyItems, (item) => item.bucket_start),
    [accessLatencyItems],
  );

  const leaseLifetimeItems = historyQuery.data?.leaseLifetime.items ?? EMPTY_LEASE_LIFETIME_ITEMS;
  const sortedLeaseLifetimeItems = useMemo(
    () => sortTimeSeriesByTimestamp(leaseLifetimeItems, (item) => item.bucket_start),
    [leaseLifetimeItems],
  );

  const latestActiveLeases = latestValue(sortedRealtimeItems.map((item) => item.active_leases));
  const totalRequests = sum(sortedRequestsItems.map((item) => item.total_requests));
  const successRequests = sum(sortedRequestsItems.map((item) => item.success_requests));
  const requestSuccessRatio = successRate(totalRequests, successRequests);

  const latestLeaseLifetime = sortedLeaseLifetimeItems[sortedLeaseLifetimeItems.length - 1];
  const latestP50LeaseMs = latestLeaseLifetime?.p50_ms ?? 0;

  const latestAccessLatency = sortedAccessLatencyItems[sortedAccessLatencyItems.length - 1];

  const snapshotNodePool = snapshotQuery.data?.nodePool;
  const snapshotLatency = snapshotQuery.data?.nodeLatency;

  const leaseTrendData = useMemo(() => {
    return downsampleArray(sortedRealtimeItems, MAX_TREND_POINTS).map((item) => ({
      label: formatClock(item.ts),
      active_leases: item.active_leases,
    }));
  }, [sortedRealtimeItems, locale]);

  const requestTrendData = useMemo(() => {
    return downsampleArray(sortedRequestsItems, MAX_TREND_POINTS).map((item) => ({
      label: formatClock(item.bucket_start),
      total_requests: item.total_requests,
      success_rate: item.success_rate * 100,
    }));
  }, [sortedRequestsItems, locale]);

  const leaseLifetimeTrendData = useMemo(() => {
    return downsampleArray(sortedLeaseLifetimeItems, MAX_TREND_POINTS).map((item) => ({
      label: formatClock(item.bucket_start),
      p1_ms: item.p1_ms,
      p5_ms: item.p5_ms,
      p50_ms: item.p50_ms,
    }));
  }, [sortedLeaseLifetimeItems, locale]);

  return (
    <section className="platform-drawer-section platform-monitor-section">
      <div className="platform-drawer-section-head platform-monitor-head">
        <div>
          <h4>平台监控</h4>
          <p>查看当前平台的租约、请求成功率、延迟和节点情况。</p>
        </div>

        <label className="platform-monitor-range" htmlFor="platform-monitor-range">
          <span>时间范围</span>
          <Select
            id="platform-monitor-range"
            value={rangeKey}
            onChange={(event) => setRangeKey(event.target.value as RangeKey)}
          >
            {RANGE_OPTIONS.map((option) => (
              <option key={option.key} value={option.key}>
                {option.label}
              </option>
            ))}
          </Select>
        </label>
      </div>

      {monitorError ? (
        <div className="callout callout-error">
          <AlertTriangle size={14} />
          <span>{fromApiError(monitorError)}</span>
        </div>
      ) : null}

      <div className="platform-monitor-kpi-grid">
        <Card className="platform-monitor-kpi-card">
          <div className="dashboard-kpi-icon lease">
            <Layers size={16} />
          </div>
          <div>
            <p className="platform-monitor-kpi-label">活跃租约</p>
            <p className="platform-monitor-kpi-value">{formatCount(latestActiveLeases)}</p>
            <p className="platform-monitor-kpi-sub">当前实时值</p>
          </div>
        </Card>

        <Card className="platform-monitor-kpi-card">
          <div className="dashboard-kpi-icon shield">
            <ShieldCheck size={16} />
          </div>
          <div>
            <p className="platform-monitor-kpi-label">请求成功率</p>
            <p className="platform-monitor-kpi-value">{formatPercent(requestSuccessRatio)}</p>
            <p className="platform-monitor-kpi-sub">
              成功 {formatCount(successRequests)} / 总计 {formatCount(totalRequests)}
            </p>
          </div>
        </Card>

        <Card className="platform-monitor-kpi-card">
          <div className="dashboard-kpi-icon gauge">
            <Waypoints size={16} />
          </div>
          <div>
            <p className="platform-monitor-kpi-label">可路由节点</p>
            <p className="platform-monitor-kpi-value">{formatCount(snapshotNodePool?.routable_node_count ?? 0)}</p>
            <p className="platform-monitor-kpi-sub">出口 IP {formatCount(snapshotNodePool?.egress_ip_count ?? 0)}</p>
          </div>
        </Card>

        <Card className="platform-monitor-kpi-card">
          <div className="dashboard-kpi-icon waves">
            <Clock3 size={16} />
          </div>
          <div>
            <p className="platform-monitor-kpi-label">租约 P50 存活时长</p>
            <p className="platform-monitor-kpi-value">{formatLatency(latestP50LeaseMs)}</p>
            <p className="platform-monitor-kpi-sub">历史租约时长统计</p>
          </div>
        </Card>
      </div>

      <div className="platform-monitor-grid">
        <Card className="dashboard-panel">
          <div className="dashboard-panel-header">
            <h3>活跃租约趋势</h3>
            <p>平台实时租约数量</p>
          </div>
          <TrendLineChart
            data={leaseTrendData}
            emptyText="暂无租约实时数据"
            yTickFormatter={formatShortNumber}
            lines={[{ dataKey: "active_leases", name: "活跃租约", color: "#2068f6" }]}
          />
        </Card>

        <Card className="dashboard-panel">
          <div className="dashboard-panel-header">
            <h3>请求质量</h3>
            <p>请求总量 + 成功率</p>
          </div>
          <RequestQualityChart data={requestTrendData} emptyText="暂无请求统计数据" />
          <div className="dashboard-summary-inline">
            <span>总请求 {formatCount(totalRequests)}</span>
            <span>成功率 {formatPercent(requestSuccessRatio)}</span>
          </div>
        </Card>

        <Card className="dashboard-panel">
          <div className="dashboard-panel-header">
            <h3>租约存活分位趋势</h3>
            <p>P1 / P5 / P50 (ms)</p>
          </div>
          <TrendLineChart
            data={leaseLifetimeTrendData}
            emptyText="暂无租约生命周期数据"
            yTickFormatter={formatLatency}
            lines={[
              { dataKey: "p1_ms", name: "P1", color: "#2d63d8" },
              { dataKey: "p5_ms", name: "P5", color: "#0f9d8b" },
              { dataKey: "p50_ms", name: "P50", color: "#f18f01" },
            ]}
          />
        </Card>

        <Card className="dashboard-panel">
          <div className="dashboard-panel-header">
            <h3>平台节点快照</h3>
            <p>当前平台节点池与延迟样本</p>
          </div>

          <div className="platform-monitor-snapshot-list">
            <div>
              <span>可路由节点数</span>
              <p>{formatCount(snapshotNodePool?.routable_node_count ?? 0)}</p>
            </div>
            <div>
              <span>出口 IP 数</span>
              <p>{formatCount(snapshotNodePool?.egress_ip_count ?? 0)}</p>
            </div>
            <div>
              <span>延迟样本数</span>
              <p>{formatCount(snapshotLatency?.sample_count ?? 0)}</p>
            </div>
            <div>
              <span>快照更新时间</span>
              <p>{snapshotLatency?.generated_at ? formatClock(snapshotLatency.generated_at) : "--"}</p>
            </div>
          </div>
        </Card>

        <Card className="dashboard-panel platform-monitor-span-2">
          <div className="dashboard-panel-header">
            <h3>访问延迟分布（历史最新桶）</h3>
            <p>历史访问延迟分布</p>
          </div>
          <LatencyHistogram buckets={latestAccessLatency?.buckets ?? []} emptyText="暂无访问延迟分布数据" />
          <div className="dashboard-summary-inline">
            <span>时间 {latestAccessLatency ? formatClock(latestAccessLatency.bucket_end) : "--"}</span>
            <span>样本 {formatCount(latestAccessLatency?.sample_count ?? 0)}</span>
            <span>溢出 {formatCount(latestAccessLatency?.overflow_count ?? 0)}</span>
          </div>
        </Card>

        <Card className="dashboard-panel platform-monitor-span-2">
          <div className="dashboard-panel-header">
            <h3>节点延迟分布（实时快照）</h3>
            <p>实时节点延迟分布快照</p>
          </div>
          <LatencyHistogram buckets={snapshotLatency?.buckets ?? []} emptyText="暂无节点延迟快照数据" />
          <div className="dashboard-summary-inline">
            <span>样本 {formatCount(snapshotLatency?.sample_count ?? 0)}</span>
            <span>溢出 {formatCount(snapshotLatency?.overflow_count ?? 0)}</span>
            <span>分桶 {formatCount(snapshotLatency?.bin_width_ms ?? 0)}ms</span>
          </div>
        </Card>
      </div>

      {isInitialLoading ? (
        <div className="callout callout-warning">
          <Activity size={14} />
          <span>平台监控数据加载中...</span>
        </div>
      ) : null}

      {(realtimeQuery.isFetching || historyQuery.isFetching || snapshotQuery.isFetching) && !isInitialLoading ? (
        <div className="platform-monitor-refreshing">
          <Badge variant="warning">监控数据刷新中</Badge>
        </div>
      ) : null}
    </section>
  );
}
