import { useQuery } from "@tanstack/react-query";
import { createColumnHelper } from "@tanstack/react-table";
import { AlertTriangle, Eraser, RefreshCw, Sparkles, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { DataTable } from "../../components/ui/DataTable";
import { CursorPagination } from "../../components/ui/CursorPagination";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { ApiError } from "../../lib/api-client";
import { formatBytes } from "../../lib/bytes";
import { formatDateTime } from "../../lib/time";
import { getSystemConfig } from "../systemConfig/api";
import { getRequestLog, getRequestLogPayloads, listRequestLogs } from "./api";
import type { RequestLogItem, RequestLogListFilters } from "./types";

type BoolFilter = "all" | "true" | "false";
type ProxyTypeFilter = "all" | "1" | "2";

type FilterDraft = {
  from_local: string;
  to_local: string;
  platform_name: string;
  account: string;
  target_host: string;
  egress_ip: string;
  proxy_type: ProxyTypeFilter;
  net_ok: BoolFilter;
  http_status: string;
  limit: number;
};

const defaultFilters: FilterDraft = {
  from_local: "",
  to_local: "",
  platform_name: "",
  account: "",
  target_host: "",
  egress_ip: "",
  proxy_type: "all",
  net_ok: "all",
  http_status: "",
  limit: 100,
};
const PAGE_SIZE_OPTIONS = [20, 50, 100, 200, 500, 1000, 2000] as const;

const PAYLOAD_TABS = ["request", "response"] as const;
type PayloadTab = (typeof PAYLOAD_TABS)[number];
const EMPTY_LOGS: RequestLogItem[] = [];

function fromApiError(error: unknown): string {
  if (error instanceof ApiError) {
    return `${error.code}: ${error.message}`;
  }
  if (error instanceof Error) {
    return error.message;
  }
  return "未知错误";
}

function toRFC3339(localDateTime: string): string {
  if (!localDateTime) {
    return "";
  }
  const date = new Date(localDateTime);
  if (Number.isNaN(date.getTime())) {
    return "";
  }
  return date.toISOString();
}

function boolFromFilter(value: BoolFilter): boolean | undefined {
  if (value === "true") {
    return true;
  }
  if (value === "false") {
    return false;
  }
  return undefined;
}

function decodeBase64ToText(raw: string): string {
  if (!raw) {
    return "";
  }

  try {
    const binary = atob(raw);
    const bytes = Uint8Array.from(binary, (char) => char.charCodeAt(0));
    return new TextDecoder().decode(bytes);
  } catch {
    return "[Base64 解码失败]";
  }
}

function isFromBeforeTo(fromISO?: string, toISO?: string): boolean {
  if (!fromISO || !toISO) {
    return true;
  }
  return new Date(fromISO).getTime() < new Date(toISO).getTime();
}

function buildActiveFilters(draft: FilterDraft): Omit<RequestLogListFilters, "cursor"> {
  const status = Number(draft.http_status);
  const hasValidStatus = Number.isInteger(status) && status >= 100 && status <= 599;
  const from = toRFC3339(draft.from_local);
  const to = toRFC3339(draft.to_local);
  const validRange = isFromBeforeTo(from, to);

  return {
    from,
    to: validRange ? to : undefined,
    platform_name: draft.platform_name,
    account: draft.account,
    target_host: draft.target_host,
    egress_ip: draft.egress_ip,
    proxy_type: draft.proxy_type === "all" ? undefined : Number(draft.proxy_type),
    net_ok: boolFromFilter(draft.net_ok),
    http_status: hasValidStatus ? status : undefined,
    limit: draft.limit,
    fuzzy: true,
  };
}

function proxyTypeLabel(proxyType: number): string {
  if (proxyType === 1) {
    return "正向代理";
  }
  if (proxyType === 2) {
    return "反向代理";
  }
  return String(proxyType);
}



function splitDateTime(input: string): { date: string; time: string } {
  if (!input) {
    return { date: "-", time: "-" };
  }

  const value = new Date(input);
  if (Number.isNaN(value.getTime())) {
    return { date: input, time: "-" };
  }

  const date = new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).format(value);

  const time = new Intl.DateTimeFormat("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(value);

  return { date, time };
}

export function RequestLogsPage() {
  const [filters, setFilters] = useState<FilterDraft>(defaultFilters);
  const [cursorStack, setCursorStack] = useState<string[]>([""]);
  const [pageIndex, setPageIndex] = useState(0);
  const [selectedLogId, setSelectedLogId] = useState("");
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [payloadTab, setPayloadTab] = useState<PayloadTab>("request");
  const { toasts, dismissToast } = useToast();

  const configQuery = useQuery({
    queryKey: ["system-config"],
    queryFn: getSystemConfig,
    staleTime: 60_000,
  });

  const activeFilters = useMemo(() => buildActiveFilters(filters), [filters]);
  const cursor = cursorStack[pageIndex] || "";

  const rangeInvalid = useMemo(() => {
    const from = toRFC3339(filters.from_local);
    const to = toRFC3339(filters.to_local);
    return Boolean(from && to && !isFromBeforeTo(from, to));
  }, [filters.from_local, filters.to_local]);

  const httpStatusInvalid = useMemo(() => {
    const raw = filters.http_status.trim();
    if (!raw) {
      return false;
    }
    const value = Number(raw);
    return !(Number.isInteger(value) && value >= 100 && value <= 599);
  }, [filters.http_status]);

  const logsQuery = useQuery({
    queryKey: ["request-logs", activeFilters, cursor],
    queryFn: () => listRequestLogs({ ...activeFilters, cursor }),
    refetchInterval: 15_000,
    placeholderData: (prev) => prev,
  });

  const logs = logsQuery.data?.items ?? EMPTY_LOGS;

  const visibleLogs = logs;

  const selectedLog = useMemo(() => {
    if (!selectedLogId) {
      return null;
    }
    return logs.find((item) => item.id === selectedLogId) ?? null;
  }, [logs, selectedLogId]);

  const detailLogId = selectedLogId;
  const drawerVisible = drawerOpen && Boolean(detailLogId);

  const detailQuery = useQuery({
    queryKey: ["request-log", detailLogId],
    queryFn: () => getRequestLog(detailLogId),
    enabled: drawerVisible,
  });

  const detailLog: RequestLogItem | null = detailQuery.data ?? selectedLog ?? null;

  const payloadQuery = useQuery({
    queryKey: ["request-log-payload", detailLogId],
    queryFn: () => getRequestLogPayloads(detailLogId),
    enabled: drawerVisible && Boolean(detailLog?.payload_present),
    staleTime: 30_000,
  });

  useEffect(() => {
    if (!drawerVisible) {
      return;
    }

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") {
        return;
      }
      setDrawerOpen(false);
    };

    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [drawerVisible]);

  const updateFilter = <K extends keyof FilterDraft>(key: K, value: FilterDraft[K]) => {
    setFilters((prev) => ({ ...prev, [key]: value }));
    setCursorStack([""]);
    setPageIndex(0);
    setSelectedLogId("");
    setDrawerOpen(false);
  };

  const resetFilters = () => {
    setFilters(defaultFilters);
    setCursorStack([""]);
    setPageIndex(0);
    setSelectedLogId("");
    setDrawerOpen(false);
  };

  const openDrawer = (logId: string) => {
    setSelectedLogId(logId);
    setDrawerOpen(true);
    setPayloadTab("request");
  };

  const moveNext = () => {
    const nextCursor = logsQuery.data?.next_cursor;
    if (!nextCursor) {
      return;
    }

    setCursorStack((prev) => {
      const expectedNextIndex = pageIndex + 1;
      if (prev[expectedNextIndex] === nextCursor) {
        return prev;
      }
      return [...prev.slice(0, expectedNextIndex), nextCursor];
    });
    setPageIndex((prev) => prev + 1);
    setSelectedLogId("");
    setDrawerOpen(false);
  };

  const movePrev = () => {
    setPageIndex((prev) => Math.max(0, prev - 1));
    setSelectedLogId("");
    setDrawerOpen(false);
  };



  const payloadData = useMemo(() => {
    if (!payloadQuery.data) {
      return { headers: "", body: "" };
    }

    const { req_headers_b64, req_body_b64, resp_headers_b64, resp_body_b64 } = payloadQuery.data;

    switch (payloadTab) {
      case "request": {
        const headers = decodeBase64ToText(req_headers_b64).trimEnd();
        const body = decodeBase64ToText(req_body_b64).trimEnd();
        return { headers, body };
      }
      case "response":
      default: {
        const headers = decodeBase64ToText(resp_headers_b64).trimEnd();
        const body = decodeBase64ToText(resp_body_b64).trimEnd();
        return { headers, body };
      }
    }
  }, [payloadQuery.data, payloadTab]);

  const hasMore = Boolean(logsQuery.data?.has_more && logsQuery.data?.next_cursor);

  const col = useMemo(() => createColumnHelper<RequestLogItem>(), []);

  const logColumns = useMemo(
    () => [
      col.accessor("ts", {
        header: "时间",
        cell: (info) => {
          const timeParts = splitDateTime(info.getValue());
          return (
            <div className="logs-cell-stack logs-time-cell">
              <span>{timeParts.time}</span>
              <small>{timeParts.date}</small>
            </div>
          );
        },
      }),
      col.accessor("proxy_type", {
        header: "代理",
        cell: (info) => {
          const val = info.getValue();
          if (val === 1) return <Badge variant="info">正向</Badge>;
          if (val === 2) return <Badge variant="accent">反向</Badge>;
          return <Badge variant="neutral">{val}</Badge>;
        },
      }),
      col.display({
        id: "platform_account",
        header: "平台 / 账号",
        cell: (info) => {
          const log = info.row.original;
          return (
            <div className="logs-cell-stack">
              <span>{log.platform_name || "-"}</span>
              <small>{log.account || "-"}</small>
            </div>
          );
        },
      }),
      col.display({
        id: "target",
        header: "目标",
        cell: (info) => {
          const log = info.row.original;
          return (
            <div className="logs-cell-stack">
              <span title={log.target_host}>{log.target_host || "-"}</span>
              <small title={log.target_url}>{log.target_url || "-"}</small>
            </div>
          );
        },
      }),
      col.display({
        id: "http",
        header: "HTTP",
        cell: (info) => {
          const log = info.row.original;
          return (
            <div className="logs-cell-stack">
              <span>{log.http_method || "-"}</span>
              <small>{log.http_status || "-"}</small>
            </div>
          );
        },
      }),

      col.accessor("net_ok", {
        header: "网络",
        cell: (info) => (
          <Badge variant={info.getValue() ? "success" : "warning"}>
            {info.getValue() ? "成功" : "失败"}
          </Badge>
        ),
      }),
      col.accessor("duration_ms", {
        header: "耗时",
        cell: (info) => `${info.getValue()} ms`,
      }),
      col.display({
        id: "traffic",
        header: "流量",
        cell: (info) => {
          const log = info.row.original;
          return formatBytes((log.ingress_bytes || 0) + (log.egress_bytes || 0));
        },
      }),
      col.display({
        id: "node",
        header: "节点",
        cell: (info) => {
          const log = info.row.original;
          return (
            <div className="logs-cell-stack">
              {log.node_tag ? (
                <Link
                  to={`/nodes?tag_keyword=${encodeURIComponent(log.node_tag)}`}
                  title={`在节点池搜索 ${log.node_tag}`}
                  onClick={(event) => event.stopPropagation()}
                  style={{
                    color: "var(--accent-primary)",
                    textDecoration: "none",
                    width: "fit-content",
                  }}
                >
                  {log.node_tag}
                </Link>
              ) : (
                <span>-</span>
              )}
              <small title={log.egress_ip}>{log.egress_ip || "-"}</small>
            </div>
          );
        },
      }),
    ],
    [col]
  );

  return (
    <section className="nodes-page">
      <header className="module-header">
        <div>
          <h2>请求日志</h2>
          <p className="module-description">按条件检索请求记录，快速定位问题。</p>
        </div>
        {!configQuery.isLoading && configQuery.data && (
          <Link to="/system-config" style={{ display: "flex", textDecoration: "none" }}>
            <Badge variant={configQuery.data.request_log_enabled ? "success" : "warning"} style={{ cursor: "pointer", fontSize: "13px", padding: "6px 12px" }}>
              {configQuery.data.request_log_enabled ? "当前实时日志记录已开启" : "当前实时日志记录未开启"}
            </Badge>
          </Link>
        )}
      </header>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <Card className="filter-card platform-list-card platform-directory-card">
        <div className="list-card-header">
          <div style={{ display: "flex", flexDirection: "column", gap: "0.75rem", width: "100%" }}>
            {/* 时间与路由信息 */}
            <div
              className="logs-inline-filters"
              style={{ display: "flex", flexWrap: "wrap", gap: "0.75rem", alignItems: "flex-end" }}
            >
              <div style={{ flex: 1, display: "flex", flexDirection: "column", gap: "0.25rem" }}>
                <label htmlFor="logs-from" style={{ fontSize: "0.75rem", color: "var(--text-secondary)" }}>
                  开始时间
                </label>
                <Input
                  id="logs-from"
                  type="datetime-local"
                  value={filters.from_local}
                  onChange={(event) => updateFilter("from_local", event.target.value)}
                  style={{ width: "100%", padding: "4px 8px", fontSize: "0.875rem", minHeight: "32px", height: "32px" }}
                />
              </div>

              <div style={{ flex: 1, display: "flex", flexDirection: "column", gap: "0.25rem" }}>
                <label htmlFor="logs-to" style={{ fontSize: "0.75rem", color: "var(--text-secondary)" }}>
                  结束时间
                </label>
                <Input
                  id="logs-to"
                  type="datetime-local"
                  value={filters.to_local}
                  onChange={(event) => updateFilter("to_local", event.target.value)}
                  style={{ width: "100%", padding: "4px 8px", fontSize: "0.875rem", minHeight: "32px", height: "32px" }}
                />
              </div>

              <div style={{ flex: 1, display: "flex", flexDirection: "column", gap: "0.25rem" }}>
                <label htmlFor="logs-platform-name" style={{ fontSize: "0.75rem", color: "var(--text-secondary)" }}>
                  平台
                </label>
                <Input
                  id="logs-platform-name"
                  value={filters.platform_name}
                  onChange={(event) => updateFilter("platform_name", event.target.value)}
                  style={{ width: "100%", padding: "4px 8px", fontSize: "0.875rem", minHeight: "32px", height: "32px" }}
                />
              </div>

              <div style={{ flex: 1, display: "flex", flexDirection: "column", gap: "0.25rem" }}>
                <label htmlFor="logs-account" style={{ fontSize: "0.75rem", color: "var(--text-secondary)" }}>
                  账号
                </label>
                <Input
                  id="logs-account"
                  value={filters.account}
                  onChange={(event) => updateFilter("account", event.target.value)}
                  style={{ width: "100%", padding: "4px 8px", fontSize: "0.875rem", minHeight: "32px", height: "32px" }}
                />
              </div>

              <div style={{ flex: 1, display: "flex", flexDirection: "column", gap: "0.25rem" }}>
                <label htmlFor="logs-target-host" style={{ fontSize: "0.75rem", color: "var(--text-secondary)" }}>
                  目标主机
                </label>
                <Input
                  id="logs-target-host"
                  value={filters.target_host}
                  onChange={(event) => updateFilter("target_host", event.target.value)}
                  style={{ width: "100%", padding: "4px 8px", fontSize: "0.875rem", minHeight: "32px", height: "32px" }}
                />
              </div>
            </div>

            {/* 网络状态与操作 */}
            <div
              className="logs-inline-filters"
              style={{ display: "flex", flexWrap: "wrap", gap: "0.75rem", alignItems: "flex-end" }}
            >
              <div style={{ flex: 1, display: "flex", flexDirection: "column", gap: "0.25rem" }}>
                <label htmlFor="logs-proxy-type" style={{ fontSize: "0.75rem", color: "var(--text-secondary)" }}>
                  代理类型
                </label>
                <Select
                  id="logs-proxy-type"
                  value={filters.proxy_type}
                  onChange={(event) => updateFilter("proxy_type", event.target.value as ProxyTypeFilter)}
                  style={{ width: "100%", padding: "4px 8px", fontSize: "0.875rem", minHeight: "32px", height: "32px" }}
                >
                  <option value="all">全部</option>
                  <option value="1">正向代理</option>
                  <option value="2">反向代理</option>
                </Select>
              </div>

              <div style={{ flex: 1, display: "flex", flexDirection: "column", gap: "0.25rem" }}>
                <label htmlFor="logs-egress-ip" style={{ fontSize: "0.75rem", color: "var(--text-secondary)" }}>
                  出口 IP
                </label>
                <Input
                  id="logs-egress-ip"
                  value={filters.egress_ip}
                  onChange={(event) => updateFilter("egress_ip", event.target.value)}
                  style={{ width: "100%", padding: "4px 8px", fontSize: "0.875rem", minHeight: "32px", height: "32px" }}
                />
              </div>

              <div style={{ flex: 1, display: "flex", flexDirection: "column", gap: "0.25rem" }}>
                <label htmlFor="logs-net-ok" style={{ fontSize: "0.75rem", color: "var(--text-secondary)" }}>
                  网络状态
                </label>
                <Select
                  id="logs-net-ok"
                  value={filters.net_ok}
                  onChange={(event) => updateFilter("net_ok", event.target.value as BoolFilter)}
                  style={{ width: "100%", padding: "4px 8px", fontSize: "0.875rem", minHeight: "32px", height: "32px" }}
                >
                  <option value="all">全部</option>
                  <option value="true">成功</option>
                  <option value="false">失败</option>
                </Select>
              </div>

              <div style={{ flex: 1, display: "flex", flexDirection: "column", gap: "0.25rem" }}>
                <label htmlFor="logs-http-status" style={{ fontSize: "0.75rem", color: "var(--text-secondary)" }}>
                  HTTP 状态
                </label>
                <Input
                  id="logs-http-status"
                  placeholder="100-599"
                  value={filters.http_status}
                  onChange={(event) => updateFilter("http_status", event.target.value)}
                  style={{ width: "100%", padding: "4px 8px", fontSize: "0.875rem", minHeight: "32px", height: "32px" }}
                />
              </div>

              <div style={{ flex: "0 0 auto", display: "flex", gap: "0.5rem", marginBottom: "0.125rem", marginLeft: "auto" }}>
                <Button
                  size="sm"
                  variant="secondary"
                  onClick={() => void logsQuery.refetch()}
                  disabled={logsQuery.isFetching}
                  style={{ minHeight: "32px", height: "32px", padding: "0 0.75rem", display: "flex", alignItems: "center", gap: "0.25rem" }}
                >
                  <RefreshCw size={14} className={logsQuery.isFetching ? "spin" : undefined} />
                  刷新
                </Button>
                <Button
                  size="sm"
                  variant="secondary"
                  onClick={resetFilters}
                  style={{ minHeight: "32px", height: "32px", padding: "0 0.75rem", display: "flex", alignItems: "center", gap: "0.25rem" }}
                >
                  <Eraser size={14} />
                  重置
                </Button>
              </div>
            </div>
          </div>
        </div>

        {rangeInvalid ? <div className="callout callout-warning">时间范围错误：开始时间必须早于结束时间，已暂不应用结束时间筛选。</div> : null}
        {httpStatusInvalid ? <div className="callout callout-warning">HTTP 状态码需为 100-599 的整数，当前输入暂不应用。</div> : null}
      </Card>

      <Card className="nodes-table-card platform-cards-container subscriptions-table-card">
        {logsQuery.isLoading ? <p className="muted">正在加载日志...</p> : null}

        {logsQuery.isError ? (
          <div className="callout callout-error">
            <AlertTriangle size={14} />
            <span>{fromApiError(logsQuery.error)}</span>
          </div>
        ) : null}

        {!logsQuery.isLoading && !visibleLogs.length ? (
          <div className="empty-box">
            <Sparkles size={16} />
            <p>没有匹配日志</p>
          </div>
        ) : null}

        {visibleLogs.length ? (
          <DataTable
            data={visibleLogs}
            columns={logColumns}
            onRowClick={(log) => openDrawer(log.id)}
            selectedRowId={drawerVisible ? detailLogId : undefined}
            getRowId={(log) => log.id}
            wrapClassName="data-table-wrap-logs"
          />
        ) : null}

        <CursorPagination
          pageIndex={pageIndex}
          hasMore={hasMore}
          pageSize={filters.limit}
          pageSizeOptions={PAGE_SIZE_OPTIONS}
          onPageSizeChange={(limit) => updateFilter("limit", limit)}
          onPrev={movePrev}
          onNext={moveNext}
        />
      </Card>

      {drawerVisible && detailLog ? (
        <div
          className="drawer-overlay"
          role="dialog"
          aria-modal="true"
          aria-label={`请求日志详情 ${detailLog.id}`}
          onClick={() => setDrawerOpen(false)}
        >
          <Card className="drawer-panel" onClick={(event) => event.stopPropagation()}>
            <div className="drawer-header">
              <div>
                <h3>{detailLog.target_host || detailLog.account || "请求日志详情"}</h3>
                <p>{detailLog.id}</p>
              </div>
              <div className="drawer-header-actions">
                <Button variant="ghost" size="sm" aria-label="关闭详情面板" onClick={() => setDrawerOpen(false)}>
                  <X size={16} />
                </Button>
              </div>
            </div>

            <div className="platform-drawer-layout">
              <section className="platform-drawer-section">
                <div className="platform-drawer-section-head">
                  <h4>日志摘要</h4>
                  <p>请求时间、协议结果与平台路由信息。</p>
                </div>

                {detailQuery.isError ? (
                  <div className="callout callout-error">
                    <AlertTriangle size={14} />
                    <span>{fromApiError(detailQuery.error)}</span>
                  </div>
                ) : null}

                <div className="stats-grid">
                  <div>
                    <span>时间</span>
                    <p>{formatDateTime(detailLog.ts)}</p>
                  </div>
                  <div>
                    <span>代理类型</span>
                    <p>
                      {detailLog.proxy_type === 1 ? (
                        <Badge variant="info">正向</Badge>
                      ) : detailLog.proxy_type === 2 ? (
                        <Badge variant="accent">反向</Badge>
                      ) : (
                        proxyTypeLabel(detailLog.proxy_type)
                      )}
                    </p>
                  </div>
                  <div>
                    <span>HTTP</span>
                    <p>
                      {detailLog.http_method || "-"} {detailLog.http_status || "-"}
                    </p>
                  </div>

                  <div>
                    <span>耗时</span>
                    <p>{detailLog.duration_ms} ms</p>
                  </div>
                  <div>
                    <span>平台</span>
                    <p>{detailLog.platform_name || "-"}</p>
                  </div>
                  <div>
                    <span>账号</span>
                    <p>{detailLog.account || "-"}</p>
                  </div>
                  <div>
                    <span>出口 IP</span>
                    <p>{detailLog.egress_ip || "-"}</p>
                  </div>
                  <div>
                    <span>客户端 IP</span>
                    <p>{detailLog.client_ip || "-"}</p>
                  </div>
                </div>
              </section>

              <section className="platform-drawer-section">
                <div className="platform-drawer-section-head">
                  <h4>诊断</h4>
                  <p>异常排查与连接状态分析。</p>
                </div>
                <div style={{
                  backgroundColor: "var(--surface)",
                  border: "1px solid var(--border)",
                  borderRadius: "12px",
                  padding: "16px",
                  fontFamily: "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace",
                  fontSize: "13px",
                  color: "var(--text-secondary)",
                  lineHeight: "1.6",
                }}>
                  {(detailLog.resin_error || detailLog.upstream_stage || detailLog.upstream_err_kind || detailLog.upstream_errno || detailLog.upstream_err_msg) ? (
                    <table style={{ borderCollapse: "collapse", width: "100%" }}>
                      <tbody>
                        {detailLog.resin_error ? (
                          <tr>
                            <td style={{ color: "var(--danger)", fontWeight: 600, paddingBottom: "8px", paddingRight: "16px", whiteSpace: "nowrap", verticalAlign: "top", width: "1%" }}>Resin 错误:</td>
                            <td style={{ color: "var(--text)", paddingBottom: "8px", wordBreak: "break-all", verticalAlign: "top" }}>{detailLog.resin_error}</td>
                          </tr>
                        ) : null}
                        {detailLog.upstream_stage ? (
                          <tr>
                            <td style={{ color: "var(--warning)", fontWeight: 600, paddingBottom: "8px", paddingRight: "16px", whiteSpace: "nowrap", verticalAlign: "top", width: "1%" }}>失败阶段:</td>
                            <td style={{ color: "var(--text)", paddingBottom: "8px", wordBreak: "break-all", verticalAlign: "top" }}>{detailLog.upstream_stage}</td>
                          </tr>
                        ) : null}
                        {detailLog.upstream_err_kind ? (
                          <tr>
                            <td style={{ fontWeight: 600, paddingBottom: "8px", paddingRight: "16px", whiteSpace: "nowrap", verticalAlign: "top", width: "1%" }}>错误类型:</td>
                            <td style={{ color: "var(--text)", paddingBottom: "8px", wordBreak: "break-all", verticalAlign: "top" }}>{detailLog.upstream_err_kind}</td>
                          </tr>
                        ) : null}
                        {detailLog.upstream_errno ? (
                          <tr>
                            <td style={{ fontWeight: 600, paddingBottom: "8px", paddingRight: "16px", whiteSpace: "nowrap", verticalAlign: "top", width: "1%" }}>Errno:</td>
                            <td style={{ color: "var(--text)", paddingBottom: "8px", wordBreak: "break-all", verticalAlign: "top" }}>{detailLog.upstream_errno}</td>
                          </tr>
                        ) : null}
                        {detailLog.upstream_err_msg ? (
                          <tr>
                            <td style={{ fontWeight: 600, paddingBottom: "8px", paddingRight: "16px", whiteSpace: "nowrap", verticalAlign: "top", width: "1%" }}>错误详情:</td>
                            <td style={{ color: "var(--text)", paddingBottom: "8px", wordBreak: "break-all", verticalAlign: "top" }}>{detailLog.upstream_err_msg}</td>
                          </tr>
                        ) : null}
                      </tbody>
                    </table>
                  ) : null}
                  {!detailLog.resin_error && !detailLog.upstream_stage && !detailLog.upstream_err_kind && !detailLog.upstream_err_msg ? (
                    <div style={{ color: "var(--success)", display: "flex", alignItems: "center", gap: "6px" }}>
                      <span style={{ display: "inline-block", width: "8px", height: "8px", borderRadius: "50%", backgroundColor: "var(--success)" }}></span>
                      当前请求未产生异常诊断信息
                    </div>
                  ) : null}
                </div>
              </section>

              <section className="platform-drawer-section">
                <div className="platform-drawer-section-head">
                  <h4>目标与节点</h4>
                  <p>请求目标与命中节点信息。</p>
                </div>

                <div className="stats-grid">
                  <div>
                    <span>目标地址</span>
                    <p>{detailLog.target_host || "-"}</p>
                    <code style={{ display: 'block', marginTop: '4px', fontSize: '11px', color: 'var(--text-muted)', wordBreak: 'break-all' }}>{detailLog.target_url || "-"}</code>
                  </div>

                  <div>
                    <span>流量</span>
                    <p>{formatBytes((detailLog.ingress_bytes || 0) + (detailLog.egress_bytes || 0))}</p>
                    <div style={{ display: 'flex', gap: '8px', marginTop: '4px', fontSize: '11px', color: 'var(--text-muted)' }}>
                      <span>↓ {formatBytes(detailLog.ingress_bytes || 0)}</span>
                      <span>↑ {formatBytes(detailLog.egress_bytes || 0)}</span>
                    </div>
                  </div>

                  <div>
                    <span>节点</span>
                    <p>{detailLog.node_tag || "-"}</p>
                    <code style={{ display: 'block', marginTop: '4px', fontSize: '11px', color: 'var(--text-muted)', wordBreak: 'break-all' }}>{detailLog.node_hash || "-"}</code>
                  </div>
                </div>
              </section>

              <section className="platform-drawer-section">
                <div className="platform-drawer-section-head">
                  <h4>报文内容</h4>
                  <p>查看请求/响应内容。</p>
                </div>

                {!detailLog.payload_present ? (
                  <p className="muted" style={{ fontSize: "13px" }}>该条日志未记录报文内容。</p>
                ) : (
                  <section className="logs-payload-section">
                    <div className="logs-payload-tabs">
                      {PAYLOAD_TABS.map((tab) => {
                        const labelMap: Record<PayloadTab, string> = {
                          request: "请求",
                          response: "响应",
                        };

                        const truncated = payloadQuery.data
                          ? (tab === "request"
                            ? payloadQuery.data.truncated.req_headers || payloadQuery.data.truncated.req_body
                            : payloadQuery.data.truncated.resp_headers || payloadQuery.data.truncated.resp_body)
                          : false;

                        return (
                          <button
                            key={tab}
                            type="button"
                            className={`payload-tab ${payloadTab === tab ? "payload-tab-active" : ""}`}
                            onClick={() => setPayloadTab(tab)}
                          >
                            <span>{labelMap[tab]}</span>
                            {truncated ? <Badge variant="warning">已截断</Badge> : null}
                          </button>
                        );
                      })}
                    </div>

                    {payloadQuery.isError ? (
                      <div className="callout callout-error">
                        <AlertTriangle size={14} />
                        <span>{fromApiError(payloadQuery.error)}</span>
                      </div>
                    ) : null}

                    {payloadQuery.isFetching && !(payloadData.headers || payloadData.body) ? (
                      <div className="callout" style={{ marginTop: "12px", color: "var(--text-secondary)" }}>
                        <RefreshCw size={14} className="spin" />
                        <span>加载报文内容中...</span>
                      </div>
                    ) : (
                      <>
                        <pre className="logs-payload-box" style={{ minHeight: "auto", border: "1px solid var(--border)", marginBottom: "8px" }}>
                          {payloadData.headers || "（空 Headers）"}
                        </pre>
                        <pre className="logs-payload-box">
                          {payloadData.body || "（空 Body）"}
                        </pre>
                      </>
                    )}
                  </section>
                )}
              </section>
            </div>
          </Card>
        </div>
      ) : null}
    </section>
  );
}
