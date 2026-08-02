import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  Info,
  LockKeyhole,
  Pencil,
  Plus,
  RefreshCw,
  Sparkles,
  Trash2,
  X,
} from "lucide-react";
import { type FormEvent, useEffect, useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { OffsetPagination } from "../../components/ui/OffsetPagination";
import { Switch } from "../../components/ui/Switch";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { createEndpoint, deleteEndpoint, listEndpoints, updateEndpoint } from "./api";
import type { Endpoint, EndpointInput } from "./types";

type EndpointFormState = {
  port: string;
  enabled: boolean;
  allow_management: boolean;
  require_proxy_auth_info: boolean;
  allow_http_forward: boolean;
  allow_http_reverse: boolean;
  allow_socks5: boolean;
};

type TranslateFn = (text: string, options?: Record<string, unknown>) => string;

const EMPTY_ENDPOINTS: Endpoint[] = [];
const PAGE_SIZE_OPTIONS = [10, 20, 50, 100] as const;
const REQUIRE_PROXY_AUTH_LABEL = "强制客户端认证";
const REQUIRE_PROXY_AUTH_HINT = `一些应用（例如浏览器）只有在代理服务器强制要求认证的时候，才会发送认证信息。
因此，当 Resin 没有设置代理令牌时，这些应用不会向 Resin 发送认证字段，导致平台与账号信息缺失。
如果你的 Resin 部署没有设置代理令牌，同时又需要兼容这些应用，可以开启此选项。
注意：开启后，如果客户端没有发送认证信息，平台不再被视为 Default，而是拒绝请求。`;

const DEFAULT_FORM: EndpointFormState = {
  port: "",
  enabled: true,
  allow_management: false,
  require_proxy_auth_info: false,
  allow_http_forward: true,
  allow_http_reverse: true,
  allow_socks5: true,
};

function endpointToForm(endpoint: Endpoint | null): EndpointFormState {
  if (!endpoint) {
    return DEFAULT_FORM;
  }

  return {
    port: String(endpoint.port),
    enabled: endpoint.enabled,
    allow_management: endpoint.allow_management,
    require_proxy_auth_info:
      endpoint.require_proxy_auth_info && (endpoint.allow_http_forward || endpoint.allow_socks5),
    allow_http_forward: endpoint.allow_http_forward,
    allow_http_reverse: endpoint.allow_http_reverse,
    allow_socks5: endpoint.allow_socks5,
  };
}

function parseEndpointForm(
  form: EndpointFormState,
  endpoints: Endpoint[],
  editingID: string | null,
  t: TranslateFn,
): EndpointInput {
  const port = Number(form.port.trim());
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error(t("端口必须是 1 到 65535 之间的整数"));
  }
  if (endpoints.some((endpoint) => endpoint.id !== editingID && endpoint.port === port)) {
    throw new Error(t("端口 {{port}} 已被其他接入点使用", { port }));
  }

  const allowProxy = form.allow_http_forward || form.allow_http_reverse || form.allow_socks5;
  if (!form.allow_management && !allowProxy) {
    throw new Error(t("至少启用管理页面或一种代理能力"));
  }
  if (form.require_proxy_auth_info && !form.allow_http_forward && !form.allow_socks5) {
    throw new Error(t("强制客户端发送认证信息需要启用 HTTP 正向代理或 SOCKS5 代理"));
  }

  return {
    port,
    enabled: form.enabled,
    allow_management: form.allow_management,
    allow_proxy: allowProxy,
    require_proxy_auth_info: form.require_proxy_auth_info,
    allow_http_forward: form.allow_http_forward,
    allow_http_reverse: form.allow_http_reverse,
    allow_socks5: form.allow_socks5,
  };
}

function statusPresentation(status: string, t: TranslateFn) {
  switch (status) {
    case "active":
      return { label: t("运行中"), variant: "success" as const };
    case "starting":
      return { label: t("启动中"), variant: "warning" as const };
    case "error":
      return { label: t("异常"), variant: "danger" as const };
    default:
      return { label: t("未运行"), variant: "neutral" as const };
  }
}

type EndpointFormProps = {
  endpoint: Endpoint | null;
  endpoints: Endpoint[];
  pending: boolean;
  onClose: () => void;
  onSubmit: (input: EndpointInput) => Promise<void>;
};

function EndpointForm({ endpoint, endpoints, pending, onClose, onSubmit }: EndpointFormProps) {
  const { t } = useI18n();
  const [form, setForm] = useState<EndpointFormState>(() => endpointToForm(endpoint));
  const [formError, setFormError] = useState("");
  const isEditing = Boolean(endpoint);
  const readOnly = endpoint?.read_only ?? false;
  const authPolicyAvailable = form.allow_http_forward || form.allow_socks5;

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !pending) {
        onClose();
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [onClose, pending]);

  const setProtocol = (
    field: "allow_http_forward" | "allow_http_reverse" | "allow_socks5",
    enabled: boolean,
  ) => {
    setForm((current) => {
      const next = { ...current, [field]: enabled };
      if (!next.allow_http_forward && !next.allow_socks5) {
        next.require_proxy_auth_info = false;
      }
      return next;
    });
    setFormError("");
  };

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (readOnly) {
      return;
    }
    let input: EndpointInput;
    try {
      input = parseEndpointForm(form, endpoints, endpoint?.id ?? null, t);
    } catch (error) {
      setFormError(error instanceof Error ? error.message : t("未知错误"));
      return;
    }
    setFormError("");
    void onSubmit(input).catch(() => undefined);
  };

  return (
    <form className="form-grid" onSubmit={handleSubmit}>
      <div className="subscription-switch-item field-span-2">
        <label className="subscription-switch-label" htmlFor="endpoint-enabled">
          {t("启用")}
        </label>
        <Switch
          id="endpoint-enabled"
          checked={form.enabled}
          disabled={readOnly}
          onChange={(event) => {
            setForm((current) => ({ ...current, enabled: event.target.checked }));
            setFormError("");
          }}
        />
      </div>

      <div className="field-group field-span-2">
        <label className="field-label" htmlFor="endpoint-port">
          {t("监听端口")}
        </label>
        <Input
          id="endpoint-port"
          type="number"
          min={1}
          max={65535}
          step={1}
          inputMode="numeric"
          autoFocus={!readOnly}
          disabled={readOnly}
          value={form.port}
          onChange={(event) => {
            setForm((current) => ({ ...current, port: event.target.value }));
            setFormError("");
          }}
        />
      </div>

      <div className="field-group field-span-2">
        <label className="field-label">{t("接入能力")}</label>
        <div className="subscription-switch-group">
          <div className="subscription-switch-item">
            <label className="subscription-switch-label" htmlFor="endpoint-management">
              {t("登录管理页面")}
            </label>
            <Switch
              id="endpoint-management"
              checked={form.allow_management}
              disabled={readOnly}
              onChange={(event) => {
                setForm((current) => ({ ...current, allow_management: event.target.checked }));
                setFormError("");
              }}
            />
          </div>

          <div className="subscription-switch-item">
            <label className="subscription-switch-label" htmlFor="endpoint-http-forward">
              {t("HTTP 正向代理")}
            </label>
            <Switch
              id="endpoint-http-forward"
              checked={form.allow_http_forward}
              disabled={readOnly}
              onChange={(event) => setProtocol("allow_http_forward", event.target.checked)}
            />
          </div>

          <div className="subscription-switch-item">
            <label className="subscription-switch-label" htmlFor="endpoint-http-reverse">
              {t("HTTP 反向代理")}
            </label>
            <Switch
              id="endpoint-http-reverse"
              checked={form.allow_http_reverse}
              disabled={readOnly}
              onChange={(event) => setProtocol("allow_http_reverse", event.target.checked)}
            />
          </div>

          <div className="subscription-switch-item">
            <label className="subscription-switch-label" htmlFor="endpoint-socks5">
              {t("SOCKS5 代理")}
            </label>
            <Switch
              id="endpoint-socks5"
              checked={form.allow_socks5}
              disabled={readOnly}
              onChange={(event) => setProtocol("allow_socks5", event.target.checked)}
            />
          </div>
        </div>
      </div>

      <div className="field-group field-span-2">
        <label className="field-label">{t("认证策略")}</label>
        <div className="subscription-switch-item">
          <label className="subscription-switch-label" htmlFor="endpoint-require-auth-info">
            <span>{t(REQUIRE_PROXY_AUTH_LABEL)}</span>
            <span
              className="subscription-info-icon"
              title={t(REQUIRE_PROXY_AUTH_HINT)}
              aria-label={t(REQUIRE_PROXY_AUTH_HINT)}
              tabIndex={0}
            >
              <Info size={13} />
            </span>
          </label>
          <Switch
            id="endpoint-require-auth-info"
            checked={form.require_proxy_auth_info}
            disabled={readOnly || !authPolicyAvailable}
            onChange={(event) =>
              setForm((current) => ({ ...current, require_proxy_auth_info: event.target.checked }))
            }
          />
        </div>
      </div>

      {formError ? (
        <div className="callout callout-error field-span-2" role="alert">
          <AlertTriangle size={14} />
          <span>{formError}</span>
        </div>
      ) : null}

      {isEditing && !readOnly ? (
        <div className="platform-config-actions">
          <Button type="submit" disabled={pending}>
            {pending ? t("保存中...") : t("保存配置")}
          </Button>
        </div>
      ) : !isEditing ? (
        <div className="detail-actions" style={{ justifyContent: "flex-end" }}>
          <Button variant="secondary" onClick={onClose} disabled={pending}>
            {t("取消")}
          </Button>
          <Button type="submit" disabled={pending}>
            {pending ? t("创建中...") : t("确认创建")}
          </Button>
        </div>
      ) : null}
    </form>
  );
}

export function EndpointsPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState<number>(20);
  const [createModalOpen, setCreateModalOpen] = useState(false);
  const [editingEndpoint, setEditingEndpoint] = useState<Endpoint | null>(null);
  const [pendingEnabledStates, setPendingEnabledStates] = useState<Map<string, boolean>>(
    () => new Map(),
  );

  const endpointsQuery = useQuery({
    queryKey: ["endpoints", "page", page, pageSize],
    queryFn: () => listEndpoints({ limit: pageSize, offset: page * pageSize }),
    placeholderData: (previousData) => previousData,
    refetchInterval: 15_000,
  });
  const endpoints = endpointsQuery.data?.items ?? EMPTY_ENDPOINTS;
  const totalEndpoints = endpointsQuery.data?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(totalEndpoints / pageSize));
  const currentPage = Math.min(page, totalPages - 1);

  const invalidateEndpoints = async () => {
    await queryClient.invalidateQueries({ queryKey: ["endpoints"] });
  };

  const createMutation = useMutation({
    mutationFn: createEndpoint,
    onSuccess: async (endpoint) => {
      await invalidateEndpoints();
      setCreateModalOpen(false);
      showToast("success", t("接入点 :{{port}} 已创建", { port: endpoint.port }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const updateMutation = useMutation({
    mutationFn: ({ id, input }: { id: string; input: EndpointInput }) => updateEndpoint(id, input),
    onSuccess: async (endpoint) => {
      await invalidateEndpoints();
      setEditingEndpoint(null);
      showToast("success", t("接入点 :{{port}} 已更新", { port: endpoint.port }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const toggleEnabledMutation = useMutation({
    mutationFn: ({ endpoint, enabled }: { endpoint: Endpoint; enabled: boolean }) =>
      updateEndpoint(endpoint.id, { enabled }),
    onSuccess: async (endpoint, { enabled }) => {
      await invalidateEndpoints();
      showToast(
        "success",
        enabled
          ? t("接入点 :{{port}} 已启用", { port: endpoint.port })
          : t("接入点 :{{port}} 已禁用", { port: endpoint.port }),
      );
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async (endpoint: Endpoint) => {
      await deleteEndpoint(endpoint.id);
      return endpoint;
    },
    onSuccess: async (endpoint) => {
      if (endpoints.length === 1 && page > 0) {
        setPage(page - 1);
      }
      if (editingEndpoint?.id === endpoint.id) {
        setEditingEndpoint(null);
      }
      await invalidateEndpoints();
      showToast("success", t("接入点 :{{port}} 已删除", { port: endpoint.port }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const openCreateModal = () => {
    setEditingEndpoint(null);
    setCreateModalOpen(true);
  };

  const openEditDrawer = (endpoint: Endpoint) => {
    setCreateModalOpen(false);
    setEditingEndpoint(endpoint);
  };

  const closeCreateModal = () => {
    setCreateModalOpen(false);
  };

  const closeEditDrawer = () => {
    setEditingEndpoint(null);
  };

  const changePageSize = (nextPageSize: number) => {
    setPageSize(nextPageSize);
    setPage(0);
  };

  const submitCreateEndpoint = async (input: EndpointInput) => {
    await createMutation.mutateAsync(input);
  };

  const submitUpdateEndpoint = async (input: EndpointInput) => {
    if (!editingEndpoint || editingEndpoint.read_only) {
      return;
    }
    await updateMutation.mutateAsync({ id: editingEndpoint.id, input });
  };

  const handleDelete = async (endpoint: Endpoint) => {
    if (endpoint.read_only) {
      return;
    }
    const confirmed = window.confirm(t("确认删除接入点 :{{port}}？该端口将立即停止监听。", { port: endpoint.port }));
    if (!confirmed) {
      return;
    }
    await deleteMutation.mutateAsync(endpoint).catch(() => undefined);
  };

  const handleEnabledChange = async (endpoint: Endpoint, enabled: boolean) => {
    if (endpoint.read_only || pendingEnabledStates.has(endpoint.id)) {
      return;
    }
    setPendingEnabledStates((current) => new Map(current).set(endpoint.id, enabled));
    try {
      await toggleEnabledMutation.mutateAsync({ endpoint, enabled });
    } catch {
      // The mutation callback surfaces the API error.
    } finally {
      setPendingEnabledStates((current) => {
        const next = new Map(current);
        next.delete(endpoint.id);
        return next;
      });
    }
  };

  return (
    <section className="platform-page">
      <header className="module-header">
        <div>
          <h2>{t("接入点")}</h2>
          <p className="module-description">{t("管理监听端口及其可用的接入能力。")}</p>
        </div>
      </header>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <Card className="platform-list-card platform-directory-card endpoint-toolbar-card">
        <div className="list-card-header">
          <div>
            <h3>{t("接入点列表")}</h3>
            <p>{t("共 {{count}} 个接入点", { count: totalEndpoints })}</p>
          </div>
          <div className="endpoint-toolbar-actions">
            <Button variant="secondary" size="sm" onClick={openCreateModal}>
              <Plus size={16} />
              {t("新建")}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => void endpointsQuery.refetch()}
              disabled={endpointsQuery.isFetching}
            >
              <RefreshCw size={16} className={endpointsQuery.isFetching ? "spin" : undefined} />
              {t("刷新")}
            </Button>
          </div>
        </div>
      </Card>

      <Card className="platform-cards-container">
        {endpointsQuery.isLoading ? <p className="muted">{t("正在加载接入点...")}</p> : null}

        {endpointsQuery.isError ? (
          <div className="callout callout-error">
            <AlertTriangle size={14} />
            <span>{formatApiErrorMessage(endpointsQuery.error, t)}</span>
          </div>
        ) : null}

        {!endpointsQuery.isLoading && !endpointsQuery.isError && endpoints.length === 0 ? (
          <div className="empty-box">
            <Sparkles size={16} />
            <p>{t("暂无接入点")}</p>
          </div>
        ) : null}

        <div className="endpoint-list">
          {endpoints.map((endpoint) => {
            const status = statusPresentation(endpoint.status, t);
            const displayedEnabled = pendingEnabledStates.get(endpoint.id) ?? endpoint.enabled;
            const enabledToggleLabel = endpoint.read_only
              ? t("默认接入点由环境端口定义，不可修改或删除")
              : displayedEnabled
                ? t("禁用接入点 :{{port}}", { port: endpoint.port })
                : t("启用接入点 :{{port}}", { port: endpoint.port });
            const capabilities = [
              { label: t("登录管理页面"), enabled: endpoint.allow_management },
              { label: t("HTTP 正向代理"), enabled: endpoint.allow_http_forward },
              { label: t("HTTP 反向代理"), enabled: endpoint.allow_http_reverse },
              { label: t("SOCKS5 代理"), enabled: endpoint.allow_socks5 },
              {
                label: t("强制客户端认证"),
                enabled: endpoint.require_proxy_auth_info,
              },
            ];

            return (
              <article
                className={`platform-tile endpoint-tile${endpoint.read_only ? " is-read-only" : ""}`}
                key={endpoint.id}
                onClick={endpoint.read_only ? undefined : () => openEditDrawer(endpoint)}
              >
                <div className="platform-tile-head">
                  <div className="endpoint-tile-heading">
                    <span
                      className={`endpoint-status-dot is-${status.variant}`}
                      title={status.label}
                      role="img"
                      aria-label={status.label}
                    />
                    <p>{endpoint.port}</p>
                    {endpoint.read_only ? (
                      <div className="endpoint-tile-badges">
                        <Badge
                          variant="info"
                          title={t("默认接入点由环境端口定义，不可修改或删除")}
                        >
                          <LockKeyhole size={10} />
                          {t("默认 · 只读")}
                        </Badge>
                      </div>
                    ) : null}
                  </div>

                  <span
                    className="endpoint-enabled-toggle"
                    title={enabledToggleLabel}
                    onClick={(event) => event.stopPropagation()}
                  >
                    <Switch
                      checked={displayedEnabled}
                      disabled={endpoint.read_only || pendingEnabledStates.has(endpoint.id)}
                      onChange={(event) =>
                        void handleEnabledChange(endpoint, event.target.checked)
                      }
                      aria-label={enabledToggleLabel}
                    />
                  </span>
                </div>

                {endpoint.last_error ? (
                  <div className="callout callout-error" role="alert">
                    <AlertTriangle size={14} />
                    <span>{t("监听错误：{{message}}", { message: endpoint.last_error })}</span>
                  </div>
                ) : null}

                <div className="endpoint-tile-bottom">
                  <div className="platform-tile-facts endpoint-capabilities" aria-label={t("接入能力")}>
                    {capabilities.map((capability) => (
                      <span
                        className="endpoint-capability"
                        key={capability.label}
                        aria-label={`${capability.label} · ${capability.enabled ? t("已开启") : t("已关闭")}`}
                      >
                        <span
                          className={`endpoint-capability-indicator ${capability.enabled ? "is-enabled" : "is-disabled"}`}
                          aria-hidden="true"
                        />
                        <span>{capability.label}</span>
                        <span className="endpoint-capability-state">
                          {capability.enabled ? t("已开启") : t("已关闭")}
                        </span>
                      </span>
                    ))}
                  </div>

                  <div
                    className="subscriptions-row-actions"
                    onClick={(event) => event.stopPropagation()}
                  >
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => openEditDrawer(endpoint)}
                      disabled={endpoint.read_only}
                      title={endpoint.read_only ? t("默认接入点由环境端口定义，不可修改或删除") : t("编辑接入点")}
                      aria-label={t("编辑接入点 :{{port}}", { port: endpoint.port })}
                    >
                      <Pencil size={14} />
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => void handleDelete(endpoint)}
                      disabled={endpoint.read_only || deleteMutation.isPending}
                      title={endpoint.read_only ? t("默认接入点由环境端口定义，不可修改或删除") : t("删除接入点")}
                      aria-label={t("删除接入点 :{{port}}", { port: endpoint.port })}
                      style={{ color: "var(--delete-btn-color, #c27070)" }}
                    >
                      <Trash2 size={14} />
                    </Button>
                  </div>
                </div>
              </article>
            );
          })}
        </div>

        <OffsetPagination
          page={currentPage}
          totalPages={totalPages}
          totalItems={totalEndpoints}
          pageSize={pageSize}
          pageSizeOptions={PAGE_SIZE_OPTIONS}
          onPageChange={setPage}
          onPageSizeChange={changePageSize}
        />
      </Card>

      {editingEndpoint ? (
        <div
          className="drawer-overlay"
          role="dialog"
          aria-modal="true"
          aria-label={editingEndpoint.read_only ? t("接入点详情") : t("编辑接入点")}
          onClick={() => {
            if (!updateMutation.isPending) {
              closeEditDrawer();
            }
          }}
        >
          <Card className="drawer-panel" onClick={(event) => event.stopPropagation()}>
            <div className="drawer-header">
              <div>
                <h3>{editingEndpoint.port}</h3>
                <p>{editingEndpoint.id}</p>
              </div>
              <div className="drawer-header-actions">
                <Button
                  variant="ghost"
                  size="sm"
                  aria-label={t("关闭编辑面板")}
                  onClick={closeEditDrawer}
                  disabled={updateMutation.isPending}
                >
                  <X size={16} />
                </Button>
              </div>
            </div>

            <div className="platform-drawer-layout">
              <section className="platform-drawer-section">
                <div className="platform-drawer-section-head">
                  <h4>{t("接入点配置")}</h4>
                  <p>
                    {editingEndpoint.read_only
                      ? t("默认接入点由环境端口定义，不可修改或删除")
                      : t("修改监听端口和接入能力后保存。")}
                  </p>
                </div>
                <EndpointForm
                  key={editingEndpoint.id}
                  endpoint={editingEndpoint}
                  endpoints={endpoints}
                  pending={updateMutation.isPending}
                  onClose={closeEditDrawer}
                  onSubmit={submitUpdateEndpoint}
                />
              </section>

              {!editingEndpoint.read_only ? (
                <section className="platform-drawer-section platform-ops-section">
                  <div className="platform-drawer-section-head">
                    <h4>{t("运维操作")}</h4>
                  </div>
                  <div className="platform-ops-list">
                    <div className="platform-op-item">
                      <div className="platform-op-copy">
                        <h5>{t("删除接入点")}</h5>
                        <p className="platform-op-hint">{t("删除接入点并停止监听，操作不可撤销。")}</p>
                      </div>
                      <Button
                        variant="danger"
                        onClick={() => void handleDelete(editingEndpoint)}
                        disabled={deleteMutation.isPending}
                      >
                        {deleteMutation.isPending ? t("删除中...") : t("删除")}
                      </Button>
                    </div>
                  </div>
                </section>
              ) : null}
            </div>
          </Card>
        </div>
      ) : null}

      {createModalOpen ? (
        <div
          className="modal-overlay"
          role="dialog"
          aria-modal="true"
          aria-labelledby="endpoint-create-title"
          onMouseDown={(event) => {
            if (event.target === event.currentTarget && !createMutation.isPending) {
              closeCreateModal();
            }
          }}
        >
          <Card className="modal-card">
            <div className="modal-header">
              <h3 id="endpoint-create-title">{t("新建接入点")}</h3>
              <Button
                variant="ghost"
                size="sm"
                onClick={closeCreateModal}
                disabled={createMutation.isPending}
                title={t("关闭")}
                aria-label={t("关闭")}
              >
                <X size={16} />
              </Button>
            </div>
            <EndpointForm
              key="create"
              endpoint={null}
              endpoints={endpoints}
              pending={createMutation.isPending}
              onClose={closeCreateModal}
              onSubmit={submitCreateEndpoint}
            />
          </Card>
        </div>
      ) : null}
    </section>
  );
}
