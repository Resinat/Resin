import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, RefreshCw, Search, Sparkles, Zap } from "lucide-react";
import { useMemo, useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { ApiError } from "../../lib/api-client";
import { formatDateTime } from "../../lib/time";
import { getNode, listNodes, probeEgress, probeLatency } from "./api";
import type { NodeListFilters, NodeSortBy, SortOrder } from "./types";

type BoolFilter = "all" | "true" | "false";

type NodeFilterDraft = {
  platform_id: string;
  subscription_id: string;
  region: string;
  egress_ip: string;
  probed_since_local: string;
  circuit_open: BoolFilter;
  has_outbound: BoolFilter;
};

const defaultFilterDraft: NodeFilterDraft = {
  platform_id: "",
  subscription_id: "",
  region: "",
  egress_ip: "",
  probed_since_local: "",
  circuit_open: "all",
  has_outbound: "all",
};

const PAGE_SIZE_OPTIONS = [20, 50, 100, 200] as const;

function fromApiError(error: unknown): string {
  if (error instanceof ApiError) {
    return `${error.code}: ${error.message}`;
  }
  if (error instanceof Error) {
    return error.message;
  }
  return "未知错误";
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

function draftToActiveFilters(draft: NodeFilterDraft): NodeListFilters {
  return {
    platform_id: draft.platform_id,
    subscription_id: draft.subscription_id,
    region: draft.region,
    egress_ip: draft.egress_ip,
    probed_since: toRFC3339(draft.probed_since_local),
    circuit_open: boolFromFilter(draft.circuit_open),
    has_outbound: boolFromFilter(draft.has_outbound),
  };
}

function firstTag(node: { tags: { tag: string }[] }): string {
  if (!node.tags.length) {
    return "-";
  }
  return node.tags[0].tag;
}

function extraTagCount(node: { tags: unknown[] }): number {
  if (node.tags.length <= 1) {
    return 0;
  }
  return node.tags.length - 1;
}

function formatLatency(value: number): string {
  if (!Number.isFinite(value)) {
    return "-";
  }
  return `${value.toFixed(1)} ms`;
}

function sortIndicator(active: boolean, order: SortOrder): string {
  if (!active) {
    return "↕";
  }
  return order === "asc" ? "▲" : "▼";
}

export function NodesPage() {
  const [draftFilters, setDraftFilters] = useState<NodeFilterDraft>(defaultFilterDraft);
  const [activeFilters, setActiveFilters] = useState<NodeListFilters>(draftToActiveFilters(defaultFilterDraft));
  const [sortBy, setSortBy] = useState<NodeSortBy>("tag");
  const [sortOrder, setSortOrder] = useState<SortOrder>("asc");
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState<number>(50);
  const [search, setSearch] = useState("");
  const [selectedNodeHash, setSelectedNodeHash] = useState("");
  const [message, setMessage] = useState<{ tone: "success" | "error"; text: string } | null>(null);

  const queryClient = useQueryClient();

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
  };
  const nodes = nodesPage.items;

  const visibleNodes = useMemo(() => {
    const keyword = search.trim().toLowerCase();
    if (!keyword) {
      return nodes;
    }

    return nodes.filter((node) => {
      const matchedTag = node.tags.some((item) => item.tag.toLowerCase().includes(keyword));
      return (
        node.node_hash.toLowerCase().includes(keyword) ||
        (node.region || "").toLowerCase().includes(keyword) ||
        (node.egress_ip || "").toLowerCase().includes(keyword) ||
        matchedTag
      );
    });
  }, [nodes, search]);

  const totalPages = Math.max(1, Math.ceil(nodesPage.total / pageSize));
  const pageStart = nodesPage.total === 0 ? 0 : nodesPage.offset + 1;
  const pageEnd = Math.min(nodesPage.offset + nodes.length, nodesPage.total);

  const selectedNode = useMemo(() => {
    if (!visibleNodes.length) {
      return null;
    }
    return visibleNodes.find((item) => item.node_hash === selectedNodeHash) ?? visibleNodes[0];
  }, [visibleNodes, selectedNodeHash]);

  const selectedHash = selectedNode?.node_hash || "";

  const nodeDetailQuery = useQuery({
    queryKey: ["node", selectedHash],
    queryFn: () => getNode(selectedHash),
    enabled: Boolean(selectedHash),
    refetchInterval: 30_000,
  });

  const detailNode = nodeDetailQuery.data ?? selectedNode;

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
      setMessage({
        tone: "success",
        text: `出口探测完成：egress=${result.egress_ip || "-"}，latency=${formatLatency(result.latency_ewma_ms)}`,
      });
    },
    onError: (error) => {
      setMessage({ tone: "error", text: fromApiError(error) });
    },
  });

  const probeLatencyMutation = useMutation({
    mutationFn: async (hash: string) => probeLatency(hash),
    onSuccess: async (result) => {
      await refreshNodes();
      setMessage({ tone: "success", text: `延迟探测完成：latency=${formatLatency(result.latency_ewma_ms)}` });
    },
    onError: (error) => {
      setMessage({ tone: "error", text: fromApiError(error) });
    },
  });

  const runProbeEgress = async (hash: string) => {
    await probeEgressMutation.mutateAsync(hash);
  };

  const runProbeLatency = async (hash: string) => {
    await probeLatencyMutation.mutateAsync(hash);
  };

  const applyFilters = () => {
    setActiveFilters(draftToActiveFilters(draftFilters));
    setSelectedNodeHash("");
    setPage(0);
  };

  const resetFilters = () => {
    setDraftFilters(defaultFilterDraft);
    setActiveFilters(draftToActiveFilters(defaultFilterDraft));
    setSelectedNodeHash("");
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

  return (
    <section className="nodes-page">
      <header className="module-header">
        <div>
          <p className="eyebrow">Control Plane</p>
          <h2>节点池</h2>
          <p className="module-description">节点管理主视图采用服务端分页表格，支持表头排序与行内探测动作。</p>
        </div>
        <Button onClick={() => void refreshNodes()} disabled={nodesQuery.isFetching}>
          <RefreshCw size={16} className={nodesQuery.isFetching ? "spin" : undefined} />
          刷新数据
        </Button>
      </header>

      {message ? (
        <div className={message.tone === "success" ? "callout callout-success" : "callout callout-error"}>
          {message.text}
        </div>
      ) : null}

      <Card className="filter-card">
        <div className="filter-grid">
          <div className="field-group">
            <label className="field-label" htmlFor="node-platform-id">
              Platform ID
            </label>
            <Input
              id="node-platform-id"
              value={draftFilters.platform_id}
              onChange={(event) => setDraftFilters((prev) => ({ ...prev, platform_id: event.target.value }))}
            />
          </div>

          <div className="field-group">
            <label className="field-label" htmlFor="node-subscription-id">
              Subscription ID
            </label>
            <Input
              id="node-subscription-id"
              value={draftFilters.subscription_id}
              onChange={(event) => setDraftFilters((prev) => ({ ...prev, subscription_id: event.target.value }))}
            />
          </div>

          <div className="field-group">
            <label className="field-label" htmlFor="node-region">
              Region
            </label>
            <Input
              id="node-region"
              value={draftFilters.region}
              onChange={(event) => setDraftFilters((prev) => ({ ...prev, region: event.target.value }))}
            />
          </div>

          <div className="field-group">
            <label className="field-label" htmlFor="node-egress-ip">
              Egress IP
            </label>
            <Input
              id="node-egress-ip"
              value={draftFilters.egress_ip}
              onChange={(event) => setDraftFilters((prev) => ({ ...prev, egress_ip: event.target.value }))}
            />
          </div>

          <div className="field-group">
            <label className="field-label" htmlFor="node-probed-since">
              Probed Since
            </label>
            <Input
              id="node-probed-since"
              type="datetime-local"
              value={draftFilters.probed_since_local}
              onChange={(event) => setDraftFilters((prev) => ({ ...prev, probed_since_local: event.target.value }))}
            />
          </div>

          <div className="field-group">
            <label className="field-label" htmlFor="node-circuit-open">
              Circuit Open
            </label>
            <Select
              id="node-circuit-open"
              value={draftFilters.circuit_open}
              onChange={(event) =>
                setDraftFilters((prev) => ({ ...prev, circuit_open: event.target.value as BoolFilter }))
              }
            >
              <option value="all">全部</option>
              <option value="true">true</option>
              <option value="false">false</option>
            </Select>
          </div>

          <div className="field-group">
            <label className="field-label" htmlFor="node-has-outbound">
              Has Outbound
            </label>
            <Select
              id="node-has-outbound"
              value={draftFilters.has_outbound}
              onChange={(event) =>
                setDraftFilters((prev) => ({ ...prev, has_outbound: event.target.value as BoolFilter }))
              }
            >
              <option value="all">全部</option>
              <option value="true">true</option>
              <option value="false">false</option>
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

      <Card className="nodes-table-card">
        <div className="nodes-toolbar">
          <label className="search-box" htmlFor="node-search">
            <Search size={14} />
            <Input
              id="node-search"
              placeholder="当前页过滤：hash / tag / region / egress"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
          </label>
          <p className="nodes-count">
            当前页匹配 {visibleNodes.length} 条 · 后端总数 {nodesPage.total} 条
          </p>
        </div>

        {nodesQuery.isLoading ? <p className="muted">正在加载节点数据...</p> : null}

        {nodesQuery.isError ? (
          <div className="callout callout-error">
            <AlertTriangle size={14} />
            <span>{fromApiError(nodesQuery.error)}</span>
          </div>
        ) : null}

        {!nodesQuery.isLoading && !visibleNodes.length ? (
          <div className="empty-box">
            <Sparkles size={16} />
            <p>没有匹配的节点</p>
          </div>
        ) : null}

        {visibleNodes.length ? (
          <div className="nodes-table-wrap">
            <table className="nodes-table">
              <thead>
                <tr>
                  <th>
                    <button type="button" className="table-sort-btn" onClick={() => changeSort("tag")}>
                      Tag
                      <span>{sortIndicator(sortBy === "tag", sortOrder)}</span>
                    </button>
                  </th>
                  <th>
                    <button type="button" className="table-sort-btn" onClick={() => changeSort("region")}>
                      Region
                      <span>{sortIndicator(sortBy === "region", sortOrder)}</span>
                    </button>
                  </th>
                  <th>Egress IP</th>
                  <th>上次探测</th>
                  <th>
                    <button type="button" className="table-sort-btn" onClick={() => changeSort("failure_count")}>
                      Failure
                      <span>{sortIndicator(sortBy === "failure_count", sortOrder)}</span>
                    </button>
                  </th>
                  <th>Outbound</th>
                  <th>Circuit</th>
                  <th>
                    <button type="button" className="table-sort-btn" onClick={() => changeSort("created_at")}>
                      Created
                      <span>{sortIndicator(sortBy === "created_at", sortOrder)}</span>
                    </button>
                  </th>
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>
                {visibleNodes.map((node) => {
                  const isSelected = node.node_hash === selectedHash;
                  const tagText = firstTag(node);
                  const tagExtra = extraTagCount(node);
                  return (
                    <tr
                      key={node.node_hash}
                      className={isSelected ? "nodes-row-selected" : undefined}
                      onClick={() => setSelectedNodeHash(node.node_hash)}
                    >
                      <td>
                        <div className="nodes-tag-cell">
                          <span>{tagText}</span>
                          {tagExtra > 0 ? <small>+{tagExtra}</small> : null}
                        </div>
                      </td>
                      <td>{node.region || "-"}</td>
                      <td>{node.egress_ip || "-"}</td>
                      <td>{formatDateTime(node.last_latency_probe_attempt || "")}</td>
                      <td>{node.failure_count}</td>
                      <td>
                        <Badge variant={node.has_outbound ? "success" : "warning"}>
                          {node.has_outbound ? "ready" : "none"}
                        </Badge>
                      </td>
                      <td>
                        {node.circuit_open_since ? (
                          <Badge variant="warning">open</Badge>
                        ) : (
                          <Badge variant="success">ok</Badge>
                        )}
                      </td>
                      <td>{formatDateTime(node.created_at)}</td>
                      <td>
                        <div className="nodes-row-actions" onClick={(event) => event.stopPropagation()}>
                          <Button
                            size="sm"
                            variant="secondary"
                            onClick={() => void runProbeEgress(node.node_hash)}
                            disabled={probeEgressMutation.isPending || probeLatencyMutation.isPending}
                          >
                            <Zap size={13} />
                            出口
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => void runProbeLatency(node.node_hash)}
                            disabled={probeEgressMutation.isPending || probeLatencyMutation.isPending}
                          >
                            <Zap size={13} />
                            延迟
                          </Button>
                        </div>
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
            第 {page + 1} / {totalPages} 页 · 显示 {pageStart}-{pageEnd} / {nodesPage.total}
          </p>
          <div className="nodes-pagination-controls">
            <label className="nodes-page-size">
              <span>每页</span>
              <Select value={String(pageSize)} onChange={(event) => changePageSize(Number(event.target.value))}>
                {PAGE_SIZE_OPTIONS.map((size) => (
                  <option key={size} value={size}>
                    {size}
                  </option>
                ))}
              </Select>
            </label>

            <Button variant="secondary" size="sm" onClick={() => setPage((prev) => Math.max(0, prev - 1))} disabled={page <= 0}>
              上一页
            </Button>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setPage((prev) => Math.min(totalPages - 1, prev + 1))}
              disabled={page >= totalPages - 1}
            >
              下一页
            </Button>
          </div>
        </div>
      </Card>

      {detailNode ? (
        <Card className="nodes-detail-card">
          <div className="detail-header">
            <div>
              <h3>{firstTag(detailNode)}</h3>
              <p>{detailNode.node_hash}</p>
            </div>
            <Badge variant={detailNode.circuit_open_since ? "warning" : "success"}>
              {detailNode.circuit_open_since ? "Circuit Open" : "Healthy"}
            </Badge>
          </div>

          <div className="stats-grid">
            <div>
              <span>Created At</span>
              <p>{formatDateTime(detailNode.created_at)}</p>
            </div>
            <div>
              <span>Failure Count</span>
              <p>{detailNode.failure_count}</p>
            </div>
            <div>
              <span>Has Outbound</span>
              <p>{detailNode.has_outbound ? "true" : "false"}</p>
            </div>
            <div>
              <span>Egress IP</span>
              <p>{detailNode.egress_ip || "-"}</p>
            </div>
            <div>
              <span>Region</span>
              <p>{detailNode.region || "-"}</p>
            </div>
            <div>
              <span>上次探测</span>
              <p>{formatDateTime(detailNode.last_latency_probe_attempt || "")}</p>
            </div>
          </div>

          {detailNode.last_error ? (
            <div className="callout callout-error">Last Error: {detailNode.last_error}</div>
          ) : (
            <div className="callout callout-success">节点当前没有最近错误</div>
          )}

          <section className="tags-section">
            <h4>Node Tags</h4>
            {!detailNode.tags.length ? (
              <p className="muted">无 tag 信息</p>
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

          <div className="detail-actions">
            <Button
              onClick={() => void runProbeEgress(detailNode.node_hash)}
              disabled={probeEgressMutation.isPending || probeLatencyMutation.isPending}
            >
              <Zap size={14} />
              触发出口探测
            </Button>

            <Button
              variant="secondary"
              onClick={() => void runProbeLatency(detailNode.node_hash)}
              disabled={probeEgressMutation.isPending || probeLatencyMutation.isPending}
            >
              <Zap size={14} />
              触发延迟探测
            </Button>
          </div>
        </Card>
      ) : null}
    </section>
  );
}
