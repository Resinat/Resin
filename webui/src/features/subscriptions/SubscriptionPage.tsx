import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Filter, Pencil, Plus, RefreshCw, Search, Sparkles, Trash2, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useForm } from "react-hook-form";
import { z } from "zod";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { OffsetPagination } from "../../components/ui/OffsetPagination";
import { Select } from "../../components/ui/Select";
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
  enabled: z.boolean(),
  ephemeral: z.boolean(),
});

const subscriptionEditSchema = subscriptionCreateSchema;

type SubscriptionCreateForm = z.infer<typeof subscriptionCreateSchema>;
type SubscriptionEditForm = z.infer<typeof subscriptionEditSchema>;
const EMPTY_SUBSCRIPTIONS: Subscription[] = [];
const PAGE_SIZE_OPTIONS = [10, 20, 50, 100] as const;

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
      update_interval: "5m",
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

  const onCreateSubmit = createForm.handleSubmit(async (values) => {
    await createMutation.mutateAsync({
      name: values.name.trim(),
      url: values.url.trim(),
      update_interval: values.update_interval.trim(),
      enabled: values.enabled,
      ephemeral: values.ephemeral,
    });
  });

  const onEditSubmit = editForm.handleSubmit(async (values) => {
    await updateMutation.mutateAsync(values);
  });

  const handleDelete = async (subscription: Subscription) => {
    const confirmed = window.confirm(`确认删除订阅 ${subscription.name}？关联节点会被清理。`);
    if (!confirmed) {
      return;
    }
    await deleteMutation.mutateAsync(subscription);
  };

  const openDrawer = (subscription: Subscription) => {
    setSelectedSubscriptionId(subscription.id);
    setDrawerOpen(true);
  };

  const changePageSize = (next: number) => {
    setPageSize(next);
    setPage(0);
  };

  return (
    <section className="platform-page">
      <header className="module-header">
        <div>
          <h2>订阅管理</h2>
          <p className="module-description">维护订阅源、更新周期与开关状态，并支持手动触发刷新。</p>
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
          <div className="nodes-table-wrap">
            <table className="nodes-table subscriptions-table">
              <thead>
                <tr>
                  <th>名称</th>
                  <th>订阅站点</th>
                  <th>更新间隔</th>
                  <th>状态</th>
                  <th>上次检查</th>
                  <th>上次更新</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {subscriptions.map((subscription) => (
                  <tr
                    key={subscription.id}
                    className="clickable-row"
                    onClick={() => openDrawer(subscription)}
                  >
                    <td>
                      <p className="subscriptions-name-cell">{subscription.name}</p>
                    </td>
                    <td>
                      <p className="subscriptions-url-cell" title={subscription.url}>
                        {extractHostname(subscription.url)}
                      </p>
                    </td>
                    <td>{formatGoDuration(subscription.update_interval)}</td>
                    <td>
                      <div className="subscriptions-status-cell">
                        {!subscription.enabled ? (
                          <Badge variant="warning">已禁用</Badge>
                        ) : subscription.last_error ? (
                          <Badge variant="danger">错误</Badge>
                        ) : (
                          <Badge variant="success">正常</Badge>
                        )}
                      </div>
                    </td>
                    <td>{formatRelativeTime(subscription.last_checked || "")}</td>
                    <td>{formatRelativeTime(subscription.last_updated || "")}</td>
                    <td>
                      <div className="subscriptions-row-actions" onClick={(event) => event.stopPropagation()}>
                        <Button size="sm" variant="ghost" onClick={() => openDrawer(subscription)} title="编辑">
                          <Pencil size={14} />
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() => void refreshMutation.mutateAsync(subscription)}
                          disabled={refreshMutation.isPending}
                          title="刷新"
                        >
                          <RefreshCw size={14} />
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() => void handleDelete(subscription)}
                          disabled={deleteMutation.isPending}
                          title="删除"
                          style={{ color: "var(--delete-btn-color, #c27070)" }}
                        >
                          <Trash2 size={14} />
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
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
                  <div className="callout callout-error">Last Error: {selectedSubscription.last_error}</div>
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

                  <div className="field-group field-span-2">
                    <label className="field-label" htmlFor="edit-sub-url">
                      订阅链接
                    </label>
                    <Input id="edit-sub-url" invalid={Boolean(editForm.formState.errors.url)} {...editForm.register("url")} />
                    {editForm.formState.errors.url?.message ? (
                      <p className="field-error">{editForm.formState.errors.url.message}</p>
                    ) : null}
                  </div>

                  <div className="checkbox-group field-span-2">
                    <label className="checkbox-line">
                      <input type="checkbox" {...editForm.register("enabled")} />
                      <span>Enabled</span>
                    </label>
                    <label className="checkbox-line">
                      <input type="checkbox" {...editForm.register("ephemeral")} />
                      <span>Ephemeral</span>
                    </label>
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
                      <p className="platform-op-hint">立即触发一次订阅源拉取并刷新对应节点。</p>
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

              <div className="checkbox-group field-span-2">
                <label className="checkbox-line">
                  <input type="checkbox" {...createForm.register("enabled")} />
                  <span>Enabled</span>
                </label>
                <label className="checkbox-line">
                  <input type="checkbox" {...createForm.register("ephemeral")} />
                  <span>Ephemeral</span>
                </label>
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
