import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Plus, RefreshCw, Search, Sparkles } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useForm } from "react-hook-form";
import { z } from "zod";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { ApiError } from "../../lib/api-client";
import { formatDateTime } from "../../lib/time";
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
  const [selectedSubscriptionId, setSelectedSubscriptionId] = useState("");
  const [createModalOpen, setCreateModalOpen] = useState(false);
  const [message, setMessage] = useState<{ tone: "success" | "error"; text: string } | null>(null);

  const queryClient = useQueryClient();
  const enabledValue = parseEnabledFilter(enabledFilter);

  const subscriptionsQuery = useQuery({
    queryKey: ["subscriptions", enabledFilter],
    queryFn: () => listSubscriptions(enabledValue),
    refetchInterval: 30_000,
  });

  const subscriptions = subscriptionsQuery.data ?? EMPTY_SUBSCRIPTIONS;

  const visibleSubscriptions = useMemo(() => {
    const keyword = search.trim().toLowerCase();
    if (!keyword) {
      return subscriptions;
    }

    return subscriptions.filter((subscription) => {
      return (
        subscription.name.toLowerCase().includes(keyword) ||
        subscription.id.toLowerCase().includes(keyword) ||
        subscription.url.toLowerCase().includes(keyword)
      );
    });
  }, [subscriptions, search]);

  const selectedSubscription = useMemo(() => {
    if (!subscriptions.length) {
      return null;
    }
    return subscriptions.find((item) => item.id === selectedSubscriptionId) ?? subscriptions[0];
  }, [subscriptions, selectedSubscriptionId]);

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
      update_interval: "5m",
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

  const invalidateSubscriptions = async () => {
    await queryClient.invalidateQueries({ queryKey: ["subscriptions"] });
  };

  const createMutation = useMutation({
    mutationFn: createSubscription,
    onSuccess: async (created) => {
      await invalidateSubscriptions();
      setSelectedSubscriptionId(created.id);
      setCreateModalOpen(false);
      createForm.reset({
        name: "",
        url: "",
        update_interval: "5m",
        enabled: true,
        ephemeral: false,
      });
      setMessage({ tone: "success", text: `订阅 ${created.name} 创建成功` });
    },
    onError: (error) => {
      setMessage({ tone: "error", text: fromApiError(error) });
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
      setMessage({ tone: "success", text: `订阅 ${updated.name} 已更新` });
    },
    onError: (error) => {
      setMessage({ tone: "error", text: fromApiError(error) });
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async (subscription: Subscription) => {
      await deleteSubscription(subscription.id);
      return subscription;
    },
    onSuccess: async (deleted) => {
      await invalidateSubscriptions();
      setMessage({ tone: "success", text: `订阅 ${deleted.name} 已删除` });
    },
    onError: (error) => {
      setMessage({ tone: "error", text: fromApiError(error) });
    },
  });

  const refreshMutation = useMutation({
    mutationFn: async (subscription: Subscription) => {
      await refreshSubscription(subscription.id);
      return subscription;
    },
    onSuccess: async (subscription) => {
      await invalidateSubscriptions();
      setMessage({ tone: "success", text: `订阅 ${subscription.name} 已手动刷新` });
    },
    onError: (error) => {
      setMessage({ tone: "error", text: fromApiError(error) });
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

  return (
    <section className="platform-page">
      <header className="module-header">
        <div>
          <h2>订阅管理</h2>
          <p className="module-description">维护订阅源、更新周期与开关状态，并支持手动触发刷新。</p>
        </div>
        <Button onClick={() => setCreateModalOpen(true)}>
          <Plus size={16} />
          新建订阅
        </Button>
      </header>

      {message ? (
        <div className={message.tone === "success" ? "callout callout-success" : "callout callout-error"}>
          {message.text}
        </div>
      ) : null}

      <div className="platform-grid">
        <Card className="platform-list-card">
          <div className="list-card-header">
            <div>
              <h3>订阅列表</h3>
              <p>共 {subscriptions.length} 个订阅</p>
            </div>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => subscriptionsQuery.refetch()}
              disabled={subscriptionsQuery.isFetching}
            >
              <RefreshCw size={14} className={subscriptionsQuery.isFetching ? "spin" : undefined} />
              刷新
            </Button>
          </div>

          <div className="row-gap-sm">
            <label className="field-label" htmlFor="sub-status-filter">
              状态筛选
            </label>
            <Select
              id="sub-status-filter"
              value={enabledFilter}
              onChange={(event) => setEnabledFilter(event.target.value as EnabledFilter)}
            >
              <option value="all">全部</option>
              <option value="enabled">仅启用</option>
              <option value="disabled">仅禁用</option>
            </Select>
          </div>

          <label className="search-box" htmlFor="subscription-search">
            <Search size={14} />
            <Input
              id="subscription-search"
              placeholder="按名称 / URL / ID 过滤"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
          </label>

          {subscriptionsQuery.isLoading ? <p className="muted">正在加载订阅数据...</p> : null}

          {subscriptionsQuery.isError ? (
            <div className="callout callout-error">
              <AlertTriangle size={14} />
              <span>{fromApiError(subscriptionsQuery.error)}</span>
            </div>
          ) : null}

          {!subscriptionsQuery.isLoading && !visibleSubscriptions.length ? (
            <div className="empty-box">
              <Sparkles size={16} />
              <p>没有匹配的订阅</p>
            </div>
          ) : null}

          <div className="platform-list">
            {visibleSubscriptions.map((subscription) => (
              <button
                key={subscription.id}
                type="button"
                className={`platform-row ${subscription.id === selectedSubscription?.id ? "platform-row-active" : ""}`}
                onClick={() => setSelectedSubscriptionId(subscription.id)}
              >
                <div className="platform-row-main">
                  <p>{subscription.name}</p>
                  <span>{subscription.url}</span>
                </div>
                <div className="platform-row-meta wrap">
                  <Badge variant={subscription.enabled ? "success" : "warning"}>
                    {subscription.enabled ? "已启用" : "已禁用"}
                  </Badge>
                  {subscription.ephemeral ? <Badge variant="neutral">Ephemeral</Badge> : null}
                </div>
              </button>
            ))}
          </div>
        </Card>

        <Card className="platform-detail-card">
          {!selectedSubscription ? (
            <div className="empty-box full-height">
              <Sparkles size={18} />
              <p>请选择一个订阅开始编辑</p>
            </div>
          ) : (
            <>
              <div className="detail-header">
                <div>
                  <h3>{selectedSubscription.name}</h3>
                  <p>{selectedSubscription.id}</p>
                </div>
                <Badge variant={selectedSubscription.enabled ? "success" : "warning"}>
                  {selectedSubscription.enabled ? "运行中" : "已停用"}
                </Badge>
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
                    Update Interval
                  </label>
                  <Input
                    id="edit-sub-interval"
                    placeholder="例如 5m"
                    invalid={Boolean(editForm.formState.errors.update_interval)}
                    {...editForm.register("update_interval")}
                  />
                  {editForm.formState.errors.update_interval?.message ? (
                    <p className="field-error">{editForm.formState.errors.update_interval.message}</p>
                  ) : null}
                </div>

                <div className="field-group field-span-2">
                  <label className="field-label" htmlFor="edit-sub-url">
                    URL
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

                <div className="detail-actions">
                  <Button type="submit" disabled={updateMutation.isPending}>
                    保存修改
                  </Button>
                  <Button
                    variant="secondary"
                    onClick={() => void refreshMutation.mutateAsync(selectedSubscription)}
                    disabled={refreshMutation.isPending}
                  >
                    手动刷新
                  </Button>
                  <Button
                    variant="danger"
                    onClick={() => void handleDelete(selectedSubscription)}
                    disabled={deleteMutation.isPending}
                  >
                    删除订阅
                  </Button>
                </div>
              </form>
            </>
          )}
        </Card>
      </div>

      {createModalOpen ? (
        <div className="modal-overlay" role="dialog" aria-modal="true">
          <Card className="modal-card">
            <div className="modal-header">
              <h3>新建订阅</h3>
              <Button variant="ghost" size="sm" onClick={() => setCreateModalOpen(false)}>
                关闭
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
                  Update Interval
                </label>
                <Input
                  id="create-sub-interval"
                  placeholder="例如 5m"
                  invalid={Boolean(createForm.formState.errors.update_interval)}
                  {...createForm.register("update_interval")}
                />
                {createForm.formState.errors.update_interval?.message ? (
                  <p className="field-error">{createForm.formState.errors.update_interval.message}</p>
                ) : null}
              </div>

              <div className="field-group field-span-2">
                <label className="field-label" htmlFor="create-sub-url">
                  URL
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

              <div className="detail-actions">
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
