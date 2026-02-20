import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, Gauge, Layers, Server, Shield, Waves } from "lucide-react";
import { useId, useMemo, useState } from "react";
import { Area, Bar, BarChart, CartesianGrid, ComposedChart, Line, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { Badge } from "../../components/ui/Badge";
import { Card } from "../../components/ui/Card";
import { Select } from "../../components/ui/Select";
import { ApiError } from "../../lib/api-client";
import { formatDateTime } from "../../lib/time";
import { listPlatforms } from "../platforms/api";
import {
  getDashboardGlobalHistoryData,
  getDashboardGlobalRealtimeData,
  getDashboardGlobalSnapshotData,
  getDashboardPlatformHistoryData,
  getDashboardPlatformRealtimeData,
  getDashboardPlatformSnapshotData,
} from "./api";
import type { DashboardGlobalData, DashboardPlatformData, LatencyBucket, TimeWindow } from "./types";

type RangeKey = "15m" | "1h" | "6h" | "24h";

type RangeOption = {
  key: RangeKey;
  label: string;
  ms: number;
};

type ChartSeries = {
  name: string;
  values: number[];
  color: string;
  fillColor?: string;
};

type TrendChartProps = {
  labels: string[];
  series: ChartSeries[];
  formatYAxisLabel?: (value: number) => string;
};

type TrendSeries = ChartSeries & {
  key: string;
};

type TrendPoint = {
  rawLabel: string;
  displayLabel: string;
  sortKey: number;
  order: number;
  [key: string]: number | string;
};

type TrendTooltipEntry = {
  dataKey?: string | number;
  value?: number | string;
};

type TrendTooltipContentProps = {
  active?: boolean;
  payload?: TrendTooltipEntry[];
  label?: string;
  series: TrendSeries[];
  valueFormatter: (value: number) => string;
};

type HistogramPoint = {
  lower_ms: number;
  upper_ms: number;
  count: number;
  label: string;
};

type HistogramTooltipEntry = {
  value?: number | string;
  payload?: HistogramPoint;
};

type HistogramTooltipContentProps = {
  active?: boolean;
  payload?: HistogramTooltipEntry[];
};

const RANGE_OPTIONS: RangeOption[] = [
  { key: "1h", label: "最近 1 小时", ms: 60 * 60 * 1000 },
  { key: "6h", label: "最近 6 小时", ms: 6 * 60 * 60 * 1000 },
  { key: "24h", label: "最近 24 小时", ms: 24 * 60 * 60 * 1000 },
];

const GLOBAL_PLATFORM_VALUE = "__global__";
const DEFAULT_REALTIME_REFRESH_SECONDS = 15;
const MIN_REALTIME_REFRESH_MS = 1_000;
const DEFAULT_HISTORY_REFRESH_MS = 60_000;
const MIN_HISTORY_REFRESH_MS = 15_000;
const MAX_HISTORY_REFRESH_MS = 300_000;
const SNAPSHOT_REFRESH_MS = 5_000;

function fromApiError(error: unknown): string {
  if (error instanceof ApiError) {
    return `${error.code}: ${error.message}`;
  }
  if (error instanceof Error) {
    return error.message;
  }
  return "未知错误";
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

function latestValue(values: number[]): number {
  if (!values.length) {
    return 0;
  }
  return values[values.length - 1];
}

function previousValue(values: number[]): number {
  if (values.length <= 1) {
    return 0;
  }
  return values[values.length - 2];
}

function percentDelta(values: number[]): number | null {
  if (values.length <= 1) {
    return null;
  }
  const prev = previousValue(values);
  const curr = latestValue(values);
  if (prev === 0) {
    return null;
  }
  return ((curr - prev) / prev) * 100;
}

function formatDelta(value: number | null): string {
  if (value === null) {
    return "--";
  }
  const sign = value > 0 ? "+" : "";
  return `${sign}${value.toFixed(1)}%`;
}

function formatCount(value: number): string {
  return new Intl.NumberFormat("zh-CN").format(Math.round(value));
}

function formatPercent(value: number): string {
  return `${(value * 100).toFixed(1)}%`;
}

function formatBps(value: number): string {
  const units = ["bps", "Kbps", "Mbps", "Gbps", "Tbps"];
  let next = value;
  let unitIndex = 0;
  while (next >= 1000 && unitIndex < units.length - 1) {
    next /= 1000;
    unitIndex += 1;
  }
  return `${next.toFixed(next >= 100 ? 0 : 1)} ${units[unitIndex]}`;
}

function formatShortBps(value: number): string {
  const units = ["bps", "Kbps", "Mbps", "Gbps", "Tbps"];
  let next = value;
  let unitIndex = 0;
  while (next >= 1000 && unitIndex < units.length - 1) {
    next /= 1000;
    unitIndex += 1;
  }
  return `${next.toFixed(next >= 100 ? 0 : 1)}${units[unitIndex]}`;
}

function formatBytes(value: number): string {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let next = value;
  let unitIndex = 0;
  while (next >= 1024 && unitIndex < units.length - 1) {
    next /= 1024;
    unitIndex += 1;
  }
  return `${next.toFixed(next >= 100 ? 0 : 1)} ${units[unitIndex]}`;
}

function formatShortBytes(value: number): string {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let next = value;
  let unitIndex = 0;
  while (next >= 1024 && unitIndex < units.length - 1) {
    next /= 1024;
    unitIndex += 1;
  }
  return `${next.toFixed(next >= 100 ? 0 : 1)}${units[unitIndex]}`;
}

function formatMilliseconds(value: number): string {
  if (value <= 0) {
    return "0 ms";
  }
  if (value >= 1000) {
    return `${(value / 1000).toFixed(2)} s`;
  }
  return `${value.toFixed(1)} ms`;
}

function formatShortMilliseconds(value: number): string {
  if (value >= 1000) {
    return `${(value / 1000).toFixed(1)}s`;
  }
  return `${Math.round(value)}ms`;
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

function formatShortPercent(value: number): string {
  return `${value.toFixed(0)}%`;
}

function formatLatencyAxisTick(value: number): string {
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
    return "";
  }
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) {
    return iso;
  }
  return new Intl.DateTimeFormat("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(date);
}

function sanitizeSeries(series: ChartSeries[]): ChartSeries[] {
  return series.map((item) => ({
    ...item,
    values: item.values.map((value) => (Number.isFinite(value) ? value : 0)),
  }));
}

function parseTrendTimestamp(value: string, fallbackIndex: number): number {
  if (!value) {
    return fallbackIndex;
  }
  const ts = Date.parse(value);
  if (Number.isNaN(ts)) {
    return fallbackIndex;
  }
  return ts;
}

function sortTimeSeriesByTimestamp<T>(items: T[], getTimestamp: (item: T) => string): T[] {
  return items
    .map((item, index) => ({
      item,
      index,
      sortKey: parseTrendTimestamp(getTimestamp(item), index),
    }))
    .sort((left, right) => {
      if (left.sortKey === right.sortKey) {
        return left.index - right.index;
      }
      return left.sortKey - right.sortKey;
    })
    .map((entry) => entry.item);
}

function normalizeTrendData(labels: string[], series: TrendSeries[]): TrendPoint[] {
  const pointCount = Math.max(labels.length, ...series.map((item) => item.values.length));

  const points = Array.from({ length: pointCount }, (_, index) => {
    const rawLabel = labels[index] ?? "";
    const point: TrendPoint = {
      rawLabel,
      displayLabel: formatClock(rawLabel),
      sortKey: parseTrendTimestamp(rawLabel, index),
      order: index,
    };

    series.forEach((item) => {
      point[item.key] = item.values[index] ?? 0;
    });

    return point;
  });

  points.sort((left, right) => {
    if (left.sortKey === right.sortKey) {
      return left.order - right.order;
    }
    return left.sortKey - right.sortKey;
  });

  return points;
}

function TrendTooltipContent({ active, payload, label, series, valueFormatter }: TrendTooltipContentProps) {
  if (!active || !payload?.length) {
    return null;
  }

  return (
    <div className="trend-tooltip">
      <p className="trend-tooltip-time">{label ? formatClock(String(label)) : "--"}</p>
      <div className="trend-tooltip-list">
        {series.map((item) => {
          const entry = payload.find((payloadItem) => payloadItem.dataKey === item.key);
          const value = Number(entry?.value ?? 0);
          const safeValue = Number.isFinite(value) ? value : 0;

          return (
            <p key={item.key} className="trend-tooltip-row">
              <span>
                <i style={{ background: item.color }} />
                {item.name}
              </span>
              <b>{valueFormatter(safeValue)}</b>
            </p>
          );
        })}
      </div>
    </div>
  );
}

function TrendChart({ labels, series, formatYAxisLabel }: TrendChartProps) {
  const safeSeries = sanitizeSeries(series);
  const yLabelFormatter = formatYAxisLabel ?? formatShortNumber;
  const trendSeries: TrendSeries[] = safeSeries.map((item, index) => ({
    ...item,
    key: `series_${index}`,
  }));
  const data = normalizeTrendData(labels, trendSeries);
  const valueCount = data.length;
  const firstLabel = data[0]?.displayLabel ?? "";
  const lastLabel = data[valueCount - 1]?.displayLabel ?? "";
  const gradientSeed = useId().replace(/:/g, "");
  const leadingSeries = trendSeries[0];
  const gradientId = `trend-gradient-${gradientSeed}`;
  const chartMargin = {
    top: 6,
    right: 8,
    bottom: 4,
    left: 8,
  };

  if (!valueCount || !trendSeries.length) {
    return (
      <div className="empty-box dashboard-empty">
        <AlertTriangle size={14} />
        <p>无可视化数据</p>
      </div>
    );
  }

  return (
    <div className="trend-chart">
      <div className="trend-svg">
        <ResponsiveContainer width="100%" height="100%">
          <ComposedChart data={data} margin={chartMargin}>
            {leadingSeries?.fillColor ? (
              <defs>
                <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor={leadingSeries.fillColor} stopOpacity={0.92} />
                  <stop offset="100%" stopColor={leadingSeries.fillColor} stopOpacity={0.14} />
                </linearGradient>
              </defs>
            ) : null}

            <CartesianGrid stroke="rgba(65, 87, 121, 0.16)" strokeDasharray="2 4" vertical={false} />
            <XAxis dataKey="rawLabel" hide />
            <YAxis
              width="auto"
              tickMargin={4}
              axisLine={false}
              tickLine={false}
              tick={{ fill: "#657691", fontSize: 11, fontWeight: 600 }}
              tickFormatter={(value) => yLabelFormatter(Number(value))}
              domain={[0, "auto"]}
            />
            <Tooltip
              cursor={{ stroke: "rgba(15, 94, 216, 0.34)", strokeWidth: 1 }}
              wrapperStyle={{ outline: "none" }}
              content={<TrendTooltipContent series={trendSeries} valueFormatter={yLabelFormatter} />}
            />

            {leadingSeries?.fillColor ? (
              <Area
                type="monotone"
                dataKey={leadingSeries.key}
                name={leadingSeries.name}
                stroke={leadingSeries.color}
                fill={`url(#${gradientId})`}
                strokeWidth={1.8}
                dot={false}
                activeDot={{ r: 3, stroke: "#ffffff", strokeWidth: 1, fill: leadingSeries.color }}
                isAnimationActive={false}
                connectNulls
              />
            ) : null}

            {trendSeries.slice(leadingSeries?.fillColor ? 1 : 0).map((item) => (
              <Line
                key={item.key}
                type="monotone"
                dataKey={item.key}
                name={item.name}
                stroke={item.color}
                strokeWidth={1.8}
                dot={false}
                activeDot={{ r: 3, stroke: "#ffffff", strokeWidth: 1, fill: item.color }}
                isAnimationActive={false}
                connectNulls
              />
            ))}
          </ComposedChart>
        </ResponsiveContainer>
      </div>

      <div className="trend-footer">
        <span>{firstLabel}</span>
        <span>{lastLabel}</span>
      </div>
    </div>
  );
}

function HistogramTooltipContent({ active, payload }: HistogramTooltipContentProps) {
  if (!active || !payload?.length) {
    return null;
  }

  const point = payload[0]?.payload;
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

function compressHistogram(buckets: LatencyBucket[], limit = 28): LatencyBucket[] {
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

function Histogram({ buckets }: { buckets: LatencyBucket[] }) {
  if (!buckets.length) {
    return (
      <div className="empty-box dashboard-empty">
        <AlertTriangle size={14} />
        <p>无分布数据</p>
      </div>
    );
  }

  const compact = compressHistogram(buckets);
  const inferredStep = compact.length >= 2 ? Math.max(1, compact[1].le_ms - compact[0].le_ms) : 0;
  const isLegacyUpperInclusive =
    compact.length > 0 && inferredStep > 0 && compact[0].le_ms === inferredStep;

  let previousUpper = -1;
  const data: HistogramPoint[] = compact.map((bucket) => {
    const lower = Math.max(0, previousUpper + 1);
    const rawUpper = isLegacyUpperInclusive ? bucket.le_ms - 1 : bucket.le_ms;
    const upperInclusive = Math.max(lower, rawUpper);
    previousUpper = upperInclusive;
    return {
      lower_ms: lower,
      upper_ms: upperInclusive,
      count: Math.max(0, bucket.count),
      label: `${upperInclusive}`,
    };
  });
  const gradientId = `histogram-gradient-${useId().replace(/:/g, "")}`;
  const chartMargin = {
    top: 6,
    right: 8,
    bottom: 4,
    left: 8,
  };

  return (
    <div className="histogram-chart">
      <ResponsiveContainer width="100%" height="100%">
        <BarChart data={data} margin={chartMargin}>
          <defs>
            <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor="#2388ff" stopOpacity={0.94} />
              <stop offset="100%" stopColor="#0f5ed8" stopOpacity={0.88} />
            </linearGradient>
          </defs>

          <CartesianGrid stroke="rgba(65, 87, 121, 0.16)" strokeDasharray="2 4" vertical={false} />
          <XAxis
            dataKey="label"
            interval="preserveStartEnd"
            minTickGap={14}
            tickMargin={4}
            axisLine={false}
            tickLine={false}
            tick={{ fill: "#607191", fontSize: 11, fontWeight: 600 }}
            tickFormatter={(value) => formatLatencyAxisTick(Number(value))}
          />
          <YAxis
            width="auto"
            allowDecimals={false}
            tickMargin={4}
            axisLine={false}
            tickLine={false}
            tick={{ fill: "#607191", fontSize: 11, fontWeight: 600 }}
            tickFormatter={(value) => formatShortNumber(Number(value))}
          />
          <Tooltip
            cursor={{ fill: "rgba(15, 94, 216, 0.08)" }}
            wrapperStyle={{ outline: "none" }}
            content={<HistogramTooltipContent />}
          />
          <Bar
            dataKey="count"
            fill={`url(#${gradientId})`}
            radius={[5, 5, 0, 0]}
            maxBarSize={28}
            activeBar={{ fill: "#0d63dd", stroke: "#f2f7ff", strokeWidth: 1.2 }}
            isAnimationActive={false}
          />
        </BarChart>
      </ResponsiveContainer>
    </div>
  );
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

function kpiTone(delta: number | null): "success" | "warning" | "neutral" {
  if (delta === null) {
    return "neutral";
  }
  return delta >= 0 ? "success" : "warning";
}

function normalizePositiveSeconds(seconds: number | undefined): number | null {
  if (typeof seconds !== "number" || !Number.isFinite(seconds) || seconds <= 0) {
    return null;
  }
  return seconds;
}

function realtimeRefreshMsFromSteps(stepSeconds: Array<number | undefined>): number {
  const steps = stepSeconds.map(normalizePositiveSeconds).filter((value): value is number => value !== null);
  if (!steps.length) {
    return DEFAULT_REALTIME_REFRESH_SECONDS * 1000;
  }
  return Math.max(MIN_REALTIME_REFRESH_MS, Math.round(Math.min(...steps) * 1000));
}

function historyRefreshMsFromBuckets(bucketSeconds: Array<number | undefined>): number {
  const buckets = bucketSeconds.map(normalizePositiveSeconds).filter((value): value is number => value !== null);
  if (!buckets.length) {
    return DEFAULT_HISTORY_REFRESH_MS;
  }
  const intervalMs = Math.round(Math.min(...buckets) * 1000);
  return Math.min(MAX_HISTORY_REFRESH_MS, Math.max(MIN_HISTORY_REFRESH_MS, intervalMs));
}

export function DashboardPage() {
  const [rangeKey, setRangeKey] = useState<RangeKey>("6h");
  const [selectedPlatformId, setSelectedPlatformId] = useState(GLOBAL_PLATFORM_VALUE);

  const platformsQuery = useQuery({
    queryKey: ["dashboard-platform-options"],
    queryFn: listPlatforms,
    refetchInterval: 60_000,
  });

  const platforms = useMemo(() => platformsQuery.data ?? [], [platformsQuery.data]);
  const isGlobalScope = selectedPlatformId === GLOBAL_PLATFORM_VALUE;
  const activePlatform = useMemo(() => {
    if (isGlobalScope) {
      return null;
    }
    return platforms.find((item) => item.id === selectedPlatformId) ?? null;
  }, [isGlobalScope, platforms, selectedPlatformId]);
  const activePlatformId = activePlatform?.id ?? "";
  const isPlatformScope = Boolean(activePlatformId);
  const activePlatformName = activePlatform?.name ?? "全局视角";

  const globalRealtimeQuery = useQuery({
    queryKey: ["dashboard-global-realtime", rangeKey],
    queryFn: async () => {
      const window = getTimeWindow(rangeKey);
      return getDashboardGlobalRealtimeData(window);
    },
    refetchInterval: (query) => {
      const data = query.state.data as Pick<DashboardGlobalData, "realtime_throughput" | "realtime_connections"> | undefined;
      return realtimeRefreshMsFromSteps([data?.realtime_throughput.step_seconds, data?.realtime_connections.step_seconds]);
    },
    placeholderData: (prev) => prev,
  });

  const globalHistoryQuery = useQuery({
    queryKey: ["dashboard-global-history", rangeKey],
    queryFn: async () => {
      const window = getTimeWindow(rangeKey);
      return getDashboardGlobalHistoryData(window);
    },
    refetchInterval: (query) => {
      const data = query.state.data as
        | Pick<
            DashboardGlobalData,
            "history_traffic" | "history_requests" | "history_access_latency" | "history_probes" | "history_node_pool"
          >
        | undefined;
      return historyRefreshMsFromBuckets([
        data?.history_traffic.bucket_seconds,
        data?.history_requests.bucket_seconds,
        data?.history_access_latency.bucket_seconds,
        data?.history_probes.bucket_seconds,
        data?.history_node_pool.bucket_seconds,
      ]);
    },
    placeholderData: (prev) => prev,
  });

  const globalSnapshotQuery = useQuery({
    queryKey: ["dashboard-global-snapshot"],
    queryFn: getDashboardGlobalSnapshotData,
    refetchInterval: SNAPSHOT_REFRESH_MS,
    placeholderData: (prev) => prev,
  });

  const platformRealtimeQuery = useQuery({
    queryKey: ["dashboard-platform-realtime", rangeKey, activePlatformId],
    queryFn: async () => {
      const window = getTimeWindow(rangeKey);
      return getDashboardPlatformRealtimeData(activePlatformId, window);
    },
    enabled: isPlatformScope,
    refetchInterval: (query) => {
      const data = query.state.data as Pick<DashboardPlatformData, "realtime_leases"> | undefined;
      return realtimeRefreshMsFromSteps([data?.realtime_leases.step_seconds]);
    },
    placeholderData: (prev) => prev,
  });

  const platformHistoryQuery = useQuery({
    queryKey: ["dashboard-platform-history", rangeKey, activePlatformId],
    queryFn: async () => {
      const window = getTimeWindow(rangeKey);
      return getDashboardPlatformHistoryData(activePlatformId, window);
    },
    enabled: isPlatformScope,
    refetchInterval: (query) => {
      const data = query.state.data as Pick<DashboardPlatformData, "history_lease_lifetime"> | undefined;
      return historyRefreshMsFromBuckets([data?.history_lease_lifetime.bucket_seconds]);
    },
    placeholderData: (prev) => prev,
  });

  const platformSnapshotQuery = useQuery({
    queryKey: ["dashboard-platform-snapshot", activePlatformId],
    queryFn: async () => getDashboardPlatformSnapshotData(activePlatformId),
    enabled: isPlatformScope,
    refetchInterval: SNAPSHOT_REFRESH_MS,
    placeholderData: (prev) => prev,
  });

  const globalData = useMemo<DashboardGlobalData | undefined>(() => {
    if (!globalRealtimeQuery.data && !globalHistoryQuery.data && !globalSnapshotQuery.data) {
      return undefined;
    }
    return {
      realtime_throughput: globalRealtimeQuery.data?.realtime_throughput ?? { step_seconds: 0, items: [] },
      realtime_connections: globalRealtimeQuery.data?.realtime_connections ?? { step_seconds: 0, items: [] },
      history_traffic: globalHistoryQuery.data?.history_traffic ?? { bucket_seconds: 0, items: [] },
      history_requests: globalHistoryQuery.data?.history_requests ?? { bucket_seconds: 0, items: [] },
      history_access_latency: globalHistoryQuery.data?.history_access_latency ?? {
        bucket_seconds: 0,
        bin_width_ms: 0,
        overflow_ms: 0,
        items: [],
      },
      history_probes: globalHistoryQuery.data?.history_probes ?? { bucket_seconds: 0, items: [] },
      history_node_pool: globalHistoryQuery.data?.history_node_pool ?? { bucket_seconds: 0, items: [] },
      snapshot_node_pool: globalSnapshotQuery.data?.snapshot_node_pool ?? {
        generated_at: "",
        total_nodes: 0,
        healthy_nodes: 0,
        egress_ip_count: 0,
      },
      snapshot_latency_global: globalSnapshotQuery.data?.snapshot_latency_global ?? {
        generated_at: "",
        scope: "global",
        bin_width_ms: 0,
        overflow_ms: 0,
        sample_count: 0,
        buckets: [],
        overflow_count: 0,
      },
    };
  }, [globalRealtimeQuery.data, globalHistoryQuery.data, globalSnapshotQuery.data]);

  const platformData = useMemo<DashboardPlatformData | undefined>(() => {
    if (!platformRealtimeQuery.data && !platformHistoryQuery.data && !platformSnapshotQuery.data) {
      return undefined;
    }
    return {
      realtime_leases: platformRealtimeQuery.data?.realtime_leases ?? {
        platform_id: activePlatformId,
        step_seconds: 0,
        items: [],
      },
      history_lease_lifetime: platformHistoryQuery.data?.history_lease_lifetime ?? {
        platform_id: activePlatformId,
        bucket_seconds: 0,
        items: [],
      },
      snapshot_platform_node_pool: platformSnapshotQuery.data?.snapshot_platform_node_pool ?? {
        generated_at: "",
        platform_id: activePlatformId,
        routable_node_count: 0,
        egress_ip_count: 0,
      },
      snapshot_latency_platform: platformSnapshotQuery.data?.snapshot_latency_platform ?? {
        generated_at: "",
        scope: "platform",
        platform_id: activePlatformId || undefined,
        bin_width_ms: 0,
        overflow_ms: 0,
        sample_count: 0,
        buckets: [],
        overflow_count: 0,
      },
    };
  }, [activePlatformId, platformRealtimeQuery.data, platformHistoryQuery.data, platformSnapshotQuery.data]);

  const globalError = globalRealtimeQuery.error ?? globalHistoryQuery.error ?? globalSnapshotQuery.error;
  const platformError = platformRealtimeQuery.error ?? platformHistoryQuery.error ?? platformSnapshotQuery.error;
  const isInitialLoading =
    !globalData && (globalRealtimeQuery.isLoading || globalHistoryQuery.isLoading || globalSnapshotQuery.isLoading);

  const throughputItems = sortTimeSeriesByTimestamp(globalData?.realtime_throughput.items ?? [], (item) => item.ts);
  const throughputIngress = throughputItems.map((item) => item.ingress_bps);
  const throughputEgress = throughputItems.map((item) => item.egress_bps);
  const throughputLabels = throughputItems.map((item) => item.ts);

  const connectionItems = sortTimeSeriesByTimestamp(globalData?.realtime_connections.items ?? [], (item) => item.ts);
  const connectionsInbound = connectionItems.map((item) => item.inbound_connections);
  const connectionsOutbound = connectionItems.map((item) => item.outbound_connections);
  const connectionsLabels = connectionItems.map((item) => item.ts);

  const leaseRealtimeItems = sortTimeSeriesByTimestamp(platformData?.realtime_leases.items ?? [], (item) => item.ts);
  const leasesValues = leaseRealtimeItems.map((item) => item.active_leases);

  const trafficItems = sortTimeSeriesByTimestamp(globalData?.history_traffic.items ?? [], (item) => item.bucket_start);
  const trafficIngress = trafficItems.map((item) => item.ingress_bytes);
  const trafficEgress = trafficItems.map((item) => item.egress_bytes);
  const trafficLabels = trafficItems.map((item) => item.bucket_start);

  const requestItems = sortTimeSeriesByTimestamp(globalData?.history_requests.items ?? [], (item) => item.bucket_start);
  const requestTotals = requestItems.map((item) => item.total_requests);
  const requestSuccessRates = requestItems.map((item) => item.success_rate * 100);
  const requestLabels = requestItems.map((item) => item.bucket_start);

  const nodePoolItems = sortTimeSeriesByTimestamp(globalData?.history_node_pool.items ?? [], (item) => item.bucket_start);
  const nodeTotal = nodePoolItems.map((item) => item.total_nodes);
  const nodeHealthy = nodePoolItems.map((item) => item.healthy_nodes);
  const nodeLabels = nodePoolItems.map((item) => item.bucket_start);

  const probeItems = sortTimeSeriesByTimestamp(globalData?.history_probes.items ?? [], (item) => item.bucket_start);
  const probeCounts = probeItems.map((item) => item.total_count);
  const probeLabels = probeItems.map((item) => item.bucket_start);

  const leaseLifetimeItems = sortTimeSeriesByTimestamp(platformData?.history_lease_lifetime.items ?? [], (item) => item.bucket_start);
  const leaseP50 = leaseLifetimeItems.map((item) => item.p50_ms);
  const leaseP5 = leaseLifetimeItems.map((item) => item.p5_ms);
  const leaseLabels = leaseLifetimeItems.map((item) => item.bucket_start);

  const latestIngress = latestValue(throughputIngress);
  const latestEgress = latestValue(throughputEgress);
  const latestConnections = latestValue(connectionsInbound) + latestValue(connectionsOutbound);
  const latestLeases = latestValue(leasesValues);

  const totalTrafficBytes = sum(trafficIngress) + sum(trafficEgress);
  const totalRequests = sum(requestTotals);
  const successRequests = requestItems.reduce((acc, item) => acc + item.success_requests, 0);
  const aggregatedSuccessRate = successRate(totalRequests, successRequests);

  const snapshotNodePool = globalData?.snapshot_node_pool;
  const nodeHealthRate = snapshotNodePool ? successRate(snapshotNodePool.total_nodes, snapshotNodePool.healthy_nodes) : 0;

  const globalLatencyHistogram = globalData?.snapshot_latency_global.buckets ?? [];
  const platformLatencyHistogram = platformData?.snapshot_latency_platform.buckets ?? [];
  const activeLatencyHistogram = isPlatformScope ? platformLatencyHistogram : globalLatencyHistogram;

  const throughputDelta = percentDelta(throughputIngress.map((value, index) => value + (throughputEgress[index] ?? 0)));
  const connectionDelta = percentDelta(connectionsInbound.map((value, index) => value + (connectionsOutbound[index] ?? 0)));
  const leasesDelta = percentDelta(leasesValues);
  return (
    <section className="dashboard-page">
      <header className="module-header">
        <div>
          <p className="eyebrow">Observability</p>
          <h2>Dashboard</h2>
          <p className="module-description">高密度可视化总览实时吞吐、连接、节点健康、探测与租约延迟分布。</p>
        </div>
      </header>

      {globalError ? (
        <div className="callout callout-error">
          <AlertTriangle size={14} />
          <span>{fromApiError(globalError)}</span>
        </div>
      ) : null}

      {isPlatformScope && platformError ? (
        <div className="callout callout-warning">
          <AlertTriangle size={14} />
          <span>平台维度指标加载失败：{fromApiError(platformError)}</span>
        </div>
      ) : null}

      <Card className="dashboard-hero">
        <div className="dashboard-hero-header">
          <div>
            <p className="dashboard-hero-title">Control Pulse</p>
          </div>

          <div className="dashboard-hero-controls">
            <label className="dashboard-control">
              <span>时间范围</span>
              <Select value={rangeKey} onChange={(event) => setRangeKey(event.target.value as RangeKey)}>
                {RANGE_OPTIONS.map((item) => (
                  <option key={item.key} value={item.key}>
                    {item.label}
                  </option>
                ))}
              </Select>
            </label>

            <label className="dashboard-control">
              <span>平台维度</span>
              <Select value={selectedPlatformId} onChange={(event) => setSelectedPlatformId(event.target.value)}>
                <option value={GLOBAL_PLATFORM_VALUE}>全局视角（不限定平台）</option>
                {platforms.map((platform) => (
                  <option key={platform.id} value={platform.id}>
                    {platform.name}
                  </option>
                ))}
              </Select>
            </label>
          </div>
        </div>

      </Card>

      <div className="dashboard-kpi-grid">
        <Card className="dashboard-kpi-card">
          <div className="dashboard-kpi-icon waves">
            <Waves size={18} />
          </div>
          <div>
            <p className="dashboard-kpi-label">实时吞吐</p>
            <p className="dashboard-kpi-value">{formatBps(latestIngress + latestEgress)}</p>
            <p className="dashboard-kpi-sub">
              ingress {formatBps(latestIngress)} · egress {formatBps(latestEgress)}
            </p>
          </div>
          <Badge variant={kpiTone(throughputDelta)}>{formatDelta(throughputDelta)}</Badge>
        </Card>

        <Card className="dashboard-kpi-card">
          <div className="dashboard-kpi-icon gauge">
            <Gauge size={18} />
          </div>
          <div>
            <p className="dashboard-kpi-label">实时连接数</p>
            <p className="dashboard-kpi-value">{formatCount(latestConnections)}</p>
            <p className="dashboard-kpi-sub">
              inbound {formatCount(latestValue(connectionsInbound))} · outbound {formatCount(latestValue(connectionsOutbound))}
            </p>
          </div>
          <Badge variant={kpiTone(connectionDelta)}>{formatDelta(connectionDelta)}</Badge>
        </Card>

        <Card className="dashboard-kpi-card">
          <div className="dashboard-kpi-icon shield">
            <Shield size={18} />
          </div>
          <div>
            <p className="dashboard-kpi-label">节点健康率</p>
            <p className="dashboard-kpi-value">{formatPercent(nodeHealthRate)}</p>
            <p className="dashboard-kpi-sub">
              healthy {formatCount(snapshotNodePool?.healthy_nodes ?? 0)} / total {formatCount(snapshotNodePool?.total_nodes ?? 0)}
            </p>
          </div>
          <Badge variant={nodeHealthRate >= 0.75 ? "success" : "warning"}>{formatCount(snapshotNodePool?.egress_ip_count ?? 0)} IP</Badge>
        </Card>

        <Card className="dashboard-kpi-card">
          <div className="dashboard-kpi-icon lease">
            <Layers size={18} />
          </div>
          <div>
            <p className="dashboard-kpi-label">平台 Active Leases</p>
            <p className="dashboard-kpi-value">{isPlatformScope ? formatCount(latestLeases) : "--"}</p>
            <p className="dashboard-kpi-sub">{isPlatformScope ? activePlatformName : "全局模式下不显示单平台租约"}</p>
          </div>
          <Badge variant={isPlatformScope ? kpiTone(leasesDelta) : "neutral"}>{isPlatformScope ? formatDelta(leasesDelta) : "N/A"}</Badge>
        </Card>
      </div>

      <div className="dashboard-main-grid">
        <Card className="dashboard-panel span-2">
          <div className="dashboard-panel-header">
            <h3>吞吐趋势</h3>
            <p>实时 ingress / egress bps</p>
          </div>
          <TrendChart
            labels={throughputLabels}
            formatYAxisLabel={formatShortBps}
            series={[
              {
                name: "Ingress",
                values: throughputIngress,
                color: "#1076ff",
                fillColor: "rgba(16, 118, 255, 0.14)",
              },
              {
                name: "Egress",
                values: throughputEgress,
                color: "#00a17f",
              },
            ]}
          />
          <div className="dashboard-legend">
            <span>
              <i style={{ background: "#1076ff" }} />
              Ingress
            </span>
            <span>
              <i style={{ background: "#00a17f" }} />
              Egress
            </span>
          </div>
        </Card>

        <Card className="dashboard-panel">
          <div className="dashboard-panel-header">
            <h3>连接趋势</h3>
            <p>实时 inbound / outbound</p>
          </div>
          <TrendChart
            labels={connectionsLabels}
            formatYAxisLabel={formatShortNumber}
            series={[
              {
                name: "Inbound",
                values: connectionsInbound,
                color: "#2467e4",
                fillColor: "rgba(36, 103, 228, 0.12)",
              },
              {
                name: "Outbound",
                values: connectionsOutbound,
                color: "#f18f01",
              },
            ]}
          />
          <div className="dashboard-legend">
            <span>
              <i style={{ background: "#2467e4" }} />
              Inbound
            </span>
            <span>
              <i style={{ background: "#f18f01" }} />
              Outbound
            </span>
          </div>
        </Card>

        <Card className="dashboard-panel">
          <div className="dashboard-panel-header">
            <h3>请求质量</h3>
            <p>请求成功率（%）</p>
          </div>
          <TrendChart
            labels={requestLabels}
            formatYAxisLabel={formatShortPercent}
            series={[
              {
                name: "Success Rate %",
                values: requestSuccessRates,
                color: "#0f9d8b",
                fillColor: "rgba(15, 157, 139, 0.14)",
              },
            ]}
          />
          <div className="dashboard-summary-inline">
            <span>总请求 {formatCount(totalRequests)}</span>
            <span>成功率 {formatPercent(aggregatedSuccessRate)}</span>
          </div>
        </Card>

        <Card className="dashboard-panel">
          <div className="dashboard-panel-header">
            <h3>流量累计</h3>
            <p>窗口内 ingress / egress bytes</p>
          </div>
          <TrendChart
            labels={trafficLabels}
            formatYAxisLabel={formatShortBytes}
            series={[
              {
                name: "Ingress Bytes",
                values: trafficIngress,
                color: "#2068f6",
                fillColor: "rgba(32, 104, 246, 0.12)",
              },
              {
                name: "Egress Bytes",
                values: trafficEgress,
                color: "#0f9d8b",
              },
            ]}
          />
          <div className="dashboard-summary-inline">
            <span>总流量 {formatBytes(totalTrafficBytes)}</span>
          </div>
        </Card>

        <Card className="dashboard-panel">
          <div className="dashboard-panel-header">
            <h3>节点池趋势</h3>
            <p>total / healthy nodes</p>
          </div>
          <TrendChart
            labels={nodeLabels}
            formatYAxisLabel={formatShortNumber}
            series={[
              {
                name: "Total Nodes",
                values: nodeTotal,
                color: "#2d63d8",
                fillColor: "rgba(45, 99, 216, 0.11)",
              },
              {
                name: "Healthy Nodes",
                values: nodeHealthy,
                color: "#0c9f68",
              },
            ]}
          />
        </Card>

        <Card className="dashboard-panel">
          <div className="dashboard-panel-header">
            <h3>探测任务量</h3>
            <p>历史 probe total_count</p>
          </div>
          <TrendChart
            labels={probeLabels}
            formatYAxisLabel={formatShortNumber}
            series={[
              {
                name: "Probes",
                values: probeCounts,
                color: "#e26a2c",
                fillColor: "rgba(226, 106, 44, 0.16)",
              },
            ]}
          />
        </Card>

        <Card className="dashboard-panel">
          <div className="dashboard-panel-header">
            <h3>租约寿命分位</h3>
            <p>{isPlatformScope ? activePlatformName : "请选择平台查看"}</p>
          </div>
          {!isPlatformScope ? (
            <div className="empty-box dashboard-empty">
              <AlertTriangle size={14} />
              <p>全局视角下不展示单平台租约寿命分位</p>
            </div>
          ) : (
            <>
              <TrendChart
                labels={leaseLabels}
                formatYAxisLabel={formatShortMilliseconds}
                series={[
                  {
                    name: "P50",
                    values: leaseP50,
                    color: "#0a86cf",
                    fillColor: "rgba(10, 134, 207, 0.14)",
                  },
                  {
                    name: "P5",
                    values: leaseP5,
                    color: "#9957e0",
                  },
                ]}
              />
              <div className="dashboard-summary-inline">
                <span>P50 {formatMilliseconds(latestValue(leaseP50))}</span>
                <span>P5 {formatMilliseconds(latestValue(leaseP5))}</span>
              </div>
            </>
          )}
        </Card>

        <Card className="dashboard-panel span-2">
          <div className="dashboard-panel-header">
            <h3>节点延迟分布</h3>
            <p>延迟直方图</p>
          </div>

          <Histogram buckets={activeLatencyHistogram} />
        </Card>

        <Card className="dashboard-panel">
          <div className="dashboard-panel-header">
            <h3>平台快照</h3>
            <p>routable / egress IP</p>
          </div>
          {!isPlatformScope ? (
            <div className="empty-box dashboard-empty">
              <AlertTriangle size={14} />
              <p>当前是全局视角，选择平台后可查看平台快照</p>
            </div>
          ) : (
            <div className="dashboard-snapshot-list">
              <div>
                <span>Platform</span>
                <p>{activePlatformName}</p>
              </div>
              <div>
                <span>Routable Nodes</span>
                <p>{formatCount(platformData?.snapshot_platform_node_pool.routable_node_count ?? 0)}</p>
              </div>
              <div>
                <span>Egress IPs</span>
                <p>{formatCount(platformData?.snapshot_platform_node_pool.egress_ip_count ?? 0)}</p>
              </div>
              <div>
                <span>Generated At</span>
                <p>{formatDateTime(platformData?.snapshot_platform_node_pool.generated_at ?? "")}</p>
              </div>
            </div>
          )}
        </Card>

        <Card className="dashboard-panel">
          <div className="dashboard-panel-header">
            <h3>系统节点快照</h3>
            <p>全局节点池状态</p>
          </div>
          <div className="dashboard-snapshot-list">
            <div>
              <span>Total Nodes</span>
              <p>{formatCount(snapshotNodePool?.total_nodes ?? 0)}</p>
            </div>
            <div>
              <span>Healthy Nodes</span>
              <p>{formatCount(snapshotNodePool?.healthy_nodes ?? 0)}</p>
            </div>
            <div>
              <span>Egress IP Count</span>
              <p>{formatCount(snapshotNodePool?.egress_ip_count ?? 0)}</p>
            </div>
            <div>
              <span>Generated At</span>
              <p>{formatDateTime(snapshotNodePool?.generated_at ?? "")}</p>
            </div>
          </div>
        </Card>
      </div>

      {isInitialLoading ? (
        <div className="callout callout-warning">
          <Server size={14} />
          <span>Dashboard 数据加载中...</span>
        </div>
      ) : null}
    </section>
  );
}
