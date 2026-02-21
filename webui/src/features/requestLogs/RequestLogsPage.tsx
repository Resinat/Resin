import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, Download, RefreshCw, Search, Sparkles } from "lucide-react";
import { useMemo, useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { ApiError } from "../../lib/api-client";
import { formatDateTime } from "../../lib/time";
import { getRequestLog, getRequestLogPayloads, listRequestLogs } from "./api";
import type { RequestLogItem, RequestLogListFilters } from "./types";

type BoolFilter = "all" | "true" | "false";
type ProxyTypeFilter = "all" | "1" | "2";

type FilterDraft = {
  from_local: string;
  to_local: string;
  platform_id: string;
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
  platform_id: "",
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

function buildActiveFilters(draft: FilterDraft): Omit<RequestLogListFilters, "cursor"> {
  const status = Number(draft.http_status);
  const hasValidStatus = Number.isInteger(status) && status >= 100 && status <= 599;

  return {
    from: toRFC3339(draft.from_local),
    to: toRFC3339(draft.to_local),
    platform_id: draft.platform_id,
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

function isFromBeforeTo(fromISO?: string, toISO?: string): boolean {
  if (!fromISO || !toISO) {
    return true;
  }
  return new Date(fromISO).getTime() < new Date(toISO).getTime();
}

export function RequestLogsPage() {
  const [draft, setDraft] = useState<FilterDraft>(defaultFilters);
  const [activeFilters, setActiveFilters] = useState<Omit<RequestLogListFilters, "cursor">>(
    buildActiveFilters(defaultFilters),
  );
  const [cursorStack, setCursorStack] = useState<string[]>([""]);
  const [pageIndex, setPageIndex] = useState(0);
  const [search, setSearch] = useState("");
  const [selectedLogId, setSelectedLogId] = useState("");
  const { toasts, showToast, dismissToast } = useToast();
  const [payloadForId, setPayloadForId] = useState("");
  const [payloadTab, setPayloadTab] = useState<PayloadTab>("req_headers");

  const cursor = cursorStack[pageIndex] || "";

  const logsQuery = useQuery({
    queryKey: ["request-logs", activeFilters, cursor],
    queryFn: () => listRequestLogs({ ...activeFilters, cursor }),
    refetchInterval: 15_000,
    placeholderData: (prev) => prev,
  });

  const logs = logsQuery.data?.items ?? EMPTY_LOGS;

  const visibleLogs = useMemo(() => {
    const keyword = search.trim().toLowerCase();
    if (!keyword) {
      return logs;
    }

    return logs.filter((log) => {
      return (
        log.id.toLowerCase().includes(keyword) ||
        log.account.toLowerCase().includes(keyword) ||
        log.target_host.toLowerCase().includes(keyword) ||
        log.platform_name.toLowerCase().includes(keyword) ||
        log.node_tag.toLowerCase().includes(keyword)
      );
    });
  }, [logs, search]);

  const selectedLog = useMemo(() => {
    if (!visibleLogs.length) {
      return null;
    }
    return visibleLogs.find((item) => item.id === selectedLogId) ?? visibleLogs[0];
  }, [visibleLogs, selectedLogId]);

  const detailLogId = selectedLog?.id || "";
  const payloadOpen = Boolean(payloadForId) && payloadForId === detailLogId;

  const detailQuery = useQuery({
    queryKey: ["request-log", detailLogId],
    queryFn: () => getRequestLog(detailLogId),
    enabled: Boolean(detailLogId),
  });

  const detailLog: RequestLogItem | null = detailQuery.data ?? selectedLog ?? null;

  const payloadQuery = useQuery({
    queryKey: ["request-log-payload", detailLogId],
    queryFn: () => getRequestLogPayloads(detailLogId),
    enabled: payloadOpen && Boolean(detailLogId),
    staleTime: 30_000,
  });

  const applyFilters = () => {
    const next = buildActiveFilters(draft);
    if (!isFromBeforeTo(next.from, next.to)) {
      showToast("error", "时间范围错误：from 必须早于 to。");
      return;
    }

    if (draft.http_status && next.http_status === undefined) {
      showToast("error", "HTTP Status 必须是 100-599 的整数。");
      return;
    }

    setActiveFilters(next);
    setCursorStack([""]);
    setPageIndex(0);
    setSelectedLogId("");
    setPayloadForId("");
  };

  const resetFilters = () => {
    setDraft(defaultFilters);
    setActiveFilters(buildActiveFilters(defaultFilters));
    setCursorStack([""]);
    setPageIndex(0);
    setSelectedLogId("");
    setPayloadForId("");

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
    setPayloadForId("");
  };

  const movePrev = () => {
    setPageIndex((prev) => Math.max(0, prev - 1));
    setSelectedLogId("");
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
    <section className="logs-page">
      <header className="module-header">
        <div>
          <h2>请求日志</h2>
          <p className="module-description">游标分页日志工作台，支持条件检索、详情分析与 payload 解码查看。</p>
        </div>
        <Button onClick={() => void logsQuery.refetch()} disabled={logsQuery.isFetching}>
          <RefreshCw size={16} className={logsQuery.isFetching ? "spin" : undefined} />
          刷新
        </Button>
      </header>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <Card className="filter-card">
        <div className="filter-grid logs-filter-grid">
          <div className="field-group">
            <label className="field-label" htmlFor="logs-from">
              From
            </label>
            <Input
              id="logs-from"
              type="datetime-local"
              value={draft.from_local}
              onChange={(event) => setDraft((prev) => ({ ...prev, from_local: event.target.value }))}
            />
          </div>

          <div className="field-group">
            <label className="field-label" htmlFor="logs-to">
              To
            </label>
            <Input
              id="logs-to"
              type="datetime-local"
              value={draft.to_local}
              onChange={(event) => setDraft((prev) => ({ ...prev, to_local: event.target.value }))}
            />
          </div>

          <div className="field-group">
            <label className="field-label" htmlFor="logs-platform-id">
              Platform ID
            </label>
            <Input
              id="logs-platform-id"
              value={draft.platform_id}
              onChange={(event) => setDraft((prev) => ({ ...prev, platform_id: event.target.value }))}
            />
          </div>

          <div className="field-group">
            <label className="field-label" htmlFor="logs-platform-name">
              Platform Name
            </label>
            <Input
              id="logs-platform-name"
              value={draft.platform_name}
              onChange={(event) => setDraft((prev) => ({ ...prev, platform_name: event.target.value }))}
            />
          </div>

          <div className="field-group">
            <label className="field-label" htmlFor="logs-account">
              Account
            </label>
            <Input
              id="logs-account"
              value={draft.account}
              onChange={(event) => setDraft((prev) => ({ ...prev, account: event.target.value }))}
            />
          </div>

          <div className="field-group">
            <label className="field-label" htmlFor="logs-target-host">
              Target Host
            </label>
            <Input
              id="logs-target-host"
              value={draft.target_host}
              onChange={(event) => setDraft((prev) => ({ ...prev, target_host: event.target.value }))}
            />
          </div>

          <div className="field-group">
            <label className="field-label" htmlFor="logs-egress-ip">
              Egress IP
            </label>
            <Input
              id="logs-egress-ip"
              value={draft.egress_ip}
              onChange={(event) => setDraft((prev) => ({ ...prev, egress_ip: event.target.value }))}
            />
          </div>

          <div className="field-group">
            <label className="field-label" htmlFor="logs-proxy-type">
              Proxy Type
            </label>
            <Select
              id="logs-proxy-type"
              value={draft.proxy_type}
              onChange={(event) => setDraft((prev) => ({ ...prev, proxy_type: event.target.value as ProxyTypeFilter }))}
            >
              <option value="all">全部</option>
              <option value="1">1 (Forward)</option>
              <option value="2">2 (Reverse)</option>
            </Select>
          </div>

          <div className="field-group">
            <label className="field-label" htmlFor="logs-net-ok">
              Net OK
            </label>
            <Select
              id="logs-net-ok"
              value={draft.net_ok}
              onChange={(event) => setDraft((prev) => ({ ...prev, net_ok: event.target.value as BoolFilter }))}
            >
              <option value="all">全部</option>
              <option value="true">true</option>
              <option value="false">false</option>
            </Select>
          </div>

          <div className="field-group">
            <label className="field-label" htmlFor="logs-http-status">
              HTTP Status
            </label>
            <Input
              id="logs-http-status"
              placeholder="100-599"
              value={draft.http_status}
              onChange={(event) => setDraft((prev) => ({ ...prev, http_status: event.target.value }))}
            />
          </div>

          <div className="field-group">
            <label className="field-label" htmlFor="logs-limit">
              每页条数
            </label>
            <Select
              id="logs-limit"
              value={String(draft.limit)}
              onChange={(event) => setDraft((prev) => ({ ...prev, limit: Number(event.target.value) }))}
            >
              <option value="20">20</option>
              <option value="50">50</option>
              <option value="100">100</option>
              <option value="200">200</option>
            </Select>
          </div>
        </div>

        <div className="detail-actions">
          <Button onClick={applyFilters}>应用筛选</Button>
          <Button variant="secondary" onClick={resetFilters}>
            重置
          </Button>
        </div>
      </Card>

      <Card className="logs-table-card">
        <div className="logs-toolbar">
          <label className="search-box" htmlFor="logs-search">
            <Search size={14} />
            <Input
              id="logs-search"
              placeholder="当前页过滤：id/account/target/platform/tag"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
          </label>
          <p className="nodes-count">第 {pageIndex + 1} 页 · 当前页 {visibleLogs.length} 条</p>
        </div>

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
          <div className="logs-table-wrap">
            <table className="logs-table">
              <thead>
                <tr>
                  <th>Time</th>
                  <th>Proxy</th>
                  <th>Platform / Account</th>
                  <th>Target</th>
                  <th>HTTP</th>
                  <th>Net</th>
                  <th>Duration</th>
                  <th>Node</th>
                  <th>Payload</th>
                </tr>
              </thead>
              <tbody>
                {visibleLogs.map((log) => {
                  const isSelected = log.id === detailLogId;
                  return (
                    <tr
                      key={log.id}
                      className={isSelected ? "nodes-row-selected" : undefined}
                      onClick={() => {
                        setSelectedLogId(log.id);
                        setPayloadForId("");
                      }}
                    >
                      <td>{formatDateTime(log.ts)}</td>
                      <td>{log.proxy_type}</td>
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
                          <small title={log.node_hash}>{log.node_hash || "-"}</small>
                        </div>
                      </td>
                      <td>
                        {log.payload_present ? <Badge variant="neutral">yes</Badge> : <Badge variant="warning">no</Badge>}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        ) : null}

        <div className="nodes-pagination">
          <p className="nodes-pagination-meta">
            游标分页 · 当前 cursor {cursor ? "已设置" : "首页"} · {hasMore ? "存在下一页" : "无更多数据"}
          </p>
          <div className="nodes-pagination-controls">
            <Button variant="secondary" size="sm" onClick={movePrev} disabled={pageIndex <= 0}>
              上一页
            </Button>
            <Button variant="secondary" size="sm" onClick={moveNext} disabled={!hasMore}>
              下一页
            </Button>
          </div>
        </div>
      </Card>

      {detailLog ? (
        <Card className="logs-detail-card">
          <div className="detail-header">
            <div>
              <h3>Log #{detailLog.id}</h3>
              <p>{formatDateTime(detailLog.ts)}</p>
            </div>
            <Badge variant={detailLog.net_ok ? "success" : "warning"}>{detailLog.net_ok ? "Net OK" : "Net Failed"}</Badge>
          </div>

          <div className="stats-grid">
            <div>
              <span>Proxy Type</span>
              <p>{detailLog.proxy_type}</p>
            </div>
            <div>
              <span>HTTP</span>
              <p>
                {detailLog.http_method || "-"} {detailLog.http_status || "-"}
              </p>
            </div>
            <div>
              <span>Duration</span>
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

          <div className="detail-actions">
            <Button onClick={loadPayload} disabled={!detailLog.payload_present || payloadQuery.isFetching}>
              <Download size={14} />
              {payloadQuery.isFetching ? "加载中..." : "加载 Payload"}
            </Button>
          </div>

          {!detailLog.payload_present ? (
            <div className="callout callout-warning">该条日志未记录 payload。</div>
          ) : null}

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

                  const truncated = payloadQuery.data
                    ? payloadQuery.data.truncated[
                    tab as "req_headers" | "req_body" | "resp_headers" | "resp_body"
                    ]
                    : false;

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
        </Card>
      ) : null}
    </section>
  );
}
