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
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { ApiError } from "../../lib/api-client";
import { formatDateTime, formatGoDuration, formatRelativeTime } from "../../lib/time";
import {
  createSubscription,
  deleteSubscription,
  listSubscriptions,
  refreshSubscription,
  updateSubscription,
} from "./api";
import type { Subscription } from "./types";

type EnabledFilter = "all" | "enabled" | "disabled";

const subscriptionCreateSchema = z.object({
  name: z.string().trim().min(1, "订阅名称不能为空"),
  url: z
    .string()
    .trim()
    .min(1, "URL 不能为空")
    .refine((value) => value.startsWith("http://") || value.startsWith("https://"), {
      message: "URL 必须是 http/https 地址",
    }),
  update_interval: z.string().trim().min(1, "更新间隔不能为空"),
  ephemeral_node_evict_delay: z.string().trim().min(1, "临时节点驱逐延迟不能为空"),
  enabled: z.boolean(),
  ephemeral: z.boolean(),
});

const subscriptionEditSchema = subscriptionCreateSchema;

type SubscriptionCreateForm = z.infer<typeof subscriptionCreateSchema>;
type SubscriptionEditForm = z.infer<typeof subscriptionEditSchema>;
const EMPTY_SUBSCRIPTIONS: Subscription[] = [];
const PAGE_SIZE_OPTIONS = [10, 20, 50, 100] as const;
const SUBSCRIPTION_DISABLE_HINT = "禁用订阅后，节点不会进入各平台的路由池，但不会从全局节点池中删除。";

function extractHostname(url: string): string {
  try {
    return new URL(url).hostname;
  } catch {
    return url;
  }
}

function fromApiError(error: unknown): string {
  if (error instanceof ApiError) {
    return `${error.code}: ${error.message}`;
  }
  if (error instanceof Error) {
    return error.message;
  }
  return "未知错误";
}

function subscriptionToEditForm(subscription: Subscription): SubscriptionEditForm {
  return {
    name: subscription.name,
    url: subscription.url,
    update_interval: subscription.update_interval,
    ephemeral_node_evict_delay: subscription.ephemeral_node_evict_delay,
    enabled: subscription.enabled,
    ephemeral: subscription.ephemeral,
  };
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

export function SubscriptionPage() {
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
      url: "",
      update_interval: "12h",
      ephemeral_node_evict_delay: "72h",
      enabled: true,
      ephemeral: false,
    },
  });

  const editForm = useForm<SubscriptionEditForm>({
    resolver: zodResolver(subscriptionEditSchema),
    defaultValues: {
      name: "",
      url: "",
      update_interval: "12h",
      ephemeral_node_evict_delay: "72h",
      enabled: true,
      ephemeral: false,
    },
  });

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

  const createMutation = useMutation({
    mutationFn: createSubscription,
    onSuccess: async (created) => {
      await invalidateSubscriptions();
      setCreateModalOpen(false);
      createForm.reset({
        name: "",
        url: "",
        update_interval: "12h",
        ephemeral_node_evict_delay: "72h",
        enabled: true,
        ephemeral: false,
      });
      showToast("success", `订阅 ${created.name} 创建成功`);
    },
    onError: (error) => {
      showToast("error", fromApiError(error));
    },
  });

  const updateMutation = useMutation({
    mutationFn: async (formData: SubscriptionEditForm) => {
      if (!selectedSubscription) {
        throw new Error("请选择要编辑的订阅");
      }

      return updateSubscription(selectedSubscription.id, {
        name: formData.name.trim(),
        url: formData.url.trim(),
        update_interval: formData.update_interval.trim(),
        ephemeral_node_evict_delay: formData.ephemeral_node_evict_delay.trim(),
        enabled: formData.enabled,
        ephemeral: formData.ephemeral,
      });
    },
    onSuccess: async (updated) => {
      await invalidateSubscriptions();
      setSelectedSubscriptionId(updated.id);
      showToast("success", `订阅 ${updated.name} 已更新`);
    },
    onError: (error) => {
      showToast("error", fromApiError(error));
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
      showToast("success", `订阅 ${deleted.name} 已删除`);
    },
    onError: (error) => {
      showToast("error", fromApiError(error));
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
      showToast("success", `订阅 ${subscription.name} 已手动刷新`);
    },
    onError: (error) => {
      showToast("error", fromApiError(error));
    },
  });
  const refreshSubscriptionMutateAsync = refreshMutation.mutateAsync;
  const isRefreshPending = refreshMutation.isPending;

  const onCreateSubmit = createForm.handleSubmit(async (values) => {
    await createMutation.mutateAsync({
      name: values.name.trim(),
      url: values.url.trim(),
      update_interval: values.update_interval.trim(),
      ephemeral_node_evict_delay: values.ephemeral_node_evict_delay.trim(),
      enabled: values.enabled,
      ephemeral: values.ephemeral,
    });
  });

  const onEditSubmit = editForm.handleSubmit(async (values) => {
    await updateMutation.mutateAsync(values);
  });

  const handleDelete = useCallback(async (subscription: Subscription) => {
    const confirmed = window.confirm(`确认删除订阅 ${subscription.name}？关联节点会被清理。`);
    if (!confirmed) {
      return;
    }
    await deleteSubscriptionMutateAsync(subscription);
  }, [deleteSubscriptionMutateAsync]);

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
        header: "名称",
        cell: (info) => <p className="subscriptions-name-cell">{info.getValue()}</p>,
      }),
      col.accessor("url", {
        header: "订阅站点",
        cell: (info) => (
          <p className="subscriptions-url-cell" title={info.getValue()}>
            {extractHostname(info.getValue())}
          </p>
        ),
      }),
      col.accessor("update_interval", {
        header: "更新间隔",
        cell: (info) => formatGoDuration(info.getValue()),
      }),
      col.accessor("node_count", {
        header: "节点数",
      }),
      col.display({
        id: "status",
        header: "状态",
        cell: (info) => {
          const s = info.row.original;
          return (
            <div className="subscriptions-status-cell">
              {!s.enabled ? (
                <Badge variant="warning">已禁用</Badge>
              ) : s.last_error ? (
                <Badge variant="danger">错误</Badge>
              ) : (
                <Badge variant="success">正常</Badge>
              )}
            </div>
          );
        },
      }),
      col.accessor("last_checked", {
        header: "上次检查",
        cell: (info) => formatRelativeTime(info.getValue() || ""),
      }),
      col.accessor("last_updated", {
        header: "上次更新",
        cell: (info) => formatRelativeTime(info.getValue() || ""),
      }),
      col.display({
        id: "actions",
        header: "操作",
        cell: (info) => {
          const s = info.row.original;
          return (
            <div className="subscriptions-row-actions" onClick={(event) => event.stopPropagation()}>
              <Link
                className="btn btn-ghost btn-sm"
                to={`/nodes?subscription_id=${encodeURIComponent(s.id)}`}
                title="预览节点池"
                aria-label={`预览订阅 ${s.name} 的节点池`}
              >
                <Eye size={14} />
              </Link>
              <Button size="sm" variant="ghost" onClick={() => openDrawer(s)} title="编辑">
                <Pencil size={14} />
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => void handleRefresh(s)}
                disabled={isRefreshPending}
                title="刷新"
              >
                <RefreshCw size={14} />
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => void handleDelete(s)}
                disabled={isDeletePending}
                title="删除"
                style={{ color: "var(--delete-btn-color, #c27070)" }}
              >
                <Trash2 size={14} />
              </Button>
            </div>
          );
        },
      }),
    ],
    [col, handleDelete, handleRefresh, isDeletePending, isRefreshPending, openDrawer]
  );

  return (
    <section className="platform-page">
      <header className="module-header">
        <div>
          <h2>订阅管理</h2>
          <p className="module-description">保障订阅按计划更新，异常时可一键刷新。</p>
        </div>
      </header>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <Card className="platform-list-card platform-directory-card">
        <div className="list-card-header">
          <div>
            <h3>订阅列表</h3>
            <p>共 {totalSubscriptions} 个订阅</p>
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
                <option value="all">全部</option>
                <option value="enabled">仅启用</option>
                <option value="disabled">仅禁用</option>
              </Select>
            </label>
            <label className="search-box" htmlFor="subscription-search" style={{ maxWidth: 200, margin: 0, gap: 6 }}>
              <Search size={16} />
              <Input
                id="subscription-search"
                placeholder="搜索订阅"
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
              新建
            </Button>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => subscriptionsQuery.refetch()}
              disabled={subscriptionsQuery.isFetching}
            >
              <RefreshCw size={16} className={subscriptionsQuery.isFetching ? "spin" : undefined} />
              刷新
            </Button>
          </div>
        </div>
      </Card>

      <Card className="platform-cards-container subscriptions-table-card">
        {subscriptionsQuery.isLoading ? <p className="muted">正在加载订阅数据...</p> : null}

        {subscriptionsQuery.isError ? (
          <div className="callout callout-error">
            <AlertTriangle size={14} />
            <span>{fromApiError(subscriptionsQuery.error)}</span>
          </div>
        ) : null}

        {!subscriptionsQuery.isLoading && !subscriptions.length ? (
          <div className="empty-box">
            <Sparkles size={16} />
            <p>没有匹配的订阅</p>
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
          aria-label={`编辑订阅 ${selectedSubscription.name}`}
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
                  {selectedSubscription.enabled ? "运行中" : "已停用"}
                </Badge>
                <Button
                  variant="ghost"
                  size="sm"
                  aria-label="关闭编辑面板"
                  onClick={() => setDrawerOpen(false)}
                >
                  <X size={16} />
                </Button>
              </div>
            </div>
            <div className="platform-drawer-layout">
              <section className="platform-drawer-section">
                <div className="platform-drawer-section-head">
                  <h4>订阅配置</h4>
                  <p>更新 URL、刷新周期与状态开关后点击保存。</p>
                </div>

                <div className="stats-grid">
                  <div>
                    <span>创建时间</span>
                    <p>{formatDateTime(selectedSubscription.created_at)}</p>
                  </div>
                  <div>
                    <span>上次检查</span>
                    <p>{formatDateTime(selectedSubscription.last_checked || "")}</p>
                  </div>
                  <div>
                    <span>上次更新</span>
                    <p>{formatDateTime(selectedSubscription.last_updated || "")}</p>
                  </div>
                </div>

                {selectedSubscription.last_error ? (
                  <div className="callout callout-error">最近错误：{selectedSubscription.last_error}</div>
                ) : (
                  <div className="callout callout-success">最近一次刷新无错误</div>
                )}

                <form className="form-grid" onSubmit={onEditSubmit}>
                  <div className="field-group">
                    <label className="field-label" htmlFor="edit-sub-name">
                      订阅名称
                    </label>
                    <Input
                      id="edit-sub-name"
                      invalid={Boolean(editForm.formState.errors.name)}
                      {...editForm.register("name")}
                    />
                    {editForm.formState.errors.name?.message ? (
                      <p className="field-error">{editForm.formState.errors.name.message}</p>
                    ) : null}
                  </div>

                  <div className="field-group">
                    <label className="field-label" htmlFor="edit-sub-interval">
                      更新间隔
                    </label>
                    <Input
                      id="edit-sub-interval"
                      placeholder="例如 12h"
                      invalid={Boolean(editForm.formState.errors.update_interval)}
                      {...editForm.register("update_interval")}
                    />
                    {editForm.formState.errors.update_interval?.message ? (
                      <p className="field-error">{editForm.formState.errors.update_interval.message}</p>
                    ) : null}
                  </div>

                  <div className="field-group">
                    <label className="field-label" htmlFor="edit-sub-ephemeral-evict-delay">
                      临时节点驱逐延迟（仅临时订阅生效）
                    </label>
                    <Input
                      id="edit-sub-ephemeral-evict-delay"
                      placeholder="例如 72h"
                      invalid={Boolean(editForm.formState.errors.ephemeral_node_evict_delay)}
                      {...editForm.register("ephemeral_node_evict_delay")}
                    />
                    {editForm.formState.errors.ephemeral_node_evict_delay?.message ? (
                      <p className="field-error">{editForm.formState.errors.ephemeral_node_evict_delay.message}</p>
                    ) : null}
                  </div>

                  <div className="field-group field-span-2">
                    <label className="field-label" htmlFor="edit-sub-url">
                      订阅链接
                    </label>
                    <Input id="edit-sub-url" invalid={Boolean(editForm.formState.errors.url)} {...editForm.register("url")} />
                    {editForm.formState.errors.url?.message ? (
                      <p className="field-error">{editForm.formState.errors.url.message}</p>
                    ) : null}
                  </div>

                  <div className="subscription-switch-group field-span-2">
                    <div className="subscription-switch-item">
                      <label className="subscription-switch-label" htmlFor="edit-sub-enabled">
                        <span>启用</span>
                        <span
                          className="subscription-info-icon"
                          title={SUBSCRIPTION_DISABLE_HINT}
                          aria-label={SUBSCRIPTION_DISABLE_HINT}
                          tabIndex={0}
                        >
                          <Info size={13} />
                        </span>
                      </label>
                      <Switch id="edit-sub-enabled" {...editForm.register("enabled")} />
                    </div>
                    <div className="subscription-switch-item">
                      <label className="subscription-switch-label" htmlFor="edit-sub-ephemeral">
                        临时订阅
                      </label>
                      <Switch id="edit-sub-ephemeral" {...editForm.register("ephemeral")} />
                    </div>
                  </div>

                  <div className="platform-config-actions">
                    <Button type="submit" disabled={updateMutation.isPending}>
                      {updateMutation.isPending ? "保存中..." : "保存配置"}
                    </Button>
                  </div>
                </form>
              </section>

              <section className="platform-drawer-section platform-ops-section">
                <div className="platform-drawer-section-head">
                  <h4>运维操作</h4>
                </div>

                <div className="platform-ops-list">
                  <div className="platform-op-item">
                    <div className="platform-op-copy">
                      <h5>手动刷新</h5>
                      <p className="platform-op-hint">立即刷新订阅并同步节点。</p>
                    </div>
                    <Button
                      variant="secondary"
                      onClick={() => void refreshMutation.mutateAsync(selectedSubscription)}
                      disabled={refreshMutation.isPending}
                    >
                      {refreshMutation.isPending ? "刷新中..." : "立即刷新"}
                    </Button>
                  </div>

                  <div className="platform-op-item">
                    <div className="platform-op-copy">
                      <h5>删除订阅</h5>
                      <p className="platform-op-hint">删除订阅并清理关联节点，操作不可撤销。</p>
                    </div>
                    <Button
                      variant="danger"
                      onClick={() => void handleDelete(selectedSubscription)}
                      disabled={deleteMutation.isPending}
                    >
                      {deleteMutation.isPending ? "删除中..." : "删除订阅"}
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
              <h3>新建订阅</h3>
              <Button variant="ghost" size="sm" onClick={() => setCreateModalOpen(false)}>
                <X size={16} />
              </Button>
            </div>

            <form className="form-grid" onSubmit={onCreateSubmit}>
              <div className="field-group">
                <label className="field-label" htmlFor="create-sub-name">
                  订阅名称
                </label>
                <Input
                  id="create-sub-name"
                  invalid={Boolean(createForm.formState.errors.name)}
                  {...createForm.register("name")}
                />
                {createForm.formState.errors.name?.message ? (
                  <p className="field-error">{createForm.formState.errors.name.message}</p>
                ) : null}
              </div>

              <div className="field-group">
                <label className="field-label" htmlFor="create-sub-interval">
                  更新间隔
                </label>
                <Input
                  id="create-sub-interval"
                  placeholder="例如 12h"
                  invalid={Boolean(createForm.formState.errors.update_interval)}
                  {...createForm.register("update_interval")}
                />
                {createForm.formState.errors.update_interval?.message ? (
                  <p className="field-error">{createForm.formState.errors.update_interval.message}</p>
                ) : null}
              </div>

              <div className="field-group">
                <label className="field-label" htmlFor="create-sub-ephemeral-evict-delay">
                  临时节点驱逐延迟（仅临时订阅生效）
                </label>
                <Input
                  id="create-sub-ephemeral-evict-delay"
                  placeholder="例如 72h"
                  invalid={Boolean(createForm.formState.errors.ephemeral_node_evict_delay)}
                  {...createForm.register("ephemeral_node_evict_delay")}
                />
                {createForm.formState.errors.ephemeral_node_evict_delay?.message ? (
                  <p className="field-error">{createForm.formState.errors.ephemeral_node_evict_delay.message}</p>
                ) : null}
              </div>

              <div className="field-group field-span-2">
                <label className="field-label" htmlFor="create-sub-url">
                  订阅链接
                </label>
                <Input
                  id="create-sub-url"
                  invalid={Boolean(createForm.formState.errors.url)}
                  {...createForm.register("url")}
                />
                {createForm.formState.errors.url?.message ? (
                  <p className="field-error">{createForm.formState.errors.url.message}</p>
                ) : null}
              </div>

              <div className="subscription-switch-group field-span-2">
                <div className="subscription-switch-item">
                  <label className="subscription-switch-label" htmlFor="create-sub-enabled">
                    <span>启用</span>
                    <span
                      className="subscription-info-icon"
                      title={SUBSCRIPTION_DISABLE_HINT}
                      aria-label={SUBSCRIPTION_DISABLE_HINT}
                      tabIndex={0}
                    >
                      <Info size={13} />
                    </span>
                  </label>
                  <Switch id="create-sub-enabled" {...createForm.register("enabled")} />
                </div>
                <div className="subscription-switch-item">
                  <label className="subscription-switch-label" htmlFor="create-sub-ephemeral">
                    临时订阅
                  </label>
                  <Switch id="create-sub-ephemeral" {...createForm.register("ephemeral")} />
                </div>
              </div>

              <div className="detail-actions" style={{ justifyContent: "flex-end" }}>
                <Button type="submit" disabled={createMutation.isPending}>
                  {createMutation.isPending ? "创建中..." : "确认创建"}
                </Button>
                <Button variant="secondary" onClick={() => setCreateModalOpen(false)}>
                  取消
                </Button>
              </div>
            </form>
          </Card>
        </div>
      ) : null}
    </section>
  );
}
