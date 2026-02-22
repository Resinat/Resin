import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, Eraser, Eye, RefreshCw, Sparkles, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { CursorPagination } from "../../components/ui/CursorPagination";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { ApiError } from "../../lib/api-client";
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
  limit: 50,
};

const PAYLOAD_TABS = ["req_headers", "req_body", "resp_headers", "resp_body"] as const;
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
    return "[base64 decode failed]";
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

export function RequestLogsPage() {
  const [filters, setFilters] = useState<FilterDraft>(defaultFilters);
  const [cursorStack, setCursorStack] = useState<string[]>([""]);
  const [pageIndex, setPageIndex] = useState(0);
  const [selectedLogId, setSelectedLogId] = useState("");
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [payloadForId, setPayloadForId] = useState("");
  const [payloadTab, setPayloadTab] = useState<PayloadTab>("req_headers");
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
  const payloadOpen = drawerVisible && Boolean(payloadForId) && payloadForId === detailLogId;

  const detailQuery = useQuery({
    queryKey: ["request-log", detailLogId],
    queryFn: () => getRequestLog(detailLogId),
    enabled: drawerVisible,
  });

  const detailLog: RequestLogItem | null = detailQuery.data ?? selectedLog ?? null;

  const payloadQuery = useQuery({
    queryKey: ["request-log-payload", detailLogId],
    queryFn: () => getRequestLogPayloads(detailLogId),
    enabled: payloadOpen,
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
    setPayloadForId("");
  };

  const resetFilters = () => {
    setFilters(defaultFilters);
    setCursorStack([""]);
    setPageIndex(0);
    setSelectedLogId("");
    setDrawerOpen(false);
    setPayloadForId("");
  };

  const openDrawer = (logId: string) => {
    setSelectedLogId(logId);
    setDrawerOpen(true);
    setPayloadForId("");
    setPayloadTab("req_headers");
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
    setPayloadForId("");
  };

  const movePrev = () => {
    setPageIndex((prev) => Math.max(0, prev - 1));
    setSelectedLogId("");
    setDrawerOpen(false);
    setPayloadForId("");
  };

  const loadPayload = () => {
    if (!detailLogId) {
      return;
    }
    setPayloadForId(detailLogId);
    setPayloadTab("req_headers");
  };

  const payloadText = useMemo(() => {
    if (!payloadQuery.data) {
      return "";
    }

    switch (payloadTab) {
      case "req_headers":
        return decodeBase64ToText(payloadQuery.data.req_headers_b64);
      case "req_body":
        return decodeBase64ToText(payloadQuery.data.req_body_b64);
      case "resp_headers":
        return decodeBase64ToText(payloadQuery.data.resp_headers_b64);
      case "resp_body":
      default:
        return decodeBase64ToText(payloadQuery.data.resp_body_b64);
    }
  }, [payloadQuery.data, payloadTab]);

  const hasMore = Boolean(logsQuery.data?.has_more && logsQuery.data?.next_cursor);

  return (
    <section className="nodes-page">
      <header className="module-header">
        <div>
          <h2>请求日志</h2>
          <p className="module-description">筛选条件修改后立即生效。</p>
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
                  Platform
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
                  Account
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
                  Target Host
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
                  Proxy Type
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
                  Egress IP
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
                  <option value="true">ok</option>
                  <option value="false">failed</option>
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
          <div className="nodes-table-wrap">
            <table className="nodes-table subscriptions-table">
              <thead>
                <tr>
                  <th>时间</th>
                  <th>代理</th>
                  <th>平台 / 账号</th>
                  <th>目标</th>
                  <th>HTTP</th>
                  <th>网络</th>
                  <th>耗时</th>
                  <th>节点</th>
                </tr>
              </thead>
              <tbody>
                {visibleLogs.map((log) => {
                  const isSelected = drawerVisible && log.id === detailLogId;
                  return (
                    <tr
                      key={log.id}
                      className={isSelected ? "nodes-row-selected" : "clickable-row"}
                      onClick={() => openDrawer(log.id)}
                    >
                      <td>{formatDateTime(log.ts)}</td>
                      <td>{proxyTypeLabel(log.proxy_type)}</td>
                      <td>
                        <div className="logs-cell-stack">
                          <span>{log.platform_name || "-"}</span>
                          <small>{log.account || "-"}</small>
                        </div>
                      </td>
                      <td>
                        <div className="logs-cell-stack">
                          <span title={log.target_host}>{log.target_host || "-"}</span>
                          <small title={log.target_url}>{log.target_url || "-"}</small>
                        </div>
                      </td>
                      <td>
                        <div className="logs-cell-stack">
                          <span>{log.http_method || "-"}</span>
                          <small>{log.http_status || "-"}</small>
                        </div>
                      </td>
                      <td>
                        <Badge variant={log.net_ok ? "success" : "warning"}>{log.net_ok ? "ok" : "failed"}</Badge>
                      </td>
                      <td>{log.duration_ms} ms</td>
                      <td>
                        <div className="logs-cell-stack">
                          <span title={log.node_tag}>{log.node_tag || "-"}</span>
                          <small title={log.egress_ip}>{log.egress_ip || "-"}</small>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        ) : null}

        <CursorPagination
          pageIndex={pageIndex}
          hasMore={hasMore}
          pageSize={filters.limit}
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
                <Badge variant={detailLog.net_ok ? "success" : "warning"}>{detailLog.net_ok ? "Net OK" : "Net Failed"}</Badge>
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
                    <span>Proxy Type</span>
                    <p>{proxyTypeLabel(detailLog.proxy_type)}</p>
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
                    <span>Platform</span>
                    <p>{detailLog.platform_name || "-"}</p>
                  </div>
                  <div>
                    <span>Account</span>
                    <p>{detailLog.account || "-"}</p>
                  </div>
                  <div>
                    <span>Egress IP</span>
                    <p>{detailLog.egress_ip || "-"}</p>
                  </div>
                  <div>
                    <span>Client IP</span>
                    <p>{detailLog.client_ip || "-"}</p>
                  </div>
                </div>
              </section>

              <section className="platform-drawer-section">
                <div className="platform-drawer-section-head">
                  <h4>目标与节点</h4>
                  <p>请求目标与命中节点信息。</p>
                </div>

                <div className="logs-detail-block">
                  <h4>Target</h4>
                  <p>{detailLog.target_host || "-"}</p>
                  <code>{detailLog.target_url || "-"}</code>
                </div>

                <div className="logs-detail-block">
                  <h4>Node</h4>
                  <p>{detailLog.node_tag || "-"}</p>
                  <code>{detailLog.node_hash || "-"}</code>
                </div>
              </section>

              <section className="platform-drawer-section platform-ops-section">
                <div className="platform-drawer-section-head">
                  <h4>Payload</h4>
                  <p>按需加载并查看请求/响应内容。</p>
                </div>

                <div className="platform-ops-list">
                  <div className="platform-op-item">
                    <div className="platform-op-copy">
                      <h5>查看 Payload</h5>
                      <p className="platform-op-hint">仅在需要时加载，避免影响列表浏览性能。</p>
                    </div>
                    <Button
                      variant="secondary"
                      onClick={loadPayload}
                      disabled={!detailLog.payload_present || payloadQuery.isFetching}
                      title={!detailLog.payload_present ? "该条日志未记录 payload。" : undefined}
                    >
                      <Eye size={14} />
                      {payloadQuery.isFetching ? "加载中..." : "查看 Payload"}
                    </Button>
                  </div>
                </div>


                {payloadOpen ? (
                  <section className="logs-payload-section">
                    <div className="logs-payload-tabs">
                      {PAYLOAD_TABS.map((tab) => {
                        const labelMap: Record<PayloadTab, string> = {
                          req_headers: "Req Headers",
                          req_body: "Req Body",
                          resp_headers: "Resp Headers",
                          resp_body: "Resp Body",
                        };

                        const truncated = payloadQuery.data ? payloadQuery.data.truncated[tab] : false;

                        return (
                          <button
                            key={tab}
                            type="button"
                            className={`payload-tab ${payloadTab === tab ? "payload-tab-active" : ""}`}
                            onClick={() => setPayloadTab(tab)}
                          >
                            <span>{labelMap[tab]}</span>
                            {truncated ? <Badge variant="warning">truncated</Badge> : null}
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

                    <pre className="logs-payload-box">{payloadText || "(empty)"}</pre>
                  </section>
                ) : null}
              </section>
            </div>
          </Card>
        </div>
      ) : null}
    </section>
  );
}
