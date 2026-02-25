import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ArrowLeft, Link2, RefreshCw } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { Link, useNavigate, useParams } from "react-router-dom";
import { z } from "zod";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { Textarea } from "../../components/ui/Textarea";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { ApiError } from "../../lib/api-client";
import { formatGoDuration, formatRelativeTime } from "../../lib/time";
import { deletePlatform, getPlatform, rebuildPlatform, resetPlatform, updatePlatform } from "./api";
import { PlatformMonitorPanel } from "./PlatformMonitorPanel";
import type { Platform, PlatformAllocationPolicy, PlatformMissAction } from "./types";

const allocationPolicies: PlatformAllocationPolicy[] = [
  "BALANCED",
  "PREFER_LOW_LATENCY",
  "PREFER_IDLE_IP",
];

const missActions: PlatformMissAction[] = ["RANDOM", "REJECT"];

const allocationPolicyLabel: Record<PlatformAllocationPolicy, string> = {
  BALANCED: "均衡",
  PREFER_LOW_LATENCY: "优先低延迟",
  PREFER_IDLE_IP: "优先空闲出口 IP",
};

const missActionLabel: Record<PlatformMissAction, string> = {
  RANDOM: "随机选择节点",
  REJECT: "拒绝代理请求",
};

const platformEditSchema = z.object({
  name: z.string().trim().min(1, "平台名称不能为空"),
  sticky_ttl: z.string().optional(),
  regex_filters_text: z.string().optional(),
  region_filters_text: z.string().optional(),
  reverse_proxy_miss_action: z.enum(missActions),
  allocation_policy: z.enum(allocationPolicies),
});

type PlatformEditForm = z.infer<typeof platformEditSchema>;
type PlatformDetailTab = "monitor" | "config" | "ops";

const ZERO_UUID = "00000000-0000-0000-0000-000000000000";
const DETAIL_TABS: Array<{ key: PlatformDetailTab; label: string; hint: string }> = [
  { key: "monitor", label: "监控", hint: "平台运行态趋势和快照" },
  { key: "config", label: "配置", hint: "过滤规则与分配策略" },
  { key: "ops", label: "运维", hint: "重建、重置、删除操作" },
];

function parseLinesToList(input: string | undefined, normalize?: (value: string) => string): string[] {
  if (!input) {
    return [];
  }

  return input
    .split(/\n/)
    .map((item) => item.trim())
    .filter(Boolean)
    .map((item) => (normalize ? normalize(item) : item));
}

function platformToEditForm(platform: Platform): PlatformEditForm {
  const regexFilters = Array.isArray(platform.regex_filters) ? platform.regex_filters : [];
  const regionFilters = Array.isArray(platform.region_filters) ? platform.region_filters : [];

  return {
    name: platform.name,
    sticky_ttl: platform.sticky_ttl,
    regex_filters_text: regexFilters.join("\n"),
    region_filters_text: regionFilters.join("\n"),
    reverse_proxy_miss_action: platform.reverse_proxy_miss_action,
    allocation_policy: platform.allocation_policy,
  };
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

export function PlatformDetailPage() {
  const { t } = useI18n();
  const { platformId = "" } = useParams();
  const navigate = useNavigate();
  const [activeTab, setActiveTab] = useState<PlatformDetailTab>("monitor");
  const { toasts, showToast, dismissToast } = useToast();
  const queryClient = useQueryClient();

  const platformQuery = useQuery({
    queryKey: ["platform", platformId],
    queryFn: () => getPlatform(platformId),
    enabled: Boolean(platformId),
    refetchInterval: 30_000,
    placeholderData: (previous) => previous,
  });

  const platform = platformQuery.data ?? null;

  const editForm = useForm<PlatformEditForm>({
    resolver: zodResolver(platformEditSchema),
    defaultValues: {
      name: "",
      sticky_ttl: "",
      regex_filters_text: "",
      region_filters_text: "",
      reverse_proxy_miss_action: "RANDOM",
      allocation_policy: "BALANCED",
    },
  });

  useEffect(() => {
    if (!platform) {
      return;
    }
    editForm.reset(platformToEditForm(platform));
  }, [platform, editForm]);

  const invalidatePlatform = async (id: string) => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["platforms"] }),
      queryClient.invalidateQueries({ queryKey: ["platform", id] }),
    ]);
  };

  const updateMutation = useMutation({
    mutationFn: async (formData: PlatformEditForm) => {
      if (!platform) {
        throw new Error("平台不存在或已被删除");
      }

      return updatePlatform(platform.id, {
        name: formData.name.trim(),
        sticky_ttl: formData.sticky_ttl?.trim() || "",
        regex_filters: parseLinesToList(formData.regex_filters_text),
        region_filters: parseLinesToList(formData.region_filters_text, (value) => value.toLowerCase()),
        reverse_proxy_miss_action: formData.reverse_proxy_miss_action,
        allocation_policy: formData.allocation_policy,
      });
    },
    onSuccess: async (updated) => {
      await invalidatePlatform(updated.id);
      editForm.reset(platformToEditForm(updated));
      showToast("success", `平台 ${updated.name} 已更新`);
    },
    onError: (error) => {
      showToast("error", fromApiError(error));
    },
  });

  const resetMutation = useMutation({
    mutationFn: async () => {
      if (!platform) {
        throw new Error("平台不存在或已被删除");
      }
      return resetPlatform(platform.id);
    },
    onSuccess: async (updated) => {
      await invalidatePlatform(updated.id);
      editForm.reset(platformToEditForm(updated));
      showToast("success", `平台 ${updated.name} 已重置为默认配置`);
    },
    onError: (error) => {
      showToast("error", fromApiError(error));
    },
  });

  const rebuildMutation = useMutation({
    mutationFn: async () => {
      if (!platform) {
        throw new Error("平台不存在或已被删除");
      }
      await rebuildPlatform(platform.id);
      return platform;
    },
    onSuccess: (updated) => {
      showToast("success", `平台 ${updated.name} 已完成节点池重建`);
    },
    onError: (error) => {
      showToast("error", fromApiError(error));
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async () => {
      if (!platform) {
        throw new Error("平台不存在或已被删除");
      }
      await deletePlatform(platform.id);
      return platform;
    },
    onSuccess: async (deleted) => {
      await queryClient.invalidateQueries({ queryKey: ["platforms"] });
      showToast("success", `平台 ${deleted.name} 已删除`);
      navigate("/platforms", { replace: true });
    },
    onError: (error) => {
      showToast("error", fromApiError(error));
    },
  });

  const onEditSubmit = editForm.handleSubmit(async (values) => {
    await updateMutation.mutateAsync(values);
  });

  const handleDelete = async () => {
    if (!platform) {
      return;
    }
    const confirmed = window.confirm(t(`确认删除平台 ${platform.name}？该操作不可撤销。`));
    if (!confirmed) {
      return;
    }
    await deleteMutation.mutateAsync();
  };

  const stickyTTL = platform ? formatGoDuration(platform.sticky_ttl, "默认") : "默认";
  const regionCount = platform?.region_filters.length ?? 0;
  const regexCount = platform?.regex_filters.length ?? 0;

  return (
    <section className="platform-page platform-detail-page">
      <header className="module-header">
        <div>
          <h2>平台详情</h2>
          <p className="module-description">调整当前平台策略，并执行维护操作。</p>
        </div>
        <div className="platform-detail-toolbar">
          <Button variant="secondary" size="sm" onClick={() => navigate("/platforms")}>
            <ArrowLeft size={16} />
            返回列表
          </Button>
          <Button variant="secondary" size="sm" onClick={() => platformQuery.refetch()} disabled={!platformId || platformQuery.isFetching}>
            <RefreshCw size={16} className={platformQuery.isFetching ? "spin" : undefined} />
            刷新
          </Button>
        </div>
      </header>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      {!platformId ? (
        <div className="callout callout-error">
          <AlertTriangle size={14} />
          <span>平台 ID 缺失，无法加载详情。</span>
        </div>
      ) : null}

      {platformQuery.isError && !platform ? (
        <div className="callout callout-error">
          <AlertTriangle size={14} />
          <span>{fromApiError(platformQuery.error)}</span>
        </div>
      ) : null}

      {platformQuery.isLoading && !platform ? (
        <Card className="platform-cards-container">
          <p className="muted">正在加载平台详情...</p>
        </Card>
      ) : null}

      {platform ? (
        <>
          <Card className="platform-directory-card platform-detail-header-card">
            <div className="platform-detail-header-main">
              <div>
                <h3>{platform.name}</h3>
                <p>{platform.id}</p>
              </div>
              <div className="platform-detail-header-meta">
                <Badge variant={platform.id === ZERO_UUID ? "warning" : "success"}>
                  {platform.id === ZERO_UUID ? "内置平台" : "自定义平台"}
                </Badge>
                <span>更新于 {formatRelativeTime(platform.updated_at)}</span>
              </div>
            </div>
            <div className="platform-detail-header-footer">
              <div className="platform-tile-facts">
                <span className="platform-fact">
                  <span>区域</span>
                  <strong>{regionCount}</strong>
                </span>
                <span className="platform-fact">
                  <span>正则</span>
                  <strong>{regexCount}</strong>
                </span>
                <span className="platform-fact">
                  <span>租约时长</span>
                  <strong>{stickyTTL}</strong>
                </span>
                <span className="platform-fact">
                  <span>策略</span>
                  <strong>{allocationPolicyLabel[platform.allocation_policy]}</strong>
                </span>
                <span className="platform-fact">
                  <span>未命中策略</span>
                  <strong>{missActionLabel[platform.reverse_proxy_miss_action]}</strong>
                </span>
              </div>
              <Link to={`/nodes?platform_id=${encodeURIComponent(platform.id)}`} className="platform-detail-node-link">
                <Link2 size={14} />
                <span>可路由节点</span>
              </Link>
            </div>
          </Card>

          <Card className="platform-cards-container platform-detail-main-card">
            <div className="platform-detail-tabs" role="tablist" aria-label="平台详情板块">
              {DETAIL_TABS.map((tab) => {
                const selected = activeTab === tab.key;
                return (
                  <button
                    key={tab.key}
                    id={`platform-tab-${tab.key}`}
                    type="button"
                    role="tab"
                    aria-selected={selected}
                    aria-controls={`platform-tabpanel-${tab.key}`}
                    className={`platform-detail-tab ${selected ? "platform-detail-tab-active" : ""}`}
                    title={tab.hint}
                    onClick={() => setActiveTab(tab.key)}
                  >
                    <span>{tab.label}</span>
                  </button>
                );
              })}
            </div>

            {activeTab === "monitor" ? (
              <div
                id="platform-tabpanel-monitor"
                role="tabpanel"
                aria-labelledby="platform-tab-monitor"
                className="platform-detail-panel"
              >
                <PlatformMonitorPanel platform={platform} />
              </div>
            ) : null}

            {activeTab === "config" ? (
              <section
                id="platform-tabpanel-config"
                role="tabpanel"
                aria-labelledby="platform-tab-config"
                className="platform-detail-tabpanel"
              >
                <div className="platform-drawer-section-head">
                  <h4>平台配置</h4>
                  <p>修改过滤策略与路由策略后点击保存。</p>
                </div>

                <form className="form-grid platform-config-form" onSubmit={onEditSubmit}>
                  <div className="field-group">
                    <label className="field-label" htmlFor="detail-edit-name">
                      名称
                    </label>
                    <Input id="detail-edit-name" invalid={Boolean(editForm.formState.errors.name)} {...editForm.register("name")} />
                    {editForm.formState.errors.name?.message ? (
                      <p className="field-error">{editForm.formState.errors.name.message}</p>
                    ) : null}
                  </div>

                  <div className="field-group">
                    <label className="field-label" htmlFor="detail-edit-sticky">
                      租约保持时长
                    </label>
                    <Input
                      id="detail-edit-sticky"
                      placeholder="例如 168h"
                      invalid={Boolean(editForm.formState.errors.sticky_ttl)}
                      {...editForm.register("sticky_ttl")}
                    />
                  </div>

                  <div className="field-group">
                    <label className="field-label" htmlFor="detail-edit-miss-action">
                      反向代理未命中策略
                    </label>
                    <Select id="detail-edit-miss-action" {...editForm.register("reverse_proxy_miss_action")}>
                      {missActions.map((item) => (
                        <option key={item} value={item}>
                          {missActionLabel[item]}
                        </option>
                      ))}
                    </Select>
                  </div>

                  <div className="field-group">
                    <label className="field-label" htmlFor="detail-edit-policy">
                      节点分配策略
                    </label>
                    <Select id="detail-edit-policy" {...editForm.register("allocation_policy")}>
                      {allocationPolicies.map((item) => (
                        <option key={item} value={item}>
                          {allocationPolicyLabel[item]}
                        </option>
                      ))}
                    </Select>
                  </div>

                  <div className="field-group">
                    <label className="field-label" htmlFor="detail-edit-regex">
                      节点名正则过滤规则
                    </label>
                    <Textarea id="detail-edit-regex" rows={6} placeholder="每行一条" {...editForm.register("regex_filters_text")} />
                  </div>

                  <div className="field-group">
                    <label className="field-label" htmlFor="detail-edit-region">
                      地区过滤规则
                    </label>
                    <Textarea
                      id="detail-edit-region"
                      rows={6}
                      placeholder="每行一条，如 hk / us"
                      {...editForm.register("region_filters_text")}
                    />
                  </div>

                  <div className="platform-config-actions">
                    <Button type="submit" disabled={updateMutation.isPending}>
                      {updateMutation.isPending ? "保存中..." : "保存配置"}
                    </Button>
                  </div>
                </form>
              </section>
            ) : null}

            {activeTab === "ops" ? (
              <section
                id="platform-tabpanel-ops"
                role="tabpanel"
                aria-labelledby="platform-tab-ops"
                className="platform-detail-tabpanel platform-ops-section"
              >
                <div className="platform-drawer-section-head">
                  <h4>运维操作</h4>
                  <p>以下操作会直接作用于当前平台，请谨慎执行。</p>
                </div>

                <div className="platform-ops-list">
                  <div className="platform-op-item">
                    <div className="platform-op-copy">
                      <h5>重建路由池</h5>
                      <p className="platform-op-hint">重新整理当前平台可用节点，不会修改配置。</p>
                    </div>
                    <Button variant="secondary" onClick={() => void rebuildMutation.mutateAsync()} disabled={rebuildMutation.isPending}>
                      {rebuildMutation.isPending ? "重建中..." : "重建路由池"}
                    </Button>
                  </div>

                  <div className="platform-op-item">
                    <div className="platform-op-copy">
                      <h5>重置为默认配置</h5>
                      <p className="platform-op-hint">恢复默认设置，并覆盖当前修改。</p>
                    </div>
                    <Button variant="secondary" onClick={() => void resetMutation.mutateAsync()} disabled={resetMutation.isPending}>
                      {resetMutation.isPending ? "重置中..." : "重置为默认配置"}
                    </Button>
                  </div>

                  <div className="platform-op-item">
                    <div className="platform-op-copy">
                      <h5>删除平台</h5>
                      <p className="platform-op-hint">永久删除当前平台及其配置，操作不可撤销。</p>
                    </div>
                    <Button variant="danger" onClick={() => void handleDelete()} disabled={deleteMutation.isPending}>
                      {deleteMutation.isPending ? "删除中..." : "删除平台"}
                    </Button>
                  </div>
                </div>
              </section>
            ) : null}
          </Card>
        </>
      ) : null}
    </section>
  );
}
