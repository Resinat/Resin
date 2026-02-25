import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Gauge, Layers, Server, Shield, Waves } from "lucide-react";
import { useId, useMemo, useState } from "react";
import { Area, Bar, BarChart, CartesianGrid, ComposedChart, Line, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { Badge } from "../../components/ui/Badge";
import { Card } from "../../components/ui/Card";
import { Select } from "../../components/ui/Select";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { formatApiErrorMessage } from "../../lib/error-message";
import {
  type DashboardGlobalHistoryData,
  type DashboardGlobalRealtimeData,
  getDashboardGlobalHistoryData,
  getDashboardGlobalRealtimeData,
  getDashboardGlobalSnapshotData,
} from "./api";
import type { DashboardGlobalData, LatencyBucket, TimeWindow } from "./types";

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

const DEFAULT_REALTIME_REFRESH_SECONDS = 15;
const MIN_REALTIME_REFRESH_MS = 1_000;
const DEFAULT_HISTORY_REFRESH_MS = 60_000;
const MIN_HISTORY_REFRESH_MS = 15_000;
const MAX_HISTORY_REFRESH_MS = 300_000;
const SNAPSHOT_REFRESH_MS = 5_000;
const MAX_TREND_POINTS = 480;
const MAX_HISTOGRAM_BUCKETS = 120;

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

function numberLocale(): string {
  return isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN";
}

function formatCount(value: number): string {
  return new Intl.NumberFormat(numberLocale()).format(Math.round(value));
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
  return new Intl.DateTimeFormat(numberLocale(), {
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
    const idx = 1 + Math.floor((i * span) / middleCount);
    selected.add(Math.min(pointCount - 2, idx));
  }
  return Array.from(selected).sort((a, b) => a - b);
}

// M4 downsampling keeps first/min/max/last from each bucket.
function buildM4SampleIndices(pointCount: number, series: ChartSeries[], maxPoints: number): number[] {
  if (pointCount <= 0 || maxPoints <= 0) {
    return [];
  }
  if (pointCount <= maxPoints) {
    return Array.from({ length: pointCount }, (_, index) => index);
  }
  if (maxPoints < 4) {
    return buildUniformSampleIndices(pointCount, maxPoints);
  }

  const bucketCount = Math.max(1, Math.min(pointCount, Math.floor(maxPoints / 4)));
  if (bucketCount <= 1) {
    return buildUniformSampleIndices(pointCount, maxPoints);
  }

  const selected: number[] = [];
  let lastPushed = -1;
  const pushUnique = (index: number) => {
    if (index !== lastPushed) {
      selected.push(index);
      lastPushed = index;
    }
  };

  for (let bucket = 0; bucket < bucketCount; bucket += 1) {
    const start = Math.floor((bucket * pointCount) / bucketCount);
    const endExclusive = Math.floor(((bucket + 1) * pointCount) / bucketCount);
    const end = Math.max(start, endExclusive - 1);

    let minIndex = start;
    let maxIndex = start;
    let minValue = Number.POSITIVE_INFINITY;
    let maxValue = Number.NEGATIVE_INFINITY;

    for (let index = start; index <= end; index += 1) {
      let pointMin = Number.POSITIVE_INFINITY;
      let pointMax = Number.NEGATIVE_INFINITY;

      if (!series.length) {
        pointMin = 0;
        pointMax = 0;
      } else {
        for (const item of series) {
          const value = item.values[index] ?? 0;
          if (value < pointMin) {
            pointMin = value;
          }
          if (value > pointMax) {
            pointMax = value;
          }
        }
      }

      if (pointMin < minValue || (pointMin === minValue && index < minIndex)) {
        minValue = pointMin;
        minIndex = index;
      }
      if (pointMax > maxValue || (pointMax === maxValue && index < maxIndex)) {
        maxValue = pointMax;
        maxIndex = index;
      }
    }

    const bucketIndices = [start, minIndex, maxIndex, end].sort((a, b) => a - b);
    let previousBucketIndex = -1;
    for (const index of bucketIndices) {
      if (index !== previousBucketIndex) {
        pushUnique(index);
        previousBucketIndex = index;
      }
    }
  }

  if (selected.length <= maxPoints) {
    return selected;
  }

  const sampledSelection = buildUniformSampleIndices(selected.length, maxPoints);
  return sampledSelection.map((index) => selected[index] ?? selected[selected.length - 1]);
}

function downsampleTrendInput(
  labels: string[],
  series: ChartSeries[],
  maxPoints: number,
): { labels: string[]; series: ChartSeries[] } {
  const pointCount = Math.max(labels.length, ...series.map((item) => item.values.length));
  if (pointCount <= 0) {
    return { labels, series };
  }

  const indices = buildM4SampleIndices(pointCount, series, maxPoints);
  if (!indices.length || indices.length === pointCount) {
    return { labels, series };
  }

  return {
    labels: indices.map((index) => labels[index] ?? ""),
    series: series.map((item) => ({
      ...item,
      values: indices.map((index) => item.values[index] ?? 0),
    })),
  };
}

function TrendTooltipContent({ active, payload, label, series, valueFormatter }: TrendTooltipContentProps) {
  if (!active || !payload?.length) {
    return null;
  }

  return (
    <div className="trend-tooltip">
      <p className="trend-tooltip-time">{label ? formatClock(new Date(Number(label)).toISOString()) : "--"}</p>
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
  const { t } = useI18n();
  const safeSeries = sanitizeSeries(series);
  const sampled = downsampleTrendInput(labels, safeSeries, MAX_TREND_POINTS);
  const yLabelFormatter = formatYAxisLabel ?? formatShortNumber;
  const trendSeries: TrendSeries[] = sampled.series.map((item, index) => ({
    ...item,
    key: `series_${index}`,
  }));
  const data = normalizeTrendData(sampled.labels, trendSeries);
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
        <p>{t("无可视化数据")}</p>
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
            <XAxis dataKey="sortKey" type="number" scale="time" domain={["dataMin", "dataMax"]} hide />
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
  const { t } = useI18n();

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
      <p className="histogram-tooltip-value">{t("节点数 {{count}}", { count: formatCount(safeCount) })}</p>
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

function Histogram({ buckets }: { buckets: LatencyBucket[] }) {
  const { t } = useI18n();
  const gradientId = `histogram-gradient-${useId().replace(/:/g, "")}`;

  if (!buckets.length) {
    return (
      <div className="empty-box dashboard-empty">
        <AlertTriangle size={14} />
        <p>{t("无分布数据")}</p>
      </div>
    );
  }

  const compact = compressHistogram(buckets);
  const inferredStep = compact.length >= 2 ? Math.max(1, compact[1].le_ms - compact[0].le_ms) : 0;
  const isLegacyUpperInclusive =
    compact.length > 0 && inferredStep > 0 && compact[0].le_ms === inferredStep;

  const data = compact.reduce<{ points: HistogramPoint[]; previousUpper: number }>(
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
            count: Math.max(0, bucket.count),
            label: `${lower}`,
          },
        ],
      };
    },
    { points: [], previousUpper: -1 },
  ).points;
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
  const { t } = useI18n();
  const [rangeKey, setRangeKey] = useState<RangeKey>("6h");
  const queryClient = useQueryClient();

  const globalRealtimeQuery = useQuery({
    queryKey: ["dashboard-global-realtime", rangeKey],
    queryFn: async () => {
      const window = getTimeWindow(rangeKey);
      const previous = queryClient.getQueryData<DashboardGlobalRealtimeData>(["dashboard-global-realtime", rangeKey]);
      return getDashboardGlobalRealtimeData(window, previous);
    },
    refetchInterval: (query) => {
      const data = query.state.data as
        | Pick<DashboardGlobalData, "realtime_throughput" | "realtime_connections" | "realtime_leases">
        | undefined;
      return realtimeRefreshMsFromSteps([
        data?.realtime_throughput.step_seconds,
        data?.realtime_connections.step_seconds,
        data?.realtime_leases.step_seconds,
      ]);
    },
    placeholderData: (prev) => prev,
  });

  const globalHistoryQuery = useQuery({
    queryKey: ["dashboard-global-history", rangeKey],
    queryFn: async () => {
      const window = getTimeWindow(rangeKey);
      const previous = queryClient.getQueryData<DashboardGlobalHistoryData>(["dashboard-global-history", rangeKey]);
      return getDashboardGlobalHistoryData(window, previous);
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

  const globalData = useMemo<DashboardGlobalData | undefined>(() => {
    if (!globalRealtimeQuery.data && !globalHistoryQuery.data && !globalSnapshotQuery.data) {
      return undefined;
    }
    return {
      realtime_throughput: globalRealtimeQuery.data?.realtime_throughput ?? { step_seconds: 0, items: [] },
      realtime_connections: globalRealtimeQuery.data?.realtime_connections ?? { step_seconds: 0, items: [] },
      realtime_leases: globalRealtimeQuery.data?.realtime_leases ?? { platform_id: "", step_seconds: 0, items: [] },
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
        healthy_egress_ip_count: 0,
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

  const globalError = globalRealtimeQuery.error ?? globalHistoryQuery.error ?? globalSnapshotQuery.error;
  const isInitialLoading =
    !globalData && (globalRealtimeQuery.isLoading || globalHistoryQuery.isLoading || globalSnapshotQuery.isLoading);

  const realtimeThroughputItems = globalData?.realtime_throughput.items;
  const throughputItems = useMemo(
    () => sortTimeSeriesByTimestamp(realtimeThroughputItems ?? [], (item) => item.ts),
    [realtimeThroughputItems],
  );
  const throughputIngress = throughputItems.map((item) => item.ingress_bps);
  const throughputEgress = throughputItems.map((item) => item.egress_bps);
  const throughputLabels = throughputItems.map((item) => item.ts);

  const realtimeConnectionItems = globalData?.realtime_connections.items;
  const connectionItems = useMemo(
    () => sortTimeSeriesByTimestamp(realtimeConnectionItems ?? [], (item) => item.ts),
    [realtimeConnectionItems],
  );
  const connectionsInbound = connectionItems.map((item) => item.inbound_connections);
  const connectionsOutbound = connectionItems.map((item) => item.outbound_connections);
  const connectionsLabels = connectionItems.map((item) => item.ts);

  const realtimeLeaseItems = globalData?.realtime_leases.items;
  const leaseRealtimeItems = useMemo(
    () => sortTimeSeriesByTimestamp(realtimeLeaseItems ?? [], (item) => item.ts),
    [realtimeLeaseItems],
  );
  const leasesValues = leaseRealtimeItems.map((item) => item.active_leases);

  const historyTrafficItems = globalData?.history_traffic.items;
  const trafficItems = useMemo(
    () => sortTimeSeriesByTimestamp(historyTrafficItems ?? [], (item) => item.bucket_start),
    [historyTrafficItems],
  );
  const trafficIngress = trafficItems.map((item) => item.ingress_bytes);
  const trafficEgress = trafficItems.map((item) => item.egress_bytes);
  const trafficLabels = trafficItems.map((item) => item.bucket_start);

  const historyRequestItems = globalData?.history_requests.items;
  const requestItems = useMemo(
    () => sortTimeSeriesByTimestamp(historyRequestItems ?? [], (item) => item.bucket_start),
    [historyRequestItems],
  );
  const requestTotals = requestItems.map((item) => item.total_requests);
  const requestSuccesses = requestItems.map((item) => item.success_requests);
  const requestLabels = requestItems.map((item) => item.bucket_start);

  const historyNodePoolItems = globalData?.history_node_pool.items;
  const nodePoolItems = useMemo(
    () => sortTimeSeriesByTimestamp(historyNodePoolItems ?? [], (item) => item.bucket_start),
    [historyNodePoolItems],
  );
  const nodeTotal = nodePoolItems.map((item) => item.total_nodes);
  const nodeHealthy = nodePoolItems.map((item) => item.healthy_nodes);
  const nodeLabels = nodePoolItems.map((item) => item.bucket_start);

  const historyProbeItems = globalData?.history_probes.items;
  const probeItems = useMemo(
    () => sortTimeSeriesByTimestamp(historyProbeItems ?? [], (item) => item.bucket_start),
    [historyProbeItems],
  );
  const probeCounts = probeItems.map((item) => item.total_count);
  const probeLabels = probeItems.map((item) => item.bucket_start);

  const latestIngress = latestValue(throughputIngress);
  const latestEgress = latestValue(throughputEgress);
  const latestConnections = latestValue(connectionsInbound) + latestValue(connectionsOutbound);
  const latestLeases = latestValue(leasesValues);

  const totalTrafficBytes = sum(trafficIngress) + sum(trafficEgress);
  const totalRequests = sum(requestTotals);
  const successRequests = requestItems.reduce((acc, item) => acc + item.success_requests, 0);

  const snapshotNodePool = globalData?.snapshot_node_pool;
  const uniqueHealthyEgressIPs = snapshotNodePool?.healthy_egress_ip_count ?? 0;
  const nodeHealthRate = snapshotNodePool ? successRate(snapshotNodePool.total_nodes, snapshotNodePool.healthy_nodes) : 0;

  const activeLatencyHistogram = globalData?.snapshot_latency_global.buckets ?? [];

  return (
    <section className="dashboard-page">
      <header className="module-header">
        <div>
          <h2>{t("总览看板")}</h2>
          <p className="module-description">{t("快速发现流量与节点异常，掌握整体运行状态。")}</p>
        </div>
        <div className="dashboard-header-controls">
          <label className="dashboard-control">
            <span>{t("时间范围")}</span>
            <Select value={rangeKey} onChange={(event) => setRangeKey(event.target.value as RangeKey)}>
              {RANGE_OPTIONS.map((item) => (
                <option key={item.key} value={item.key}>
                  {t(item.label)}
                </option>
              ))}
            </Select>
          </label>
        </div>
      </header>

      {globalError ? (
        <div className="callout callout-error">
          <AlertTriangle size={14} />
          <span>{formatApiErrorMessage(globalError, t)}</span>
        </div>
      ) : null}

      <div className="dashboard-kpi-grid">
        <Card className="dashboard-kpi-card">
          <div className="dashboard-kpi-icon waves">
            <Waves size={18} />
          </div>
          <div>
            <p className="dashboard-kpi-label">{t("实时吞吐")}</p>
            <p className="dashboard-kpi-value">{formatBps(latestIngress + latestEgress)}</p>
            <p className="dashboard-kpi-sub">
              {t("下载")} {formatBps(latestIngress)} · {t("上传")} {formatBps(latestEgress)}
            </p>
          </div>
        </Card>

        <Card className="dashboard-kpi-card">
          <div className="dashboard-kpi-icon gauge">
            <Gauge size={18} />
          </div>
          <div>
            <p className="dashboard-kpi-label">{t("实时连接数")}</p>
            <p className="dashboard-kpi-value">{formatCount(latestConnections)}</p>
            <p className="dashboard-kpi-sub">
              {t("入站")} {formatCount(latestValue(connectionsInbound))} · {t("出站")} {formatCount(latestValue(connectionsOutbound))}
            </p>
          </div>
        </Card>

        <Card className="dashboard-kpi-card dashboard-kpi-card-with-badge">
          <div className="dashboard-kpi-icon shield">
            <Shield size={18} />
          </div>
          <div>
            <p className="dashboard-kpi-label">{t("节点健康率")}</p>
            <p className="dashboard-kpi-value">{formatPercent(nodeHealthRate)}</p>
            <p className="dashboard-kpi-sub">
              {t("健康")} {formatCount(snapshotNodePool?.healthy_nodes ?? 0)} / {t("总计")} {formatCount(snapshotNodePool?.total_nodes ?? 0)}
            </p>
          </div>
          <Badge className="dashboard-kpi-badge" variant={nodeHealthRate >= 0.75 ? "success" : "warning"}>
            {formatCount(uniqueHealthyEgressIPs)} IP
          </Badge>
        </Card>

        <Card className="dashboard-kpi-card">
          <div className="dashboard-kpi-icon lease">
            <Layers size={18} />
          </div>
          <div>
            <p className="dashboard-kpi-label">{t("活跃租约数")}</p>
            <p className="dashboard-kpi-value">{formatCount(latestLeases)}</p>
            <p className="dashboard-kpi-sub">{t("来自所有平台租约总和")}</p>
          </div>
        </Card>
      </div>

      <div className="dashboard-main-grid">
        <Card className="dashboard-panel span-2">
          <div className="dashboard-panel-header">
            <h3>{t("吞吐趋势")}</h3>
            <p>{t("实时下载 / 上传速率（bps）")}</p>
          </div>
          <TrendChart
            labels={throughputLabels}
            formatYAxisLabel={formatShortBps}
            series={[
              {
                name: t("下载速率"),
                values: throughputIngress,
                color: "#1076ff",
                fillColor: "rgba(16, 118, 255, 0.14)",
              },
              {
                name: t("上传速率"),
                values: throughputEgress,
                color: "#00a17f",
              },
            ]}
          />
          <div className="dashboard-legend">
            <span>
              <i style={{ background: "#1076ff" }} />
              {t("下载速率")}
            </span>
            <span>
              <i style={{ background: "#00a17f" }} />
              {t("上传速率")}
            </span>
          </div>
        </Card>

        <Card className="dashboard-panel">
          <div className="dashboard-panel-header">
            <h3>{t("连接峰值")}</h3>
            <p>{t("实时入站 / 出站连接")}</p>
          </div>
          <TrendChart
            labels={connectionsLabels}
            formatYAxisLabel={formatShortNumber}
            series={[
              {
                name: t("入站连接"),
                values: connectionsInbound,
                color: "#2467e4",
                fillColor: "rgba(36, 103, 228, 0.12)",
              },
              {
                name: t("出站连接"),
                values: connectionsOutbound,
                color: "#f18f01",
              },
            ]}
          />
          <div className="dashboard-legend">
            <span>
              <i style={{ background: "#2467e4" }} />
              {t("入站连接")}
            </span>
            <span>
              <i style={{ background: "#f18f01" }} />
              {t("出站连接")}
            </span>
          </div>
        </Card>

        <Card className="dashboard-panel span-2">
          <div className="dashboard-panel-header">
            <h3>{t("节点延迟分布")}</h3>
            <p>{t("延迟直方图")}</p>
          </div>

          <Histogram buckets={activeLatencyHistogram} />
        </Card>

        <Card className="dashboard-panel">
          <div className="dashboard-panel-header">
            <h3>{t("节点池趋势")}</h3>
            <p>{t("节点总数 / 健康节点数")}</p>
          </div>
          <TrendChart
            labels={nodeLabels}
            formatYAxisLabel={formatShortNumber}
            series={[
              {
                name: t("节点总数"),
                values: nodeTotal,
                color: "#2d63d8",
                fillColor: "rgba(45, 99, 216, 0.11)",
              },
              {
                name: t("健康节点数"),
                values: nodeHealthy,
                color: "#0c9f68",
              },
            ]}
          />
        </Card>

        <Card className="dashboard-panel">
          <div className="dashboard-panel-header">
            <h3>{t("请求统计")}</h3>
            <p>{t("总请求数 / 成功请求数")}</p>
          </div>
          <TrendChart
            labels={requestLabels}
            formatYAxisLabel={formatShortNumber}
            series={[
              {
                name: t("总请求数"),
                values: requestTotals,
                color: "#2467e4",
              },
              {
                name: t("成功请求数"),
                values: requestSuccesses,
                color: "#0f9d8b",
              },
            ]}
          />
          <div className="dashboard-summary-inline">
            <span>{t("总请求")} {formatCount(totalRequests)}</span>
            <span>{t("成功请求")} {formatCount(successRequests)}</span>
          </div>
        </Card>

        <Card className="dashboard-panel">
          <div className="dashboard-panel-header">
            <h3>{t("流量累计")}</h3>
            <p>{t("窗口内下载 / 上传流量（字节）")}</p>
          </div>
          <TrendChart
            labels={trafficLabels}
            formatYAxisLabel={formatShortBytes}
            series={[
              {
                name: t("下载流量"),
                values: trafficIngress,
                color: "#2068f6",
                fillColor: "rgba(32, 104, 246, 0.12)",
              },
              {
                name: t("上传流量"),
                values: trafficEgress,
                color: "#0f9d8b",
              },
            ]}
          />
          <div className="dashboard-summary-inline">
            <span>{t("总流量")} {formatBytes(totalTrafficBytes)}</span>
          </div>
        </Card>

        <Card className="dashboard-panel">
          <div className="dashboard-panel-header">
            <h3>{t("探测任务量")}</h3>
            <p>{t("历史探测总次数")}</p>
          </div>
          <TrendChart
            labels={probeLabels}
            formatYAxisLabel={formatShortNumber}
            series={[
              {
                name: t("探测次数"),
                values: probeCounts,
                color: "#e26a2c",
                fillColor: "rgba(226, 106, 44, 0.16)",
              },
            ]}
          />
        </Card>

      </div>

      {isInitialLoading ? (
        <div className="callout callout-warning">
          <Server size={14} />
          <span>{t("总览看板数据加载中...")}</span>
        </div>
      ) : null}
    </section>
  );
}
