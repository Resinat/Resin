import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createColumnHelper } from "@tanstack/react-table";
import { AlertTriangle, ArrowDown, ArrowUp, ArrowUpDown, Copy, Link2Off, Plus, Search, Sparkles, X } from "lucide-react";
import { type ReactNode, useState } from "react";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { DataTable } from "../../components/ui/DataTable";
import { Input } from "../../components/ui/Input";
import { OffsetPagination } from "../../components/ui/OffsetPagination";
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

type SortField = "account" | "node_tag" | "egress_ip" | "reference_latency_ms" | "created_at" | "expiry" | "last_accessed";
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

  const bindLeasesQuery = useQuery({
    queryKey: ["platform-leases", platform.id, "bind-map"],
    queryFn: () =>
      listPlatformLeases(platform.id, {
        limit: 100000,
        offset: 0,
        sort_by: "account",
        sort_order: "asc",
      }),
    enabled: bindOpen,
    placeholderData: (prev) => prev,
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

  const leaseAccountsByNode = new Map<string, string[]>();
  for (const lease of bindLeasesQuery.data?.items ?? []) {
    const accounts = leaseAccountsByNode.get(lease.node_hash) ?? [];
    accounts.push(lease.account);
    leaseAccountsByNode.set(lease.node_hash, accounts);
  }

  const openBindModal = (lease?: LeaseResponse) => {
    setBindAccount(lease?.account ?? "");
    setSelectedNodeHash(lease?.node_hash ?? "");
    setBindOpen(true);
  };

  const closeBindModal = () => {
    setBindOpen(false);
    setBindAccount("");
    setSelectedNodeHash("");
  };

  const copyLeaseName = async () => {
    const account = bindAccount.trim();
    if (!account) return;
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(account);
      } else {
        const area = document.createElement("textarea");
        area.value = account;
        area.style.position = "fixed";
        area.style.opacity = "0";
        document.body.appendChild(area);
        area.select();
        document.execCommand("copy");
        document.body.removeChild(area);
      }
      showToast("success", t("租约名称已复制"));
    } catch {
      showToast("error", t("复制失败"));
    }
  };

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
      closeBindModal();
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
      cell: (info) => (
        <button
          type="button"
          className="lease-account-button"
          title={t("绑定租约 {{account}}", { account: info.getValue() })}
          onClick={(event) => {
            event.stopPropagation();
            openBindModal(info.row.original);
          }}
        >
          {info.getValue()}
        </button>
      ),
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
      header: () => sortHeader(t("参考延迟"), "reference_latency_ms"),
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
        <Button variant="secondary" size="sm" onClick={() => openBindModal()}>
          <Plus size={14} />
          {t("绑定租约")}
        </Button>
      </div>

      {bindOpen ? (
        <div className="modal-overlay" role="dialog" aria-modal="true" aria-label={t("绑定租约")} onClick={closeBindModal}>
          <Card className="modal-card platform-lease-bind-modal" onClick={(event) => event.stopPropagation()}>
            <div className="modal-header">
              <h3>{t("绑定租约")}</h3>
              <Button variant="ghost" size="sm" aria-label={t("关闭")} onClick={closeBindModal}>
                <X size={16} />
              </Button>
            </div>

            <form className="platform-lease-bind-form" onSubmit={handleBind}>
              <div className="field-group">
                <label className="field-label" htmlFor="bind-lease-account">
                  {t("租约名称")}
                </label>
                <Input
                  id="bind-lease-account"
                  placeholder={t("Account")}
                  value={bindAccount}
                  onChange={(e) => setBindAccount(e.target.value)}
                  required
                />
              </div>

              {bindAccount.trim() ? (
                <button type="button" className="lease-copy-chip" title={t("点击复制租约名称")} onClick={copyLeaseName}>
                  <span>{bindAccount.trim()}</span>
                  <Copy size={13} />
                </button>
              ) : null}

              <div className="platform-lease-node-picker">
                <div className="platform-lease-node-picker-head">
                  <span>{t("选择节点")}</span>
                  <span>{nodesQuery.isLoading ? t("加载节点中...") : t("共 {{count}} 个节点", { count: sortedNodes.length })}</span>
                </div>

                {nodesQuery.isError ? (
                  <div className="callout callout-error">
                    <AlertTriangle size={14} />
                    <span>{formatApiErrorMessage(nodesQuery.error, t)}</span>
                  </div>
                ) : null}

                {bindLeasesQuery.isError ? (
                  <div className="callout callout-error">
                    <AlertTriangle size={14} />
                    <span>{formatApiErrorMessage(bindLeasesQuery.error, t)}</span>
                  </div>
                ) : null}

                {nodesQuery.isLoading ? <p className="muted">{t("加载节点中...")}</p> : null}

                {!nodesQuery.isLoading && !sortedNodes.length ? (
                  <div className="empty-box">
                    <Sparkles size={16} />
                    <p>{t("没有匹配的节点")}</p>
                  </div>
                ) : null}

                {sortedNodes.length ? (
                  <div className="platform-lease-node-table-wrap">
                    <table className="platform-lease-node-table">
                      <thead>
                        <tr>
                          <th>{t("节点")}</th>
                          <th>{t("出口 IP")}</th>
                          <th>{t("参考延迟")}</th>
                          <th>{t("已绑定")}</th>
                          <th>{t("操作")}</th>
                        </tr>
                      </thead>
                      <tbody>
                        {sortedNodes.map((nd) => {
                          const tag = nd.tags.map((tg) => tg.tag).join(", ") || nd.display_tag || nd.node_hash.slice(0, 8);
                          const latencyMs = nd.reference_latency_ms;
                          const accounts = leaseAccountsByNode.get(nd.node_hash) ?? [];
                          const hiddenAccountCount = Math.max(0, accounts.length - 2);
                          const accountText = accounts.slice(0, 2).join(", ");
                          const boundTitle = accounts.join(", ");
                          const isOccupied = accounts.length > 0 || nd.lease_count > 0;
                          const selected = selectedNodeHash === nd.node_hash;
                          return (
                            <tr
                              key={nd.node_hash}
                              className={`${selected ? "selected" : ""}${isOccupied ? " occupied" : ""}`}
                            >
                              <td className="platform-lease-node-name" title={tag}>
                                {tag}
                              </td>
                              <td className="platform-lease-node-ip" title={nd.egress_ip || "-"}>
                                {nd.egress_ip || "-"}
                              </td>
                              <td>
                                <span
                                  className="platform-lease-node-latency"
                                  style={typeof latencyMs === "number" ? { color: referenceLatencyColor(latencyMs) } : undefined}
                                >
                                  {typeof latencyMs === "number" ? formatLatency(latencyMs) : "-"}
                                </span>
                              </td>
                              <td className="platform-lease-node-bound" title={boundTitle}>
                                {accounts.length ? (
                                  <>
                                    <span className="platform-lease-bound-label">{t("已绑定:")}</span>
                                    <span className="platform-lease-bound-accounts">
                                      {accountText}
                                      {hiddenAccountCount ? ` ${t("+{{count}}", { count: hiddenAccountCount })}` : ""}
                                    </span>
                                  </>
                                ) : isOccupied && bindLeasesQuery.isLoading ? (
                                  <span className="muted">{t("加载中...")}</span>
                                ) : (
                                  "-"
                                )}
                              </td>
                              <td className="platform-lease-node-action">
                                <Button
                                  variant={selected ? "primary" : "secondary"}
                                  size="sm"
                                  onClick={() => setSelectedNodeHash(nd.node_hash)}
                                >
                                  {selected ? t("已选择") : t("选择")}
                                </Button>
                              </td>
                            </tr>
                          );
                        })}
                      </tbody>
                    </table>
                  </div>
                ) : null}
              </div>

              <div className="bind-actions">
                <Button type="submit" size="sm" disabled={bindMutation.isPending || !bindAccount.trim() || !selectedNodeHash}>
                  {bindMutation.isPending ? t("绑定中...") : t("确认绑定")}
                </Button>
                <Button variant="secondary" size="sm" type="button" onClick={closeBindModal}>
                  {t("取消")}
                </Button>
              </div>
            </form>
          </Card>
        </div>
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
