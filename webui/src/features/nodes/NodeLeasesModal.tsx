import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createColumnHelper } from "@tanstack/react-table";
import { AlertTriangle, Globe, Link2Off, Sparkles, Tag, X, Zap } from "lucide-react";
import { useEffect } from "react";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { DataTable } from "../../components/ui/DataTable";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatRelativeTime } from "../../lib/time";
import { deletePlatformLease } from "../platforms/api";
import { listNodeLeases } from "./api";
import {
  displayableReferenceLatencyMs,
  firstTag,
  formatLatency,
  referenceLatencyColor,
  regionToFlag,
} from "./nodeFormat";
import type { NodeLease, NodeSummary } from "./types";

const columnHelper = createColumnHelper<NodeLease>();

type Props = {
  node: NodeSummary;
  platformId?: string;
  onClose: () => void;
  showToast: (type: "success" | "error", message: string) => void;
};

export function NodeLeasesModal({ node, platformId, onClose, showToast }: Props) {
  const { t } = useI18n();
  const queryClient = useQueryClient();

  const queryKey = ["node-leases", node.node_hash, platformId ?? ""];

  const leasesQuery = useQuery({
    queryKey,
    queryFn: () => listNodeLeases(node.node_hash, platformId),
    refetchInterval: 15_000,
    placeholderData: (prev) => prev,
  });

  const leases = leasesQuery.data ?? [];

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        onClose();
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [onClose]);

  const invalidateRelatedQueries = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["node-leases", node.node_hash] }),
      queryClient.invalidateQueries({ queryKey: ["nodes"] }),
      queryClient.invalidateQueries({ queryKey: ["platform-leases"] }),
      queryClient.invalidateQueries({ queryKey: ["platform-monitor"] }),
    ]);
  };

  const unbindMutation = useMutation({
    mutationFn: (target: { platformId: string; account: string }) =>
      deletePlatformLease(target.platformId, target.account),
    onSuccess: async (_, target) => {
      await invalidateRelatedQueries();
      showToast("success", t("租约 {{account}} 已解绑", { account: target.account }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const handleUnbind = (lease: NodeLease) => {
    const confirmed = window.confirm(
      t("确认解绑租约 {{account}}（平台 {{platform}}）？", {
        account: lease.account,
        platform: lease.platform_name || lease.platform_id,
      }),
    );
    if (!confirmed) {
      return;
    }
    unbindMutation.mutate({ platformId: lease.platform_id, account: lease.account });
  };

  const columns = [
    columnHelper.accessor("platform_name", {
      header: () => t("平台"),
      cell: (info) => info.getValue() || info.row.original.platform_id || "-",
    }),
    columnHelper.accessor("account", {
      header: () => t("账号"),
      cell: (info) => <span className="lease-account-cell">{info.getValue()}</span>,
    }),
    columnHelper.accessor("created_at", {
      header: () => t("绑定时间"),
      cell: (info) => formatRelativeTime(info.getValue()),
    }),
    columnHelper.accessor("expiry", {
      header: () => t("到期时间"),
      cell: (info) => formatRelativeTime(info.getValue()),
    }),
    columnHelper.accessor("last_accessed", {
      header: () => t("最近访问"),
      cell: (info) => formatRelativeTime(info.getValue()),
    }),
    columnHelper.display({
      id: "actions",
      header: () => t("操作"),
      cell: (info) => (
        <Button
          variant="danger"
          size="sm"
          onClick={(e) => {
            e.stopPropagation();
            handleUnbind(info.row.original);
          }}
          disabled={unbindMutation.isPending}
          title={t("解绑")}
        >
          <Link2Off size={14} />
        </Button>
      ),
    }),
  ];

  const titleTag = firstTag(node);
  const scopeHint = platformId
    ? t("仅展示当前筛选平台下的租约")
    : t("展示该节点在所有平台上的租约");
  const sourceLabel = titleTag;
  const egressLabel = node.egress_ip
    ? node.region
      ? `${node.egress_ip}  ${regionToFlag(node.region)}`
      : node.egress_ip
    : "-";
  const latencyMs = displayableReferenceLatencyMs(node);

  return (
    <div
      className="modal-overlay"
      role="dialog"
      aria-modal="true"
      aria-label={t("节点 {{name}} 的租约", { name: titleTag })}
      onClick={onClose}
    >
      <Card className="node-leases-modal-card" onClick={(event) => event.stopPropagation()}>
        <div className="modal-header">
          <div className="node-leases-modal-titles">
            <h3>{t("节点 {{name}} 的租约", { name: titleTag })}</h3>
            <p className="node-leases-modal-subtitle">{scopeHint}</p>
          </div>
          <Button variant="ghost" size="sm" aria-label={t("关闭")} onClick={onClose}>
            <X size={16} />
          </Button>
        </div>

        <div className="node-leases-modal-meta">
          <span className="node-leases-modal-meta-item" title={t("节点来源")}>
            <Tag size={13} />
            <span>{sourceLabel}</span>
          </span>
          <span className="node-leases-modal-meta-item" title={t("出口 IP")}>
            <Globe size={13} />
            <span>{egressLabel}</span>
          </span>
          <span className="node-leases-modal-meta-item" title={t("参考延迟")}>
            <Zap size={13} />
            <span
              className="node-leases-modal-meta-latency"
              style={latencyMs !== null ? { color: referenceLatencyColor(latencyMs) } : undefined}
            >
              {latencyMs !== null ? formatLatency(latencyMs) : "-"}
            </span>
          </span>
        </div>

        {leasesQuery.data && leases.length ? (
          <p className="node-leases-modal-summary">{t("共 {{count}} 条", { count: leases.length })}</p>
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
            columns={columns}
            getRowId={(l) => `${l.platform_id}:${l.account}`}
            wrapClassName="node-leases-table-wrap"
          />
        ) : null}
      </Card>
    </div>
  );
}
