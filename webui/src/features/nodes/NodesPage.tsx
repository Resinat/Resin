import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createColumnHelper } from "@tanstack/react-table";
import { AlertTriangle, Ban, CircleCheck, Eraser, Globe, RefreshCw, Sparkles, Zap } from "lucide-react";
import { useMemo, useRef, useState, type CSSProperties } from "react";
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
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatDateTime, formatRelativeTime } from "../../lib/time";
import { listPlatforms } from "../platforms/api";
import type { Platform } from "../platforms/types";
import { listSubscriptions } from "../subscriptions/api";
import { disableNode, enableNode, getNode, listNodes, probeEgress, probeLatency } from "./api";
import { NodeDetailDrawer } from "./NodeDetailDrawer";
import { NodeLeasesModal } from "./NodeLeasesModal";
import type { NodeSummary } from "./types";
import { getAllRegions } from "./regions";
import {
  displayableReferenceLatencyMs,
  firstTag,
  formatLatency,
  getNodeDisplayStatus,
  referenceLatencyColor,
  regionToFlag,
} from "./nodeFormat";
import type { NodeListFilters, NodeSortBy, SortOrder } from "./types";

type NodeStatusFilter = "all" | "healthy" | "circuit_open" | "error" | "disabled" | "manually_disabled";
type ProbeAction = "egress" | "latency";

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
  if (
    normalized === "all" ||
    normalized === "healthy" ||
    normalized === "circuit_open" ||
    normalized === "error" ||
    normalized === "disabled" ||
    normalized === "manually_disabled"
  ) {
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
  const enabled = parseBoolParam(params.get("enabled"));
  const manuallyDisabled = parseBoolParam(params.get("manually_disabled"));

  if (manuallyDisabled === true) {
    return "manually_disabled";
  }
  if (enabled === false) {
    return "disabled";
  }

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
  let enabled: boolean | undefined = undefined;
  let manually_disabled: boolean | undefined = undefined;

  switch (draft.status) {
    case "healthy":
      enabled = true;
      has_outbound = true;
      circuit_open = false;
      manually_disabled = false;
      break;
    case "circuit_open":
      enabled = true;
      has_outbound = true;
      circuit_open = true;
      manually_disabled = false;
      break;
    case "error":
      enabled = true;
      has_outbound = false;
      manually_disabled = false;
      break;
    case "disabled":
      enabled = false;
      manually_disabled = false;
      break;
    case "manually_disabled":
      manually_disabled = true;
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
    enabled,
    manually_disabled,
    circuit_open,
    has_outbound,
  };
}

function sortIndicator(active: boolean, order: SortOrder): string {
  if (!active) {
    return "↕";
  }
  return order === "asc" ? "▲" : "▼";
}

export function NodesPage() {
  const { t } = useI18n();
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
  const [leasesModalNodeHash, setLeasesModalNodeHash] = useState<string | null>(null);
  const [pendingEgressHashes, setPendingEgressHashes] = useState<Set<string>>(() => new Set());
  const [pendingLatencyHashes, setPendingLatencyHashes] = useState<Set<string>>(() => new Set());
  const { toasts, showToast, dismissToast } = useToast();
  const pendingEgressHashesRef = useRef<Set<string>>(new Set());
  const pendingLatencyHashesRef = useRef<Set<string>>(new Set());

  const queryClient = useQueryClient();

  const allRegions = getAllRegions();

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
    unique_healthy_egress_ips: 0,
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
        t("出口探测完成：出口 IP={{ip}}，区域={{region}}，延迟={{latency}}", {
          ip: result.egress_ip || "-",
          region: result.region || "-",
          latency: formatLatency(result.latency_ewma_ms),
        })
      );
    },
    onError: async (error) => {
      await refreshNodes();
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const probeLatencyMutation = useMutation({
    mutationFn: async (hash: string) => probeLatency(hash),
    onSuccess: async (result) => {
      await refreshNodes();
      showToast("success", t("延迟探测完成：延迟={{latency}}", { latency: formatLatency(result.latency_ewma_ms) }));
    },
    onError: async (error) => {
      await refreshNodes();
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const disableNodeMutation = useMutation({
    mutationFn: async (hash: string) => disableNode(hash),
    onSuccess: async (result) => {
      await refreshNodes();
      const count = result.released_lease_count ?? 0;
      if (count > 0) {
        showToast("success", t("已禁用，解除 {{count}} 个绑定", { count }));
      } else {
        showToast("success", t("已禁用"));
      }
    },
    onError: async (error) => {
      await refreshNodes();
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const enableNodeMutation = useMutation({
    mutationFn: async (hash: string) => enableNode(hash),
    onSuccess: async () => {
      await refreshNodes();
      showToast("success", t("已启用"));
    },
    onError: async (error) => {
      await refreshNodes();
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const runDisableNode = async (hash: string) => {
    try {
      await disableNodeMutation.mutateAsync(hash);
    } catch {
      // Mutation callbacks already surface the failure to the user.
    }
  };

  const runEnableNode = async (hash: string) => {
    try {
      await enableNodeMutation.mutateAsync(hash);
    } catch {
      // Mutation callbacks already surface the failure to the user.
    }
  };

  const isDisableActionPending = (hash: string): boolean =>
    (disableNodeMutation.isPending && disableNodeMutation.variables === hash) ||
    (enableNodeMutation.isPending && enableNodeMutation.variables === hash);

  const markProbePending = (hash: string, action: ProbeAction): boolean => {
    if (action === "egress") {
      if (pendingEgressHashesRef.current.has(hash)) {
        return false;
      }
      const next = new Set(pendingEgressHashesRef.current);
      next.add(hash);
      pendingEgressHashesRef.current = next;
      setPendingEgressHashes(next);
      return true;
    }

    if (pendingLatencyHashesRef.current.has(hash)) {
      return false;
    }
    const next = new Set(pendingLatencyHashesRef.current);
    next.add(hash);
    pendingLatencyHashesRef.current = next;
    setPendingLatencyHashes(next);
    return true;
  };

  const clearProbePending = (hash: string, action: ProbeAction) => {
    if (action === "egress") {
      if (!pendingEgressHashesRef.current.has(hash)) {
        return;
      }
      const next = new Set(pendingEgressHashesRef.current);
      next.delete(hash);
      pendingEgressHashesRef.current = next;
      setPendingEgressHashes(next);
      return;
    }

    if (!pendingLatencyHashesRef.current.has(hash)) {
      return;
    }
    const next = new Set(pendingLatencyHashesRef.current);
    next.delete(hash);
    pendingLatencyHashesRef.current = next;
    setPendingLatencyHashes(next);
  };

  const isProbePending = (hash: string, action: ProbeAction): boolean =>
    action === "egress" ? pendingEgressHashes.has(hash) : pendingLatencyHashes.has(hash);

  const runProbeEgress = async (hash: string) => {
    if (!markProbePending(hash, "egress")) {
      return;
    }
    try {
      await probeEgressMutation.mutateAsync(hash);
    } catch {
      // Mutation callbacks already surface the failure to the user.
    } finally {
      clearProbePending(hash, "egress");
    }
  };

  const runProbeLatency = async (hash: string) => {
    if (!markProbePending(hash, "latency")) {
      return;
    }
    try {
      await probeLatencyMutation.mutateAsync(hash);
    } catch {
      // Mutation callbacks already surface the failure to the user.
    } finally {
      clearProbePending(hash, "latency");
    }
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

  const nodeColumns = [
    col.accessor((row) => firstTag(row), {
      id: "tag",
      header: () => (
        <button type="button" className="table-sort-btn" onClick={() => changeSort("tag")}>
          {t("节点名")}
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
          {t("区域")}
          <span>{sortIndicator(sortBy === "region", sortOrder)}</span>
        </button>
      ),
      cell: (info) => {
        const val = regionToFlag(info.getValue());
        return (
          <div style={{ maxWidth: "100px", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={val}>
            {val}
          </div>
        );
      },
    }),
    col.accessor("egress_ip", {
      header: t("出口 IP"),
      cell: (info) => {
        const val = info.getValue() || "-";
        return (
          <div style={{ maxWidth: "100px", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={val}>
            {val}
          </div>
        );
      },
    }),
    col.display({
      id: "reference_latency_ms",
      header: t("参考延迟"),
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
      header: t("上次探测"),
      cell: (info) => formatRelativeTime(info.getValue()),
    }),
    col.accessor("failure_count", {
      header: () => (
        <button type="button" className="table-sort-btn" onClick={() => changeSort("failure_count")}>
          {t("连续失败")}
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
      header: t("状态"),
      cell: (info) => {
        const node = info.row.original;
        const status = getNodeDisplayStatus(node);
        if (status === "manually_disabled") return <Badge variant="danger">{t("手动禁用")}</Badge>;
        if (status === "disabled") return <Badge variant="neutral">{t("禁用")}</Badge>;
        if (status === "error") return <Badge variant="danger">{t("错误")}</Badge>;
        if (status === "pending_test") return <Badge variant="muted">{t("待测")}</Badge>;
        if (status === "circuit_open") return <Badge variant="warning">{t("熔断")}</Badge>;
        return <Badge variant="success">{t("健康")}</Badge>;
      },
    }),
    col.accessor("lease_count", {
      header: () => (
        <button type="button" className="table-sort-btn" onClick={() => changeSort("lease_count")}>
          {t("租约数")}
          <span>{sortIndicator(sortBy === "lease_count", sortOrder)}</span>
        </button>
      ),
      cell: (info) => {
        const node = info.row.original;
        const count = node.lease_count ?? 0;
        return (
          <button
            type="button"
            className="lease-count-link"
            onClick={(event) => {
              event.stopPropagation();
              setLeasesModalNodeHash(node.node_hash);
            }}
            disabled={count === 0}
            title={count === 0 ? t("暂无租约") : t("查看租约详情")}
          >
            {count}
          </button>
        );
      },
    }),
    col.accessor("created_at", {
      header: () => (
        <button type="button" className="table-sort-btn" onClick={() => changeSort("created_at")}>
          {t("创建时间")}
          <span>{sortIndicator(sortBy === "created_at", sortOrder)}</span>
        </button>
      ),
      cell: (info) => {
        const val = formatDateTime(info.getValue());
        if (val === "-") return val;
        const parts = val.split(" ");
        if (parts.length >= 2) {
          return (
            <div className="logs-cell-stack">
              <span>{parts[0]}</span>
              <small>{parts.slice(1).join(" ")}</small>
            </div>
          );
        }
        return val;
      },
    }),
    col.display({
      id: "actions",
      header: t("操作"),
      cell: (info) => {
        const node = info.row.original;
        const disablePending = isDisableActionPending(node.node_hash);
        return (
          <div className="subscriptions-row-actions" onClick={(event) => event.stopPropagation()}>
            <Button
              size="sm"
              variant="ghost"
              title={t("触发出口探测")}
              onClick={() => void runProbeEgress(node.node_hash)}
              disabled={isProbePending(node.node_hash, "egress")}
            >
              <Globe size={14} />
            </Button>
            <Button
              size="sm"
              variant="ghost"
              title={t("触发延迟探测")}
              onClick={() => void runProbeLatency(node.node_hash)}
              disabled={isProbePending(node.node_hash, "latency")}
            >
              <Zap size={14} />
            </Button>
            {node.manually_disabled ? (
              <Button
                size="sm"
                variant="ghost"
                title={t("启用节点")}
                onClick={() => void runEnableNode(node.node_hash)}
                disabled={disablePending}
              >
                <CircleCheck size={14} />
              </Button>
            ) : (
              <Button
                size="sm"
                variant="ghost"
                title={t("禁用节点（立即解除全部租约）")}
                onClick={() => void runDisableNode(node.node_hash)}
                disabled={disablePending}
              >
                <Ban size={14} />
              </Button>
            )}
          </div>
        );
      },
    }),
  ];

  return (
    <section className="nodes-page">
      <header className="module-header">
        <div>
          <h2>{t("节点池")}</h2>
          <p className="module-description">{t("快速定位异常节点并进行探测处理。")}</p>
        </div>
      </header>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <Card className="filter-card platform-list-card platform-directory-card">
        <div className="list-card-header">
          <div>
            <h3>{t("节点列表")}</h3>
            <p>{t("共 {{total}} 个节点，{{healthy}} 个健康 IP", { total: nodesPage.total, healthy: nodesPage.unique_healthy_egress_ips })}</p>
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
                {t("节点名")}
              </label>
              <Input
                id="node-tag-keyword"
                value={draftFilters.tag_keyword}
                onChange={(event) => handleFilterChange("tag_keyword", event.target.value)}
                placeholder={t("模糊搜索")}
                style={NODE_FILTER_CONTROL_STYLE}
              />
            </div>

            <div style={NODE_FILTER_ITEM_STYLE}>
              <label htmlFor="node-platform-id" style={{ fontSize: "0.75rem", color: "var(--text-secondary)" }}>
                {t("被此平台路由")}
              </label>
              <Select
                id="node-platform-id"
                value={draftFilters.platform_id}
                onChange={(event) => handleFilterChange("platform_id", event.target.value)}
                style={NODE_FILTER_CONTROL_STYLE}
              >
                <option value="">{t("无限制")}</option>
                {platforms.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </Select>
            </div>

            <div style={NODE_FILTER_ITEM_STYLE}>
              <label htmlFor="node-subscription-id" style={{ fontSize: "0.75rem", color: "var(--text-secondary)" }}>
                {t("来自此订阅")}
              </label>
              <Select
                id="node-subscription-id"
                value={draftFilters.subscription_id}
                onChange={(event) => handleFilterChange("subscription_id", event.target.value)}
                style={NODE_FILTER_CONTROL_STYLE}
              >
                <option value="">{t("全部")}</option>
                {subscriptions.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name}
                  </option>
                ))}
              </Select>
            </div>

            <div style={NODE_FILTER_ITEM_STYLE}>
              <label htmlFor="node-region" style={{ fontSize: "0.75rem", color: "var(--text-secondary)" }}>
                {t("区域")}
              </label>
              <Select
                id="node-region"
                value={draftFilters.region}
                onChange={(event) => handleFilterChange("region", event.target.value)}
                style={NODE_FILTER_CONTROL_STYLE}
              >
                <option value="">{t("全部")}</option>
                {allRegions.map((r) => (
                  <option key={r.code} value={r.code}>
                    {r.name}
                  </option>
                ))}
              </Select>
            </div>

            <div style={NODE_FILTER_ITEM_STYLE}>
              <label htmlFor="node-egress-ip" style={{ fontSize: "0.75rem", color: "var(--text-secondary)" }}>
                {t("出口 IP")}
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
                {t("状态")}
              </label>
              <Select
                id="node-status"
                value={draftFilters.status}
                onChange={(event) => handleFilterChange("status", event.target.value)}
                style={NODE_FILTER_CONTROL_STYLE}
              >
                <option value="all">{t("全部")}</option>
                <option value="healthy">{t("健康")}</option>
                <option value="circuit_open">{t("熔断 / 待测")}</option>
                <option value="error">{t("错误")}</option>
                <option value="disabled">{t("禁用")}</option>
                <option value="manually_disabled">{t("手动禁用")}</option>
              </Select>
            </div>

            <div style={{ display: "flex", gap: "0.5rem", marginBottom: "0.125rem", marginLeft: "auto" }}>
              <Button size="sm" variant="secondary" onClick={refreshNodes} disabled={nodesQuery.isFetching} style={{ minHeight: "32px", height: "32px", padding: "0 0.75rem", display: "flex", alignItems: "center", gap: "0.25rem" }}>
                <RefreshCw size={16} className={nodesQuery.isFetching ? "spin" : undefined} />
                {t("刷新")}
              </Button>
              <Button size="sm" variant="secondary" onClick={resetFilters} style={{ minHeight: "32px", height: "32px", padding: "0 0.75rem", display: "flex", alignItems: "center", gap: "0.25rem" }}>
                <Eraser size={16} />
                {t("重置")}
              </Button>
            </div>
          </div>
        </div>
      </Card>

      <Card className="nodes-table-card platform-cards-container subscriptions-table-card">
        {nodesQuery.isLoading ? <p className="muted">{t("正在加载节点数据...")}</p> : null}

        {nodesQuery.isError ? (
          <div className="callout callout-error">
            <AlertTriangle size={14} />
            <span>{formatApiErrorMessage(nodesQuery.error, t)}</span>
          </div>
        ) : null}

        {!nodesQuery.isLoading && !nodes.length ? (
          <div className="empty-box">
            <Sparkles size={16} />
            <p>{t("没有匹配的节点")}</p>
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
        <NodeDetailDrawer
          node={detailNode}
          onClose={() => setDrawerOpen(false)}
          onProbeEgress={(hash) => void runProbeEgress(hash)}
          onProbeLatency={(hash) => void runProbeLatency(hash)}
          isEgressProbePending={(hash) => isProbePending(hash, "egress")}
          isLatencyProbePending={(hash) => isProbePending(hash, "latency")}
        />
      ) : null}

      {leasesModalNodeHash
        ? (() => {
            const targetNode =
              nodes.find((item) => item.node_hash === leasesModalNodeHash) ?? null;
            if (!targetNode) {
              return null;
            }
            return (
              <NodeLeasesModal
                node={targetNode}
                platformId={activeFilters.platform_id || undefined}
                onClose={() => setLeasesModalNodeHash(null)}
                showToast={showToast}
              />
            );
          })()
        : null}
    </section>
  );
}
