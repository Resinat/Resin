import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createColumnHelper } from "@tanstack/react-table";
import { AlertTriangle, Eraser, Globe, RefreshCw, Sparkles, X, Zap } from "lucide-react";
import { useEffect, useMemo, useState, type CSSProperties } from "react";
import { useLocation } from "react-router-dom";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { DataTable } from "../../components/ui/DataTable";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { OffsetPagination } from "../../components/ui/OffsetPagination";
import { Select } from "../../components/ui/Select";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { ApiError } from "../../lib/api-client";
import { formatDateTime, formatRelativeTime } from "../../lib/time";
import { listPlatforms } from "../platforms/api";
import type { Platform } from "../platforms/types";
import { listSubscriptions } from "../subscriptions/api";
import { getNode, listNodes, probeEgress, probeLatency } from "./api";
import type { NodeSummary } from "./types";
import { getAllRegions, getRegionName } from "./regions";
import type { NodeListFilters, NodeSortBy, SortOrder } from "./types";

type NodeStatusFilter = "all" | "healthy" | "circuit_open" | "error";

type NodeFilterDraft = {
  platform_id: string;
  subscription_id: string;
  tag_keyword: string;
  region: string;
  egress_ip: string;
  status: NodeStatusFilter;
};

const defaultFilterDraft: NodeFilterDraft = {
  platform_id: "",
  subscription_id: "",
  tag_keyword: "",
  region: "",
  egress_ip: "",
  status: "all",
};

const PAGE_SIZE_OPTIONS = [20, 50, 100, 200, 500, 1000, 2000, 5000] as const;
const EMPTY_PLATFORMS: Platform[] = [];
const NODE_FILTER_ITEM_STYLE: CSSProperties = {
  flex: "1 1 120px",
  minWidth: "80px",
  display: "flex",
  flexDirection: "column",
  gap: "0.25rem",
};
const NODE_FILTER_CONTROL_STYLE: CSSProperties = {
  width: "100%",
  padding: "4px 8px",
  fontSize: "0.875rem",
  minHeight: "32px",
  height: "32px",
};

function fromApiError(error: unknown): string {
  if (error instanceof ApiError) {
    return `${error.code}: ${error.message}`;
  }
  if (error instanceof Error) {
    return error.message;
  }
  return "未知错误";
}

function parseBoolParam(value: string | null): boolean | undefined {
  if (value === null) {
    return undefined;
  }

  const normalized = value.trim().toLowerCase();
  if (normalized === "true" || normalized === "1") {
    return true;
  }
  if (normalized === "false" || normalized === "0") {
    return false;
  }

  return undefined;
}

function parseStatusParam(value: string | null): NodeStatusFilter | undefined {
  if (value === null) {
    return undefined;
  }

  const normalized = value.trim().toLowerCase();
  if (normalized === "all" || normalized === "healthy" || normalized === "circuit_open" || normalized === "error") {
    return normalized;
  }

  return undefined;
}

function statusFromQuery(params: URLSearchParams): NodeStatusFilter {
  const explicitStatus = parseStatusParam(params.get("status"));
  if (explicitStatus) {
    return explicitStatus;
  }

  const hasOutbound = parseBoolParam(params.get("has_outbound"));
  const circuitOpen = parseBoolParam(params.get("circuit_open"));

  if (hasOutbound === false) {
    return "error";
  }
  if (hasOutbound === true && circuitOpen === true) {
    return "circuit_open";
  }
  if (hasOutbound === true && circuitOpen === false) {
    return "healthy";
  }

  return "all";
}

function trimQueryValue(params: URLSearchParams, key: string): string {
  return params.get(key)?.trim() ?? "";
}

function draftFromQuery(search: string): NodeFilterDraft {
  const params = new URLSearchParams(search);
  const tagKeyword = trimQueryValue(params, "tag_keyword") || trimQueryValue(params, "tag");

  return {
    platform_id: trimQueryValue(params, "platform_id"),
    subscription_id: trimQueryValue(params, "subscription_id"),
    tag_keyword: tagKeyword,
    region: trimQueryValue(params, "region").toUpperCase(),
    egress_ip: trimQueryValue(params, "egress_ip"),
    status: statusFromQuery(params),
  };
}



function draftToActiveFilters(draft: NodeFilterDraft): NodeListFilters {
  let circuit_open: boolean | undefined = undefined;
  let has_outbound: boolean | undefined = undefined;

  switch (draft.status) {
    case "healthy":
      has_outbound = true;
      circuit_open = false;
      break;
    case "circuit_open":
      has_outbound = true;
      circuit_open = true;
      break;
    case "error":
      has_outbound = false;
      break;
    case "all":
    default:
      break;
  }

  return {
    platform_id: draft.platform_id,
    subscription_id: draft.subscription_id,
    tag_keyword: draft.tag_keyword,
    region: draft.region,
    egress_ip: draft.egress_ip,
    circuit_open,
    has_outbound,
  };
}

function firstTag(node: { tags: { tag: string }[] }): string {
  if (!node.tags.length) {
    return "-";
  }
  return node.tags[0].tag;
}

function isNodeHealthy(node: NodeSummary): boolean {
  return node.has_outbound && !node.circuit_open_since;
}

function referenceLatencyColor(latencyMs: number): string {
  if (!Number.isFinite(latencyMs)) {
    return "var(--text-secondary)";
  }
  if (latencyMs <= 400) {
    return "var(--success)";
  }
  if (latencyMs <= 1000) {
    return "var(--warning)";
  }
  return "var(--danger)";
}

function displayableReferenceLatencyMs(node: NodeSummary): number | null {
  if (!isNodeHealthy(node)) {
    return null;
  }
  const latencyMs = node.reference_latency_ms;
  if (typeof latencyMs !== "number" || !Number.isFinite(latencyMs)) {
    return null;
  }
  return latencyMs;
}


function formatLatency(value: number): string {
  if (!Number.isFinite(value)) {
    return "-";
  }
  return `${value.toFixed(0)} ms`;
}

function sortIndicator(active: boolean, order: SortOrder): string {
  if (!active) {
    return "↕";
  }
  return order === "asc" ? "▲" : "▼";
}

function regionToFlag(region: string | undefined): string {
  if (!region || region.length !== 2) {
    return region || "-";
  }
  const code = region.toUpperCase();
  const flag = String.fromCodePoint(...[...code].map((c) => c.charCodeAt(0) + 127397));
  const name = getRegionName(code);
  return name ? `${flag} ${code} (${name})` : `${flag} ${code}`;
}

export function NodesPage() {
  const location = useLocation();
  const [draftFilters, setDraftFilters] = useState<NodeFilterDraft>(() => draftFromQuery(location.search));
  const [activeFilters, setActiveFilters] = useState<NodeListFilters>(() =>
    draftToActiveFilters(draftFromQuery(location.search))
  );
  const [sortBy, setSortBy] = useState<NodeSortBy>("tag");
  const [sortOrder, setSortOrder] = useState<SortOrder>("asc");
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState<number>(200);
  const [selectedNodeHash, setSelectedNodeHash] = useState("");
  const [drawerOpen, setDrawerOpen] = useState(false);
  const { toasts, showToast, dismissToast } = useToast();

  const queryClient = useQueryClient();

  const allRegions = useMemo(() => getAllRegions(), []);

  const platformsQuery = useQuery({
    queryKey: ["platforms", "all"],
    queryFn: async () => {
      const data = await listPlatforms({
        limit: 100000,
        offset: 0,
      });
      return data.items;
    },
    staleTime: 60_000,
  });
  const platforms = platformsQuery.data ?? EMPTY_PLATFORMS;

  const subscriptionsQuery = useQuery({
    queryKey: ["subscriptions", "all"],
    queryFn: async () => {
      const data = await listSubscriptions({
        limit: 100000,
        offset: 0,
      });
      return data.items;
    },
    staleTime: 60_000,
  });
  const subscriptions = subscriptionsQuery.data ?? [];

  const nodesQuery = useQuery({
    queryKey: ["nodes", activeFilters, sortBy, sortOrder, page, pageSize],
    queryFn: () =>
      listNodes({
        ...activeFilters,
        sort_by: sortBy,
        sort_order: sortOrder,
        limit: pageSize,
        offset: page * pageSize,
      }),
    refetchInterval: 30_000,
    placeholderData: (prev) => prev,
  });

  const nodesPage = nodesQuery.data ?? {
    items: [],
    total: 0,
    limit: pageSize,
    offset: page * pageSize,
    unique_egress_ips: 0,
  };
  const nodes = nodesPage.items;

  const totalPages = Math.max(1, Math.ceil(nodesPage.total / pageSize));

  const selectedNode = useMemo(() => {
    if (!selectedNodeHash) {
      return null;
    }
    return nodes.find((item) => item.node_hash === selectedNodeHash) ?? null;
  }, [nodes, selectedNodeHash]);

  const selectedHash = selectedNode?.node_hash || "";

  const nodeDetailQuery = useQuery({
    queryKey: ["node", selectedHash],
    queryFn: () => getNode(selectedHash),
    enabled: Boolean(selectedHash) && drawerOpen,
    refetchInterval: 30_000,
  });

  const detailNode = nodeDetailQuery.data ?? selectedNode;
  const drawerVisible = drawerOpen && Boolean(detailNode);

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

  const openDrawer = (hash: string) => {
    setSelectedNodeHash(hash);
    setDrawerOpen(true);
  };

  const refreshNodes = async () => {
    await queryClient.invalidateQueries({ queryKey: ["nodes"] });
    if (selectedHash) {
      await queryClient.invalidateQueries({ queryKey: ["node", selectedHash] });
    }
  };

  const probeEgressMutation = useMutation({
    mutationFn: async (hash: string) => probeEgress(hash),
    onSuccess: async (result) => {
      await refreshNodes();
      showToast(
        "success",
        `出口探测完成：出口 IP=${result.egress_ip || "-"}，区域=${result.region || "-"}，延迟=${formatLatency(result.latency_ewma_ms)}`
      );
    },
    onError: async (error) => {
      await refreshNodes();
      showToast("error", fromApiError(error));
    },
  });

  const probeLatencyMutation = useMutation({
    mutationFn: async (hash: string) => probeLatency(hash),
    onSuccess: async (result) => {
      await refreshNodes();
      showToast("success", `延迟探测完成：延迟=${formatLatency(result.latency_ewma_ms)}`);
    },
    onError: async (error) => {
      await refreshNodes();
      showToast("error", fromApiError(error));
    },
  });

  const runProbeEgress = async (hash: string) => {
    await probeEgressMutation.mutateAsync(hash);
  };

  const runProbeLatency = async (hash: string) => {
    await probeLatencyMutation.mutateAsync(hash);
  };

  const handleFilterChange = (key: keyof NodeFilterDraft, value: string) => {
    setDraftFilters((prev) => {
      const next = { ...prev, [key]: value };
      setActiveFilters(draftToActiveFilters(next));
      setSelectedNodeHash("");
      setDrawerOpen(false);
      setPage(0);
      return next;
    });
  };

  const resetFilters = () => {
    setDraftFilters(defaultFilterDraft);
    setActiveFilters(draftToActiveFilters(defaultFilterDraft));
    setSelectedNodeHash("");
    setDrawerOpen(false);
    setPage(0);
  };

  const changeSort = (target: NodeSortBy) => {
    if (sortBy === target) {
      setSortOrder((prev) => (prev === "asc" ? "desc" : "asc"));
    } else {
      setSortBy(target);
      setSortOrder("asc");
    }
    setPage(0);
  };

  const changePageSize = (next: number) => {
    setPageSize(next);
    setPage(0);
  };

  const col = createColumnHelper<NodeSummary>();

  const nodeColumns = useMemo(
    () => [
      col.accessor((row) => firstTag(row), {
        id: "tag",
        header: () => (
          <button type="button" className="table-sort-btn" onClick={() => changeSort("tag")}>
            Tag
            <span>{sortIndicator(sortBy === "tag", sortOrder)}</span>
          </button>
        ),
        cell: (info) => (
          <div className="nodes-tag-cell">
            <span title={info.getValue() as string}>{info.getValue() as string}</span>
          </div>
        ),
      }),
      col.accessor("region", {
        header: () => (
          <button type="button" className="table-sort-btn" onClick={() => changeSort("region")}>
            区域
            <span>{sortIndicator(sortBy === "region", sortOrder)}</span>
          </button>
        ),
        cell: (info) => regionToFlag(info.getValue()),
      }),
      col.accessor("egress_ip", {
        header: "出口 IP",
        cell: (info) => info.getValue() || "-",
      }),
      col.display({
        id: "reference_latency_ms",
        header: "参考延迟",
        cell: (info) => {
          const node = info.row.original;
          const latencyMs = displayableReferenceLatencyMs(node);
          if (latencyMs === null) {
            return "-";
          }
          return (
            <span style={{ color: referenceLatencyColor(latencyMs), fontWeight: 600 }}>
              {formatLatency(latencyMs)}
            </span>
          );
        },
      }),
      col.accessor("last_latency_probe_attempt", {
        header: "上次探测",
        cell: (info) => formatRelativeTime(info.getValue()),
      }),
      col.accessor("failure_count", {
        header: () => (
          <button type="button" className="table-sort-btn" onClick={() => changeSort("failure_count")}>
            连续失败次数
            <span>{sortIndicator(sortBy === "failure_count", sortOrder)}</span>
          </button>
        ),
        cell: (info) => {
          const node = info.row.original;
          return !node.has_outbound ? "-" : node.failure_count;
        },
      }),
      col.display({
        id: "status",
        header: "状态",
        cell: (info) => {
          const node = info.row.original;
          if (!node.has_outbound) return <Badge variant="danger">错误</Badge>;
          if (node.circuit_open_since) return <Badge variant="warning">熔断</Badge>;
          return <Badge variant="success">健康</Badge>;
        },
      }),
      col.accessor("created_at", {
        header: () => (
          <button type="button" className="table-sort-btn" onClick={() => changeSort("created_at")}>
            创建时间
            <span>{sortIndicator(sortBy === "created_at", sortOrder)}</span>
          </button>
        ),
        cell: (info) => formatDateTime(info.getValue()),
      }),
      col.display({
        id: "actions",
        header: "操作",
        cell: (info) => {
          const node = info.row.original;
          return (
            <div className="subscriptions-row-actions" onClick={(event) => event.stopPropagation()}>
              <Button
                size="sm"
                variant="ghost"
                title="触发出口探测"
                onClick={() => void runProbeEgress(node.node_hash)}
                disabled={probeEgressMutation.isPending || probeLatencyMutation.isPending}
              >
                <Globe size={14} />
              </Button>
              <Button
                size="sm"
                variant="ghost"
                title="触发延迟探测"
                onClick={() => void runProbeLatency(node.node_hash)}
                disabled={probeEgressMutation.isPending || probeLatencyMutation.isPending}
              >
                <Zap size={14} />
              </Button>
            </div>
          );
        },
      }),
    ],
    [sortBy, sortOrder, probeEgressMutation.isPending, probeLatencyMutation.isPending]
  );

  return (
    <section className="nodes-page">
      <header className="module-header">
        <div>
          <h2>节点池</h2>
          <p className="module-description">快速定位异常节点并进行探测处理。</p>
        </div>
      </header>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <Card className="filter-card platform-list-card platform-directory-card">
        <div className="list-card-header">
          <div>
            <h3>节点列表</h3>
            <p>共 {nodesPage.total} 个节点，{nodesPage.unique_egress_ips} 个出口 IP</p>
          </div>

          <div
            className="nodes-inline-filters"
            style={{
              display: "flex",
              flexWrap: "wrap",
              gap: "0.5rem",
              alignItems: "flex-end",
            }}
          >
            <div style={NODE_FILTER_ITEM_STYLE}>
              <label htmlFor="node-tag-keyword" style={{ fontSize: "0.75rem", color: "var(--text-secondary)" }}>
                节点名
              </label>
              <Input
                id="node-tag-keyword"
                value={draftFilters.tag_keyword}
                onChange={(event) => handleFilterChange("tag_keyword", event.target.value)}
                placeholder="模糊搜索"
                style={NODE_FILTER_CONTROL_STYLE}
              />
            </div>

            <div style={NODE_FILTER_ITEM_STYLE}>
              <label htmlFor="node-platform-id" style={{ fontSize: "0.75rem", color: "var(--text-secondary)" }}>
                被此平台路由
              </label>
              <Select
                id="node-platform-id"
                value={draftFilters.platform_id}
                onChange={(event) => handleFilterChange("platform_id", event.target.value)}
                style={NODE_FILTER_CONTROL_STYLE}
              >
                <option value="">无限制</option>
                {platforms.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </Select>
            </div>

            <div style={NODE_FILTER_ITEM_STYLE}>
              <label htmlFor="node-subscription-id" style={{ fontSize: "0.75rem", color: "var(--text-secondary)" }}>
                来自此订阅
              </label>
              <Select
                id="node-subscription-id"
                value={draftFilters.subscription_id}
                onChange={(event) => handleFilterChange("subscription_id", event.target.value)}
                style={NODE_FILTER_CONTROL_STYLE}
              >
                <option value="">全部</option>
                {subscriptions.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name}
                  </option>
                ))}
              </Select>
            </div>

            <div style={NODE_FILTER_ITEM_STYLE}>
              <label htmlFor="node-region" style={{ fontSize: "0.75rem", color: "var(--text-secondary)" }}>
                区域
              </label>
              <Select
                id="node-region"
                value={draftFilters.region}
                onChange={(event) => handleFilterChange("region", event.target.value)}
                style={NODE_FILTER_CONTROL_STYLE}
              >
                <option value="">全部</option>
                {allRegions.map((r) => (
                  <option key={r.code} value={r.code}>
                    {r.name}
                  </option>
                ))}
              </Select>
            </div>

            <div style={NODE_FILTER_ITEM_STYLE}>
              <label htmlFor="node-egress-ip" style={{ fontSize: "0.75rem", color: "var(--text-secondary)" }}>
                出口 IP
              </label>
              <Input
                id="node-egress-ip"
                value={draftFilters.egress_ip}
                onChange={(event) => handleFilterChange("egress_ip", event.target.value)}
                placeholder="IP / CIDR"
                style={NODE_FILTER_CONTROL_STYLE}
              />
            </div>

            <div style={NODE_FILTER_ITEM_STYLE}>
              <label htmlFor="node-status" style={{ fontSize: "0.75rem", color: "var(--text-secondary)" }}>
                状态
              </label>
              <Select
                id="node-status"
                value={draftFilters.status}
                onChange={(event) => handleFilterChange("status", event.target.value)}
                style={NODE_FILTER_CONTROL_STYLE}
              >
                <option value="all">全部</option>
                <option value="healthy">健康</option>
                <option value="circuit_open">熔断</option>
                <option value="error">错误</option>
              </Select>
            </div>

            <div style={{ display: "flex", gap: "0.5rem", marginBottom: "0.125rem", marginLeft: "auto" }}>
              <Button size="sm" variant="secondary" onClick={refreshNodes} disabled={nodesQuery.isFetching} style={{ minHeight: "32px", height: "32px", padding: "0 0.75rem", display: "flex", alignItems: "center", gap: "0.25rem" }}>
                <RefreshCw size={16} className={nodesQuery.isFetching ? "spin" : undefined} />
                刷新
              </Button>
              <Button size="sm" variant="secondary" onClick={resetFilters} style={{ minHeight: "32px", height: "32px", padding: "0 0.75rem", display: "flex", alignItems: "center", gap: "0.25rem" }}>
                <Eraser size={16} />
                重置
              </Button>
            </div>
          </div>
        </div>
      </Card>

      <Card className="nodes-table-card platform-cards-container subscriptions-table-card">
        {nodesQuery.isLoading ? <p className="muted">正在加载节点数据...</p> : null}

        {nodesQuery.isError ? (
          <div className="callout callout-error">
            <AlertTriangle size={14} />
            <span>{fromApiError(nodesQuery.error)}</span>
          </div>
        ) : null}

        {!nodesQuery.isLoading && !nodes.length ? (
          <div className="empty-box">
            <Sparkles size={16} />
            <p>没有匹配的节点</p>
          </div>
        ) : null}

        {nodes.length ? (
          <DataTable
            data={nodes}
            columns={nodeColumns}
            onRowClick={(node) => openDrawer(node.node_hash)}
            getRowId={(node) => node.node_hash}
          />
        ) : null}

        <OffsetPagination
          page={page}
          totalPages={totalPages}
          totalItems={nodesPage.total}
          pageSize={pageSize}
          pageSizeOptions={PAGE_SIZE_OPTIONS}
          onPageChange={setPage}
          onPageSizeChange={changePageSize}
        />
      </Card>

      {drawerVisible && detailNode ? (
        <div
          className="drawer-overlay"
          role="dialog"
          aria-modal="true"
          aria-label={`节点详情 ${firstTag(detailNode)}`}
          onClick={() => setDrawerOpen(false)}
        >
          <Card className="drawer-panel" onClick={(event) => event.stopPropagation()}>
            <div className="drawer-header">
              <div>
                <h3>{firstTag(detailNode)}</h3>
                <p>{detailNode.node_hash}</p>
              </div>
              <div className="drawer-header-actions">
                <Button
                  variant="ghost"
                  size="sm"
                  aria-label="关闭详情面板"
                  onClick={() => setDrawerOpen(false)}
                >
                  <X size={16} />
                </Button>
              </div>
            </div>

            <div className="platform-drawer-layout">
              <section className="platform-drawer-section">
                <div className="platform-drawer-section-head">
                  <h4>节点状态</h4>
                  <p>节点的网络出口、探测状态以及失败历史。</p>
                </div>

                <div className="stats-grid">
                  <div>
                    <span>创建时间</span>
                    <p>{formatDateTime(detailNode.created_at)}</p>
                  </div>
                  <div>
                    <span>连续失败</span>
                    <p>{!detailNode.has_outbound ? "-" : detailNode.failure_count}</p>
                  </div>
                  <div>
                    <span>状态</span>
                    <div>
                      {!detailNode.has_outbound ? (
                        <p style={{ color: "var(--danger)" }}>错误</p>
                      ) : detailNode.circuit_open_since ? (
                        <div style={{ display: "flex", alignItems: "baseline", gap: "4px" }}>
                          <p style={{ color: "var(--warning)" }}>熔断</p>
                          <span
                            style={{
                              fontSize: "11px",
                              color: "var(--text-muted)",
                              fontWeight: "normal",
                            }}
                          >
                            ({formatRelativeTime(detailNode.circuit_open_since)})
                          </span>
                        </div>
                      ) : (
                        <p style={{ color: "var(--success)" }}>健康</p>
                      )}
                    </div>
                  </div>
                  <div>
                    <span>出口 / 区域</span>
                    <p>
                      {detailNode.egress_ip || "-"} / {regionToFlag(detailNode.region)}
                    </p>
                  </div>
                  <div>
                    <span>参考延迟</span>
                    {(() => {
                      const latencyMs = displayableReferenceLatencyMs(detailNode);
                      if (latencyMs === null) {
                        return <p>-</p>;
                      }
                      return <p style={{ color: referenceLatencyColor(latencyMs) }}>{formatLatency(latencyMs)}</p>;
                    })()}
                  </div>
                  <div>
                    <span>上次探测</span>
                    <p>{formatDateTime(detailNode.last_latency_probe_attempt || "")}</p>
                  </div>
                </div>

                {detailNode.last_error ? (
                  <div className="callout callout-error">最近错误：{detailNode.last_error}</div>
                ) : null}
              </section>

              <section className="platform-drawer-section tags-section">
                <div className="platform-drawer-section-head">
                  <h4>节点别名</h4>
                </div>
                {!detailNode.tags.length ? (
                  <p className="muted">无节点名信息</p>
                ) : (
                  <div className="tag-list">
                    {detailNode.tags.map((tag) => (
                      <div key={`${tag.subscription_id}:${tag.tag}`} className="tag-item">
                        <p>{tag.tag}</p>
                        <span>{tag.subscription_name}</span>
                        <code>{tag.subscription_id}</code>
                      </div>
                    ))}
                  </div>
                )}
              </section>

              <section className="platform-drawer-section platform-ops-section">
                <div className="platform-drawer-section-head">
                  <h4>运维操作</h4>
                </div>
                <div className="platform-ops-list">
                  <div className="platform-op-item">
                    <div className="platform-op-copy">
                      <h5>出口探测</h5>
                      <p className="platform-op-hint">检查节点当前出口 IP。</p>
                    </div>
                    <Button
                      variant="secondary"
                      onClick={() => void runProbeEgress(detailNode.node_hash)}
                      disabled={probeEgressMutation.isPending || probeLatencyMutation.isPending}
                    >
                      {probeEgressMutation.isPending ? "探测中..." : "触发出口探测"}
                    </Button>
                  </div>
                  <div className="platform-op-item">
                    <div className="platform-op-copy">
                      <h5>延迟探测</h5>
                      <p className="platform-op-hint">检测节点网络延迟。</p>
                    </div>
                    <Button
                      variant="secondary"
                      onClick={() => void runProbeLatency(detailNode.node_hash)}
                      disabled={probeEgressMutation.isPending || probeLatencyMutation.isPending}
                    >
                      {probeLatencyMutation.isPending ? "探测中..." : "触发延迟探测"}
                    </Button>
                  </div>
                </div>
              </section>
            </div>
          </Card>
        </div>
      ) : null}
    </section>
  );
}
