import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createColumnHelper } from "@tanstack/react-table";
import { AlertTriangle, ArrowDown, ArrowUp, ArrowUpDown, Link2Off, Plus, Search, Sparkles } from "lucide-react";
import { type ReactNode, useState } from "react";
import { Button } from "../../components/ui/Button";
import { DataTable } from "../../components/ui/DataTable";
import { Input } from "../../components/ui/Input";
import { OffsetPagination } from "../../components/ui/OffsetPagination";
import { Select } from "../../components/ui/Select";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatRelativeTime } from "../../lib/time";
import { getNode, listNodes, probeEgress, probeLatency } from "../nodes/api";
import { NodeDetailDrawer } from "../nodes/NodeDetailDrawer";
import { formatLatency, referenceLatencyColor } from "../nodes/nodeFormat";
import { bindPlatformLease, deletePlatformLease, listPlatformLeases } from "./api";
import type { LeaseResponse, Platform } from "./types";

const PAGE_SIZE_OPTIONS = [20, 50, 100] as const;

const columnHelper = createColumnHelper<LeaseResponse>();

type SortField = "account" | "node_tag" | "egress_ip" | "created_at" | "expiry" | "last_accessed";
type SortOrder = "asc" | "desc";

type Props = {
  platform: Platform;
  showToast: (type: "success" | "error", message: string) => void;
};

export function PlatformLeasesPanel({ platform, showToast }: Props) {
  const { t } = useI18n();
  const queryClient = useQueryClient();

  const [search, setSearch] = useState("");
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState<number>(50);
  const [bindOpen, setBindOpen] = useState(false);
  const [bindAccount, setBindAccount] = useState("");
  const [selectedNodeHash, setSelectedNodeHash] = useState("");
  const [detailNodeHash, setDetailNodeHash] = useState("");
  const [sortBy, setSortBy] = useState<SortField>("account");
  const [sortOrder, setSortOrder] = useState<SortOrder>("asc");

  const queryKey = ["platform-leases", platform.id, search, page, pageSize, sortBy, sortOrder];

  const leasesQuery = useQuery({
    queryKey,
    queryFn: () =>
      listPlatformLeases(platform.id, {
        limit: pageSize,
        offset: page * pageSize,
        account: search || undefined,
        fuzzy: true,
        sort_by: sortBy,
        sort_order: sortOrder,
      }),
    refetchInterval: 15_000,
    placeholderData: (prev) => prev,
  });

  const leasesPage = leasesQuery.data ?? { items: [], total: 0, limit: pageSize, offset: 0 };
  const leases = leasesPage.items;
  const totalPages = Math.max(1, Math.ceil(leasesPage.total / pageSize));

  const nodesQuery = useQuery({
    queryKey: ["platform-nodes", platform.id],
    queryFn: () => listNodes({ platform_id: platform.id, limit: 10000 }),
    enabled: bindOpen,
  });

  const nodeDetailQuery = useQuery({
    queryKey: ["node", detailNodeHash],
    queryFn: () => getNode(detailNodeHash),
    enabled: Boolean(detailNodeHash),
    refetchInterval: 30_000,
  });

  const sortedNodes = (nodesQuery.data?.items ?? []).slice().sort((a, b) => {
    const aLat = a.reference_latency_ms;
    const bLat = b.reference_latency_ms;
    if (aLat == null && bLat == null) return 0;
    if (aLat == null) return 1;
    if (bLat == null) return -1;
    return aLat - bLat;
  });

  const invalidateLeases = async () => {
    await queryClient.invalidateQueries({ queryKey: ["platform-leases", platform.id] });
    await queryClient.invalidateQueries({ queryKey: ["platform-monitor"] });
  };

  const refreshDetailNode = async (hash: string) => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["node", hash] }),
      queryClient.invalidateQueries({ queryKey: ["platform-leases", platform.id] }),
      queryClient.invalidateQueries({ queryKey: ["platform-nodes", platform.id] }),
    ]);
  };

  const deleteMutation = useMutation({
    mutationFn: (account: string) => deletePlatformLease(platform.id, account),
    onSuccess: async (_, account) => {
      await invalidateLeases();
      showToast("success", t("租约 {{account}} 已解绑", { account }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const bindMutation = useMutation({
    mutationFn: () => bindPlatformLease(platform.id, bindAccount.trim(), selectedNodeHash),
    onSuccess: async (lease) => {
      await invalidateLeases();
      setBindOpen(false);
      setBindAccount("");
      setSelectedNodeHash("");
      showToast("success", t("租约 {{account}} 已绑定到 {{ip}}", { account: lease.account, ip: lease.egress_ip }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const probeEgressMutation = useMutation({
    mutationFn: (hash: string) => probeEgress(hash),
    onSuccess: async (result, hash) => {
      await refreshDetailNode(hash);
      showToast(
        "success",
        t("出口探测完成：出口 IP={{ip}}，区域={{region}}，延迟={{latency}}", {
          ip: result.egress_ip || "-",
          region: result.region || "-",
          latency: formatLatency(result.latency_ewma_ms),
        }),
      );
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const probeLatencyMutation = useMutation({
    mutationFn: (hash: string) => probeLatency(hash),
    onSuccess: async (result, hash) => {
      await refreshDetailNode(hash);
      showToast("success", t("延迟探测完成：延迟={{latency}}", { latency: formatLatency(result.latency_ewma_ms) }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const handleDelete = (account: string) => {
    const confirmed = window.confirm(t("确认解绑租约 {{account}}？", { account }));
    if (confirmed) {
      deleteMutation.mutate(account);
    }
  };

  const handleBind = (e: React.FormEvent) => {
    e.preventDefault();
    if (!bindAccount.trim() || !selectedNodeHash) return;
    bindMutation.mutate();
  };

  const changePageSize = (size: number) => {
    setPageSize(size);
    setPage(0);
  };

  const toggleSort = (field: SortField) => {
    if (sortBy === field) {
      setSortOrder((prev) => (prev === "asc" ? "desc" : "asc"));
    } else {
      setSortBy(field);
      setSortOrder("asc");
    }
    setPage(0);
  };

  const sortHeader = (label: string, field: SortField): ReactNode => {
    const active = sortBy === field;
    const Icon = active ? (sortOrder === "asc" ? ArrowUp : ArrowDown) : ArrowUpDown;
    return (
      <span className={`lease-sort-header${active ? " active" : ""}`} onClick={() => toggleSort(field)}>
        {label}
        <Icon size={12} />
      </span>
    );
  };

  const leaseColumns = [
    columnHelper.accessor("account", {
      header: () => sortHeader(t("Account"), "account"),
      cell: (info) => <span className="lease-account-cell">{info.getValue()}</span>,
    }),
    columnHelper.accessor("node_tag", {
      header: () => sortHeader(t("节点"), "node_tag"),
      cell: (info) => info.getValue() || "-",
    }),
    columnHelper.accessor("egress_ip", {
      header: () => sortHeader(t("出口 IP"), "egress_ip"),
    }),
    columnHelper.display({
      id: "reference_latency_ms",
      header: t("参考延迟"),
      cell: (info) => {
        const latencyMs = info.row.original.reference_latency_ms;
        if (typeof latencyMs !== "number") {
          return "-";
        }
        return (
          <span style={{ color: referenceLatencyColor(latencyMs), fontWeight: 600 }}>
            {formatLatency(latencyMs)}
          </span>
        );
      },
    }),
    columnHelper.accessor("created_at", {
      header: () => sortHeader(t("绑定时间"), "created_at"),
      cell: (info) => formatRelativeTime(info.getValue()),
    }),
    columnHelper.accessor("expiry", {
      header: () => sortHeader(t("到期时间"), "expiry"),
      cell: (info) => formatRelativeTime(info.getValue()),
    }),
    columnHelper.accessor("last_accessed", {
      header: () => sortHeader(t("最近访问"), "last_accessed"),
      cell: (info) => formatRelativeTime(info.getValue()),
    }),
    columnHelper.display({
      id: "actions",
      header: "",
      cell: (info) => (
        <Button
          variant="danger"
          size="sm"
          onClick={(e) => {
            e.stopPropagation();
            handleDelete(info.row.original.account);
          }}
          disabled={deleteMutation.isPending}
          title={t("解绑")}
        >
          <Link2Off size={14} />
        </Button>
      ),
    }),
  ];

  return (
    <div className="platform-leases-panel">
      <div className="platform-leases-toolbar">
        <div className="platform-leases-search">
          <Search size={14} />
          <Input
            placeholder={t("搜索 Account...")}
            value={search}
            onChange={(e) => {
              setSearch(e.target.value);
              setPage(0);
            }}
          />
        </div>
        <Button variant="secondary" size="sm" onClick={() => setBindOpen(!bindOpen)}>
          <Plus size={14} />
          {t("绑定租约")}
        </Button>
      </div>

      {bindOpen ? (
        <form className="platform-leases-bind-form" onSubmit={handleBind}>
          <div className="bind-field bind-field-account">
            <Input
              placeholder={t("Account")}
              value={bindAccount}
              onChange={(e) => setBindAccount(e.target.value)}
              required
            />
          </div>
          <div className="bind-field bind-field-node">
            <Select
              value={selectedNodeHash}
              onChange={(e) => setSelectedNodeHash(e.target.value)}
              required
              disabled={nodesQuery.isLoading}
            >
              <option value="">
                {nodesQuery.isLoading ? t("加载节点中...") : t("出口 IP（如 1.2.3.4）")}
              </option>
              {sortedNodes.map((nd) => {
                const tag = nd.tags.map((tg) => tg.tag).join(", ") || nd.node_hash.slice(0, 8);
                const latency = nd.reference_latency_ms != null ? `${nd.reference_latency_ms}ms` : "-";
                return (
                  <option key={nd.node_hash} value={nd.node_hash}>
                    {tag} | {nd.egress_ip || "-"} | {latency}
                  </option>
                );
              })}
            </Select>
          </div>
          <div className="bind-actions">
            <Button type="submit" size="sm" disabled={bindMutation.isPending || !bindAccount.trim() || !selectedNodeHash}>
              {bindMutation.isPending ? t("绑定中...") : t("确认绑定")}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              type="button"
              onClick={() => {
                setBindOpen(false);
                setBindAccount("");
                setSelectedNodeHash("");
              }}
            >
              {t("取消")}
            </Button>
          </div>
        </form>
      ) : null}

      {leasesQuery.isLoading ? <p className="muted">{t("正在加载租约数据...")}</p> : null}

      {leasesQuery.isError ? (
        <div className="callout callout-error">
          <AlertTriangle size={14} />
          <span>{formatApiErrorMessage(leasesQuery.error, t)}</span>
        </div>
      ) : null}

      {!leasesQuery.isLoading && !leases.length ? (
        <div className="empty-box">
          <Sparkles size={16} />
          <p>{t("没有租约")}</p>
        </div>
      ) : null}

      {leases.length ? (
        <DataTable
          data={leases}
          columns={leaseColumns}
          getRowId={(l) => l.account}
          onRowClick={(lease) => setDetailNodeHash(lease.node_hash)}
        />
      ) : null}

      <OffsetPagination
        page={page}
        totalPages={totalPages}
        totalItems={leasesPage.total}
        pageSize={pageSize}
        pageSizeOptions={PAGE_SIZE_OPTIONS}
        onPageChange={setPage}
        onPageSizeChange={changePageSize}
      />

      {nodeDetailQuery.data ? (
        <NodeDetailDrawer
          node={nodeDetailQuery.data}
          onClose={() => setDetailNodeHash("")}
          onProbeEgress={(hash) => probeEgressMutation.mutate(hash)}
          onProbeLatency={(hash) => probeLatencyMutation.mutate(hash)}
          isEgressProbePending={(hash) => probeEgressMutation.isPending && probeEgressMutation.variables === hash}
          isLatencyProbePending={(hash) => probeLatencyMutation.isPending && probeLatencyMutation.variables === hash}
        />
      ) : null}
    </div>
  );
}
