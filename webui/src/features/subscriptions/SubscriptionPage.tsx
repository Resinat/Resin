import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createColumnHelper } from "@tanstack/react-table";
import { AlertTriangle, Eye, Filter, Info, Pencil, Plus, RefreshCw, Search, Sparkles, Trash2, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useForm } from "react-hook-form";
import { Link } from "react-router-dom";
import { z } from "zod";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { DataTable } from "../../components/ui/DataTable";
import { Input } from "../../components/ui/Input";
import { OffsetPagination } from "../../components/ui/OffsetPagination";
import { Select } from "../../components/ui/Select";
import { Switch } from "../../components/ui/Switch";
import { Textarea } from "../../components/ui/Textarea";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatDateTime, formatGoDuration, formatRelativeTime } from "../../lib/time";
import {
  cleanupSubscriptionCircuitOpenNodes,
  createSubscription,
  deleteSubscription,
  listSubscriptions,
  refreshSubscription,
  updateSubscription,
} from "./api";
import type { Subscription } from "./types";

type EnabledFilter = "all" | "enabled" | "disabled";
type SubscriptionSourceType = "remote" | "local";

const SUBSCRIPTION_SOURCE_TABS: Array<{ key: SubscriptionSourceType; label: string; hint: string }> = [
  { key: "remote", label: "远程", hint: "从 HTTP/HTTPS 订阅链接拉取内容" },
  { key: "local", label: "本地", hint: "直接填写订阅文本，不经过网络拉取" },
];

const subscriptionCreateSchema = z.object({
  name: z.string().trim().min(1, "订阅名称不能为空"),
  source_type: z.enum(["remote", "local"]),
  url: z.string(),
  content: z.string(),
  update_interval: z.string().trim().min(1, "更新间隔不能为空"),
  ephemeral_node_evict_delay: z.string().trim().min(1, "临时节点驱逐延迟不能为空"),
  enabled: z.boolean(),
  ephemeral: z.boolean(),
}).superRefine((value, ctx) => {
  const url = value.url.trim();
  const content = value.content.trim();
  if (value.source_type === "remote") {
    if (!url) {
      ctx.addIssue({ code: z.ZodIssueCode.custom, path: ["url"], message: "URL 不能为空" });
      return;
    }
    if (!(url.startsWith("http://") || url.startsWith("https://"))) {
      ctx.addIssue({ code: z.ZodIssueCode.custom, path: ["url"], message: "URL 必须是 http/https 地址" });
    }
    return;
  }
  if (!content) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, path: ["content"], message: "订阅内容不能为空" });
  }
});

const subscriptionEditSchema = subscriptionCreateSchema;

type SubscriptionCreateForm = z.infer<typeof subscriptionCreateSchema>;
type SubscriptionEditForm = z.infer<typeof subscriptionEditSchema>;
const EMPTY_SUBSCRIPTIONS: Subscription[] = [];
const PAGE_SIZE_OPTIONS = [10, 20, 50, 100] as const;
const LOCAL_SOURCE_UPDATE_INTERVAL = "12h";
const SUBSCRIPTION_DISABLE_HINT = "禁用订阅后，节点不会进入各平台的路由池，但不会从全局节点池中删除。";

function extractHostname(url: string): string {
  try {
    return new URL(url).hostname;
  } catch {
    return url;
  }
}

function subscriptionToEditForm(subscription: Subscription): SubscriptionEditForm {
  return {
    name: subscription.name,
    source_type: subscription.source_type,
    url: subscription.url,
    content: subscription.content ?? "",
    update_interval: subscription.update_interval,
    ephemeral_node_evict_delay: subscription.ephemeral_node_evict_delay,
    enabled: subscription.enabled,
    ephemeral: subscription.ephemeral,
  };
}

function sourceTypeLabel(sourceType: SubscriptionSourceType): string {
  return sourceType === "local" ? "本地" : "远程";
}

function parseEnabledFilter(value: EnabledFilter): boolean | undefined {
  if (value === "enabled") {
    return true;
  }
  if (value === "disabled") {
    return false;
  }
  return undefined;
}

function normalizeSubmitUpdateInterval(sourceType: SubscriptionSourceType, raw: string): string {
  if (sourceType === "local") {
    return LOCAL_SOURCE_UPDATE_INTERVAL;
  }
  return raw.trim();
}

export function SubscriptionPage() {
  const { t } = useI18n();
  const [enabledFilter, setEnabledFilter] = useState<EnabledFilter>("all");
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState<number>(20);
  const [selectedSubscriptionId, setSelectedSubscriptionId] = useState("");
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [createModalOpen, setCreateModalOpen] = useState(false);
  const { toasts, showToast, dismissToast } = useToast();

  const queryClient = useQueryClient();
  const enabledValue = parseEnabledFilter(enabledFilter);

  const subscriptionsQuery = useQuery({
    queryKey: ["subscriptions", enabledFilter, page, pageSize, search],
    queryFn: () =>
      listSubscriptions({
        enabled: enabledValue,
        limit: pageSize,
        offset: page * pageSize,
        keyword: search,
      }),
    refetchInterval: 30_000,
    placeholderData: (prev) => prev,
  });

  const subscriptions = subscriptionsQuery.data?.items ?? EMPTY_SUBSCRIPTIONS;
  const totalSubscriptions = subscriptionsQuery.data?.total ?? 0;

  const totalPages = Math.max(1, Math.ceil(totalSubscriptions / pageSize));
  const currentPage = Math.min(page, totalPages - 1);

  const selectedSubscription = useMemo(() => {
    if (!selectedSubscriptionId) {
      return null;
    }
    return subscriptions.find((item) => item.id === selectedSubscriptionId) ?? null;
  }, [selectedSubscriptionId, subscriptions]);

  const drawerVisible = drawerOpen && Boolean(selectedSubscription);

  const createForm = useForm<SubscriptionCreateForm>({
    resolver: zodResolver(subscriptionCreateSchema),
    defaultValues: {
      name: "",
      source_type: "remote",
      url: "",
      content: "",
      update_interval: "12h",
      ephemeral_node_evict_delay: "72h",
      enabled: true,
      ephemeral: false,
    },
  });

  const createEphemeral = createForm.watch("ephemeral");
  const createSourceType = createForm.watch("source_type");

  const editForm = useForm<SubscriptionEditForm>({
    resolver: zodResolver(subscriptionEditSchema),
    defaultValues: {
      name: "",
      source_type: "remote",
      url: "",
      content: "",
      update_interval: "12h",
      ephemeral_node_evict_delay: "72h",
      enabled: true,
      ephemeral: false,
    },
  });

  const editEphemeral = editForm.watch("ephemeral");
  const editSourceType = editForm.watch("source_type");

  useEffect(() => {
    if (!selectedSubscription) {
      return;
    }
    editForm.reset(subscriptionToEditForm(selectedSubscription));
  }, [selectedSubscription, editForm]);

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

  const invalidateSubscriptions = async () => {
    await queryClient.invalidateQueries({ queryKey: ["subscriptions"] });
  };

  const invalidateSubscriptionsAndNodes = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["subscriptions"] }),
      queryClient.invalidateQueries({ queryKey: ["nodes"] }),
    ]);
  };

  const createMutation = useMutation({
    mutationFn: createSubscription,
    onSuccess: async (created) => {
      await invalidateSubscriptions();
      setCreateModalOpen(false);
      createForm.reset({
        name: "",
        source_type: "remote",
        url: "",
        content: "",
        update_interval: LOCAL_SOURCE_UPDATE_INTERVAL,
        ephemeral_node_evict_delay: "72h",
        enabled: true,
        ephemeral: false,
      });
      showToast("success", t("订阅 {{name}} 创建成功", { name: created.name }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const updateMutation = useMutation({
    mutationFn: async (formData: SubscriptionEditForm) => {
      if (!selectedSubscription) {
        throw new Error("请选择要编辑的订阅");
      }

      const payload = {
        name: formData.name.trim(),
        update_interval: normalizeSubmitUpdateInterval(formData.source_type, formData.update_interval),
        ephemeral_node_evict_delay: formData.ephemeral_node_evict_delay.trim(),
        enabled: formData.enabled,
        ephemeral: formData.ephemeral,
        ...(formData.source_type === "remote"
          ? { url: formData.url.trim() }
          : { content: formData.content }),
      };
      return updateSubscription(selectedSubscription.id, payload);
    },
    onSuccess: async (updated) => {
      await invalidateSubscriptions();
      setSelectedSubscriptionId(updated.id);
      showToast("success", t("订阅 {{name}} 已更新", { name: updated.name }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async (subscription: Subscription) => {
      await deleteSubscription(subscription.id);
      return subscription;
    },
    onSuccess: async (deleted) => {
      await invalidateSubscriptions();
      if (selectedSubscriptionId === deleted.id) {
        setSelectedSubscriptionId("");
        setDrawerOpen(false);
      }
      showToast("success", t("订阅 {{name}} 已删除", { name: deleted.name }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });
  const deleteSubscriptionMutateAsync = deleteMutation.mutateAsync;
  const isDeletePending = deleteMutation.isPending;

  const refreshMutation = useMutation({
    mutationFn: async (subscription: Subscription) => {
      await refreshSubscription(subscription.id);
      return subscription;
    },
    onSuccess: async (subscription) => {
      await invalidateSubscriptions();
      showToast("success", t("订阅 {{name}} 已手动刷新", { name: subscription.name }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });
  const refreshSubscriptionMutateAsync = refreshMutation.mutateAsync;
  const isRefreshPending = refreshMutation.isPending;

  const cleanupCircuitOpenNodesMutation = useMutation({
    mutationFn: async (subscription: Subscription) => {
      const cleanedCount = await cleanupSubscriptionCircuitOpenNodes(subscription.id);
      return { subscription, cleanedCount };
    },
    onSuccess: async ({ subscription, cleanedCount }) => {
      await invalidateSubscriptionsAndNodes();
      if (cleanedCount > 0) {
        showToast("success", t("订阅 {{name}} 已清理 {{count}} 个节点", { name: subscription.name, count: cleanedCount }));
        return;
      }
      showToast("success", t("订阅 {{name}} 没有可清理的熔断或异常节点", { name: subscription.name }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const onCreateSubmit = createForm.handleSubmit(async (values) => {
    const payload = {
      name: values.name.trim(),
      source_type: values.source_type,
      update_interval: normalizeSubmitUpdateInterval(values.source_type, values.update_interval),
      ephemeral_node_evict_delay: values.ephemeral_node_evict_delay.trim(),
      enabled: values.enabled,
      ephemeral: values.ephemeral,
      ...(values.source_type === "remote"
        ? { url: values.url.trim() }
        : { content: values.content }),
    };
    await createMutation.mutateAsync(payload);
  });

  const onEditSubmit = editForm.handleSubmit(async (values) => {
    await updateMutation.mutateAsync(values);
  });

  const handleDelete = useCallback(async (subscription: Subscription) => {
    const confirmed = window.confirm(t("确认删除订阅 {{name}}？关联节点会被清理。", { name: subscription.name }));
    if (!confirmed) {
      return;
    }
    await deleteSubscriptionMutateAsync(subscription);
  }, [deleteSubscriptionMutateAsync, t]);

  const handleCleanupCircuitOpenNodes = async (subscription: Subscription) => {
    const confirmed = window.confirm(t("确认立即清理订阅 {{name}} 中的熔断或异常节点？", { name: subscription.name }));
    if (!confirmed) {
      return;
    }
    await cleanupCircuitOpenNodesMutation.mutateAsync(subscription);
  };

  const openDrawer = useCallback((subscription: Subscription) => {
    setSelectedSubscriptionId(subscription.id);
    setDrawerOpen(true);
  }, []);

  const handleRefresh = useCallback(async (subscription: Subscription) => {
    await refreshSubscriptionMutateAsync(subscription);
  }, [refreshSubscriptionMutateAsync]);

  const changePageSize = (next: number) => {
    setPageSize(next);
    setPage(0);
  };

  const col = useMemo(() => createColumnHelper<Subscription>(), []);

  const subColumns = useMemo(
    () => [
      col.accessor("name", {
        header: t("名称"),
        cell: (info) => <p className="subscriptions-name-cell">{info.getValue()}</p>,
      }),
      col.accessor("url", {
        header: t("订阅源"),
        cell: (info) => {
          const s = info.row.original;
          if (s.source_type === "local") {
            return (
              <p className="subscriptions-url-cell" title={t("本地订阅文本")}>
                {t("本地文本")}
              </p>
            );
          }
          return (
            <p className="subscriptions-url-cell" title={info.getValue()}>
              {extractHostname(info.getValue())}
            </p>
          );
        },
      }),
      col.accessor("update_interval", {
        header: t("更新间隔"),
        cell: (info) => formatGoDuration(info.getValue()),
      }),
      col.display({
        id: "node_count",
        header: t("节点数"),
        cell: (info) => {
          const s = info.row.original;
          return `${s.healthy_node_count} / ${s.node_count}`;
        },
      }),
      col.display({
        id: "status",
        header: t("状态"),
        cell: (info) => {
          const s = info.row.original;
          return (
            <div className="subscriptions-status-cell">
              {!s.enabled ? (
                <Badge variant="warning">{t("已禁用")}</Badge>
              ) : s.last_error ? (
                <Badge variant="danger">{t("错误")}</Badge>
              ) : (
                <Badge variant="success">{t("正常")}</Badge>
              )}
            </div>
          );
        },
      }),
      col.accessor("last_checked", {
        header: t("上次检查"),
        cell: (info) => formatRelativeTime(info.getValue() || ""),
      }),
      col.accessor("last_updated", {
        header: t("上次更新"),
        cell: (info) => formatRelativeTime(info.getValue() || ""),
      }),
      col.display({
        id: "actions",
        header: t("操作"),
        cell: (info) => {
          const s = info.row.original;
          return (
            <div className="subscriptions-row-actions" onClick={(event) => event.stopPropagation()}>
              <Link
                className="btn btn-ghost btn-sm"
                to={`/nodes?subscription_id=${encodeURIComponent(s.id)}`}
                title={t("预览节点池")}
                aria-label={t("预览订阅 {{name}} 的节点池", { name: s.name })}
              >
                <Eye size={14} />
              </Link>
              <Button size="sm" variant="ghost" onClick={() => openDrawer(s)} title={t("编辑")}>
                <Pencil size={14} />
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => void handleRefresh(s)}
                disabled={isRefreshPending}
                title={t("刷新")}
              >
                <RefreshCw size={14} />
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => void handleDelete(s)}
                disabled={isDeletePending}
                title={t("删除")}
                style={{ color: "var(--delete-btn-color, #c27070)" }}
              >
                <Trash2 size={14} />
              </Button>
            </div>
          );
        },
      }),
    ],
    [col, handleDelete, handleRefresh, isDeletePending, isRefreshPending, openDrawer, t]
  );

  return (
    <section className="platform-page">
      <header className="module-header">
        <div>
          <h2>{t("订阅管理")}</h2>
          <p className="module-description">{t("保障订阅按计划更新，异常时可一键刷新。")}</p>
        </div>
      </header>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <Card className="platform-list-card platform-directory-card">
        <div className="list-card-header">
          <div>
            <h3>{t("订阅列表")}</h3>
            <p>{t("共 {{count}} 个订阅", { count: totalSubscriptions })}</p>
          </div>
          <div style={{ display: "flex", gap: "0.5rem", alignItems: "center" }}>
            <label className="subscription-inline-filter" htmlFor="sub-status-filter" style={{ flexDirection: "row", alignItems: "center", gap: 6 }}>
              <Filter size={16} />
              <Select
                id="sub-status-filter"
                value={enabledFilter}
                onChange={(event) => {
                  setEnabledFilter(event.target.value as EnabledFilter);
                  setPage(0);
                }}
              >
                <option value="all">{t("全部")}</option>
                <option value="enabled">{t("仅启用")}</option>
                <option value="disabled">{t("仅禁用")}</option>
              </Select>
            </label>
            <label className="search-box" htmlFor="subscription-search" style={{ maxWidth: 200, margin: 0, gap: 6 }}>
              <Search size={16} />
              <Input
                id="subscription-search"
                placeholder={t("搜索订阅")}
                value={search}
                onChange={(event) => {
                  setSearch(event.target.value);
                  setPage(0);
                }}
                style={{ padding: "6px 10px", borderRadius: 8 }}
              />
            </label>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setCreateModalOpen(true)}
            >
              <Plus size={16} />
              {t("新建")}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => subscriptionsQuery.refetch()}
              disabled={subscriptionsQuery.isFetching}
            >
              <RefreshCw size={16} className={subscriptionsQuery.isFetching ? "spin" : undefined} />
              {t("刷新")}
            </Button>
          </div>
        </div>
      </Card>

      <Card className="platform-cards-container subscriptions-table-card">
        {subscriptionsQuery.isLoading ? <p className="muted">{t("正在加载订阅数据...")}</p> : null}

        {subscriptionsQuery.isError ? (
          <div className="callout callout-error">
            <AlertTriangle size={14} />
            <span>{formatApiErrorMessage(subscriptionsQuery.error, t)}</span>
          </div>
        ) : null}

        {!subscriptionsQuery.isLoading && !subscriptions.length ? (
          <div className="empty-box">
            <Sparkles size={16} />
            <p>{t("没有匹配的订阅")}</p>
          </div>
        ) : null}

        {subscriptions.length ? (
          <DataTable
            data={subscriptions}
            columns={subColumns}
            onRowClick={openDrawer}
            getRowId={(s) => s.id}
            className="data-table-subs"
          />
        ) : null}

        <OffsetPagination
          page={currentPage}
          totalPages={totalPages}
          totalItems={totalSubscriptions}
          pageSize={pageSize}
          pageSizeOptions={PAGE_SIZE_OPTIONS}
          onPageChange={setPage}
          onPageSizeChange={changePageSize}
        />
      </Card>

      {drawerVisible && selectedSubscription ? (
        <div
          className="drawer-overlay"
          role="dialog"
          aria-modal="true"
          aria-label={t("编辑订阅 {{name}}", { name: selectedSubscription.name })}
          onClick={() => setDrawerOpen(false)}
        >
          <Card className="drawer-panel" onClick={(event) => event.stopPropagation()}>
            <div className="drawer-header">
              <div>
                <h3>{selectedSubscription.name}</h3>
                <p>{selectedSubscription.id}</p>
              </div>
              <div className="drawer-header-actions">
                <Badge variant={selectedSubscription.enabled ? "success" : "warning"}>
                  {selectedSubscription.enabled ? t("运行中") : t("已停用")}
                </Badge>
                <Button
                  variant="ghost"
                  size="sm"
                  aria-label={t("关闭编辑面板")}
                  onClick={() => setDrawerOpen(false)}
                >
                  <X size={16} />
                </Button>
              </div>
            </div>
            <div className="platform-drawer-layout">
              <section className="platform-drawer-section">
                <div className="platform-drawer-section-head">
                  <h4>{t("订阅配置")}</h4>
                  <p>
                    {editSourceType === "local"
                      ? t("更新本地订阅文本、刷新周期与状态开关后点击保存。")
                      : t("更新 URL、刷新周期与状态开关后点击保存。")}
                  </p>
                </div>

                <div className="stats-grid">
                  <div>
                    <span>{t("创建时间")}</span>
                    <p>{formatDateTime(selectedSubscription.created_at)}</p>
                  </div>
                  <div>
                    <span>{t("上次检查")}</span>
                    <p>{formatDateTime(selectedSubscription.last_checked || "")}</p>
                  </div>
                  <div>
                    <span>{t("上次更新")}</span>
                    <p>{formatDateTime(selectedSubscription.last_updated || "")}</p>
                  </div>
                </div>

                {selectedSubscription.last_error ? (
                  <div className="callout callout-error">{t("最近错误：{{message}}", { message: selectedSubscription.last_error })}</div>
                ) : (
                  <div className="callout callout-success">{t("最近一次刷新无错误")}</div>
                )}

                <form className="form-grid" onSubmit={onEditSubmit}>
                  <input type="hidden" {...editForm.register("source_type")} />

                  <div className="field-group">
                    <label className="field-label" htmlFor="edit-sub-name">
                      {t("订阅名称")}
                    </label>
                    <Input
                      id="edit-sub-name"
                      invalid={Boolean(editForm.formState.errors.name)}
                      {...editForm.register("name")}
                    />
                    {editForm.formState.errors.name?.message ? (
                      <p className="field-error">{t(editForm.formState.errors.name.message)}</p>
                    ) : null}
                  </div>

                  <div className="field-group">
                    <label className="field-label" htmlFor="edit-sub-source-type">
                      {t("订阅类型")}
                    </label>
                    <Input
                      id="edit-sub-source-type"
                      value={t(sourceTypeLabel(editSourceType))}
                      readOnly
                      disabled
                    />
                  </div>

                  {editSourceType === "remote" ? (
                    <>
                      <div className="field-group field-span-2">
                        <label className="field-label" htmlFor="edit-sub-interval">
                          {t("更新间隔")}
                        </label>
                        <Input
                          id="edit-sub-interval"
                          placeholder={t("例如 12h")}
                          invalid={Boolean(editForm.formState.errors.update_interval)}
                          {...editForm.register("update_interval")}
                        />
                        {editForm.formState.errors.update_interval?.message ? (
                          <p className="field-error">{t(editForm.formState.errors.update_interval.message)}</p>
                        ) : null}
                      </div>

                      <div className="field-group field-span-2">
                        <label className="field-label" htmlFor="edit-sub-url">
                          {t("订阅链接")}
                        </label>
                        <Input id="edit-sub-url" invalid={Boolean(editForm.formState.errors.url)} {...editForm.register("url")} />
                        {editForm.formState.errors.url?.message ? (
                          <p className="field-error">{t(editForm.formState.errors.url.message)}</p>
                        ) : null}
                      </div>
                    </>
                  ) : (
                    <div className="field-group field-span-2">
                      <label className="field-label" htmlFor="edit-sub-content">
                        {t("订阅内容")}
                      </label>
                      <Textarea
                        id="edit-sub-content"
                        rows={8}
                        placeholder={t("支持 sing-box JSON / Clash YAML / URI / IP:PORT 或 IP:PORT:USER:PASS 列表")}
                        invalid={Boolean(editForm.formState.errors.content)}
                        {...editForm.register("content")}
                      />
                      {editForm.formState.errors.content?.message ? (
                        <p className="field-error">{t(editForm.formState.errors.content.message)}</p>
                      ) : null}
                    </div>
                  )}

                  <div className="field-group">
                    <label className="field-label" htmlFor="edit-sub-ephemeral" style={{ visibility: "hidden" }}>
                      {t("临时订阅")}
                    </label>
                    <div className="subscription-switch-item">
                      <label className="subscription-switch-label" htmlFor="edit-sub-ephemeral">
                        {t("临时订阅")}
                      </label>
                      <Switch id="edit-sub-ephemeral" {...editForm.register("ephemeral")} />
                    </div>
                  </div>

                  <div className="field-group">
                    <label className="field-label" htmlFor="edit-sub-ephemeral-evict-delay">
                      {t("临时节点驱逐延迟")}
                    </label>
                    <Input
                      id="edit-sub-ephemeral-evict-delay"
                      placeholder={t("例如 72h")}
                      invalid={Boolean(editForm.formState.errors.ephemeral_node_evict_delay)}
                      disabled={!editEphemeral}
                      {...editForm.register("ephemeral_node_evict_delay")}
                    />
                    {editForm.formState.errors.ephemeral_node_evict_delay?.message ? (
                      <p className="field-error">{t(editForm.formState.errors.ephemeral_node_evict_delay.message)}</p>
                    ) : null}
                  </div>

                  <div className="subscription-switch-item">
                    <label className="subscription-switch-label" htmlFor="edit-sub-enabled">
                      <span>{t("启用")}</span>
                      <span
                        className="subscription-info-icon"
                        title={t(SUBSCRIPTION_DISABLE_HINT)}
                        aria-label={t(SUBSCRIPTION_DISABLE_HINT)}
                        tabIndex={0}
                      >
                        <Info size={13} />
                      </span>
                    </label>
                    <Switch id="edit-sub-enabled" {...editForm.register("enabled")} />
                  </div>

                  <div className="platform-config-actions">
                    <Button type="submit" disabled={updateMutation.isPending}>
                      {updateMutation.isPending ? t("保存中...") : t("保存配置")}
                    </Button>
                  </div>
                </form>
              </section>

              <section className="platform-drawer-section platform-ops-section">
                <div className="platform-drawer-section-head">
                  <h4>{t("运维操作")}</h4>
                </div>

                <div className="platform-ops-list">
                  <div className="platform-op-item">
                    <div className="platform-op-copy">
                      <h5>{t("手动刷新")}</h5>
                      <p className="platform-op-hint">{t("立即刷新订阅并同步节点。")}</p>
                    </div>
                    <Button
                      variant="secondary"
                      onClick={() => void refreshMutation.mutateAsync(selectedSubscription)}
                      disabled={refreshMutation.isPending}
                    >
                      {refreshMutation.isPending ? t("刷新中...") : t("立即刷新")}
                    </Button>
                  </div>

                  <div className="platform-op-item">
                    <div className="platform-op-copy">
                      <h5>{t("清理失效节点")}</h5>
                      <p className="platform-op-hint">{t("立即清理当前熔断，或出错错误的节点。（订阅更新后节点可能会被重新添加）")}</p>
                    </div>
                    <Button
                      variant="secondary"
                      onClick={() => void handleCleanupCircuitOpenNodes(selectedSubscription)}
                      disabled={cleanupCircuitOpenNodesMutation.isPending}
                    >
                      {cleanupCircuitOpenNodesMutation.isPending ? t("清理中...") : t("立即清理")}
                    </Button>
                  </div>

                  <div className="platform-op-item">
                    <div className="platform-op-copy">
                      <h5>{t("删除订阅")}</h5>
                      <p className="platform-op-hint">{t("删除订阅并清理关联节点，操作不可撤销。")}</p>
                    </div>
                    <Button
                      variant="danger"
                      onClick={() => void handleDelete(selectedSubscription)}
                      disabled={deleteMutation.isPending}
                    >
                      {deleteMutation.isPending ? t("删除中...") : t("删除订阅")}
                    </Button>
                  </div>
                </div>
              </section>
            </div>
          </Card>
        </div>
      ) : null}

      {createModalOpen ? (
        <div className="modal-overlay" role="dialog" aria-modal="true">
          <Card className="modal-card">
            <div className="modal-header">
              <h3>{t("新建订阅")}</h3>
              <Button variant="ghost" size="sm" onClick={() => setCreateModalOpen(false)}>
                <X size={16} />
              </Button>
            </div>

            <form className="form-grid" onSubmit={onCreateSubmit}>
              <input type="hidden" {...createForm.register("source_type")} />

              <div className="field-group field-span-2">
                <label className="field-label" htmlFor="create-sub-name">
                  {t("订阅名称")}
                </label>
                <Input
                  id="create-sub-name"
                  invalid={Boolean(createForm.formState.errors.name)}
                  {...createForm.register("name")}
                />
                {createForm.formState.errors.name?.message ? (
                  <p className="field-error">{t(createForm.formState.errors.name.message)}</p>
                ) : null}
              </div>

              <div className="field-group field-span-2">
                <label className="field-label">{t("订阅来源")}</label>
                <div className="platform-detail-tabs" role="tablist" aria-label={t("订阅来源类型")}>
                  {SUBSCRIPTION_SOURCE_TABS.map((tab) => {
                    const selected = createSourceType === tab.key;
                    return (
                      <button
                        key={tab.key}
                        type="button"
                        role="tab"
                        aria-selected={selected}
                        className={`platform-detail-tab ${selected ? "platform-detail-tab-active" : ""}`}
                        title={t(tab.hint)}
                        onClick={() => createForm.setValue("source_type", tab.key, { shouldDirty: true, shouldValidate: true })}
                      >
                        <span>{t(tab.label)}</span>
                      </button>
                    );
                  })}
                </div>
              </div>

              {createSourceType === "remote" ? (
                <>
                  <div className="field-group field-span-2">
                    <label className="field-label" htmlFor="create-sub-interval">
                      {t("更新间隔")}
                    </label>
                    <Input
                      id="create-sub-interval"
                      placeholder={t("例如 12h")}
                      invalid={Boolean(createForm.formState.errors.update_interval)}
                      {...createForm.register("update_interval")}
                    />
                    {createForm.formState.errors.update_interval?.message ? (
                      <p className="field-error">{t(createForm.formState.errors.update_interval.message)}</p>
                    ) : null}
                  </div>

                  <div className="field-group field-span-2">
                    <label className="field-label" htmlFor="create-sub-url">
                      {t("订阅链接")}
                    </label>
                    <Input
                      id="create-sub-url"
                      invalid={Boolean(createForm.formState.errors.url)}
                      {...createForm.register("url")}
                    />
                    {createForm.formState.errors.url?.message ? (
                      <p className="field-error">{t(createForm.formState.errors.url.message)}</p>
                    ) : null}
                  </div>
                </>
              ) : (
                <div className="field-group field-span-2">
                  <label className="field-label" htmlFor="create-sub-content">
                    {t("订阅内容")}
                  </label>
                  <Textarea
                    id="create-sub-content"
                    rows={8}
                    placeholder={t("支持 sing-box JSON / Clash YAML / URI / IP:PORT 或 IP:PORT:USER:PASS 列表")}
                    invalid={Boolean(createForm.formState.errors.content)}
                    {...createForm.register("content")}
                  />
                  {createForm.formState.errors.content?.message ? (
                    <p className="field-error">{t(createForm.formState.errors.content.message)}</p>
                  ) : null}
                </div>
              )}

              <div className="field-group">
                <label className="field-label" htmlFor="create-sub-ephemeral" style={{ visibility: "hidden" }}>
                  {t("临时订阅")}
                </label>
                <div className="subscription-switch-item">
                  <label className="subscription-switch-label" htmlFor="create-sub-ephemeral">
                    {t("临时订阅")}
                  </label>
                  <Switch id="create-sub-ephemeral" {...createForm.register("ephemeral")} />
                </div>
              </div>

              <div className="field-group">
                <label className="field-label" htmlFor="create-sub-ephemeral-evict-delay">
                  {t("临时节点驱逐延迟")}
                </label>
                <Input
                  id="create-sub-ephemeral-evict-delay"
                  placeholder={t("例如 72h")}
                  invalid={Boolean(createForm.formState.errors.ephemeral_node_evict_delay)}
                  disabled={!createEphemeral}
                  {...createForm.register("ephemeral_node_evict_delay")}
                />
                {createForm.formState.errors.ephemeral_node_evict_delay?.message ? (
                  <p className="field-error">{t(createForm.formState.errors.ephemeral_node_evict_delay.message)}</p>
                ) : null}
              </div>

              <div className="subscription-switch-item">
                <label className="subscription-switch-label" htmlFor="create-sub-enabled">
                  <span>{t("启用")}</span>
                  <span
                    className="subscription-info-icon"
                    title={t(SUBSCRIPTION_DISABLE_HINT)}
                    aria-label={t(SUBSCRIPTION_DISABLE_HINT)}
                    tabIndex={0}
                  >
                    <Info size={13} />
                  </span>
                </label>
                <Switch id="create-sub-enabled" {...createForm.register("enabled")} />
              </div>

              <div className="detail-actions" style={{ justifyContent: "flex-end" }}>
                <Button type="submit" disabled={createMutation.isPending}>
                  {createMutation.isPending ? t("创建中...") : t("确认创建")}
                </Button>
                <Button variant="secondary" onClick={() => setCreateModalOpen(false)}>
                  {t("取消")}
                </Button>
              </div>
            </form>
          </Card>
        </div>
      ) : null}
    </section>
  );
}
