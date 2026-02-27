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
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatGoDuration, formatRelativeTime } from "../../lib/time";
import { deletePlatform, getPlatform, rebuildPlatform, resetPlatform, updatePlatform } from "./api";
import {
  allocationPolicies,
  allocationPolicyLabel,
  emptyAccountBehaviorLabel,
  emptyAccountBehaviors,
  missActionLabel,
  missActions,
} from "./constants";
import { parseHeaderLines, parseLinesToList } from "./formParsers";
import { PlatformMonitorPanel } from "./PlatformMonitorPanel";
import type { Platform } from "./types";

const platformEditSchema = z.object({
  name: z.string().trim().min(1, "平台名称不能为空"),
  sticky_ttl: z.string().optional(),
  regex_filters_text: z.string().optional(),
  region_filters_text: z.string().optional(),
  reverse_proxy_miss_action: z.enum(missActions),
  reverse_proxy_empty_account_behavior: z.enum(emptyAccountBehaviors),
  reverse_proxy_fixed_account_header: z.string().optional(),
  allocation_policy: z.enum(allocationPolicies),
}).superRefine((value, ctx) => {
  if (
    value.reverse_proxy_empty_account_behavior === "FIXED_HEADER" &&
    parseHeaderLines(value.reverse_proxy_fixed_account_header).length === 0
  ) {
    ctx.addIssue({
      code: "custom",
      path: ["reverse_proxy_fixed_account_header"],
      message: "用于提取 Account 的 Headers 不能为空",
    });
  }
});

type PlatformEditForm = z.infer<typeof platformEditSchema>;
type PlatformDetailTab = "monitor" | "config" | "ops";

const ZERO_UUID = "00000000-0000-0000-0000-000000000000";
const DETAIL_TABS: Array<{ key: PlatformDetailTab; label: string; hint: string }> = [
  { key: "monitor", label: "监控", hint: "平台运行态趋势和快照" },
  { key: "config", label: "配置", hint: "过滤规则与分配策略" },
  { key: "ops", label: "运维", hint: "重建、重置、删除操作" },
];

function platformToEditForm(platform: Platform): PlatformEditForm {
  const regexFilters = Array.isArray(platform.regex_filters) ? platform.regex_filters : [];
  const regionFilters = Array.isArray(platform.region_filters) ? platform.region_filters : [];

  return {
    name: platform.name,
    sticky_ttl: platform.sticky_ttl,
    regex_filters_text: regexFilters.join("\n"),
    region_filters_text: regionFilters.join("\n"),
    reverse_proxy_miss_action: platform.reverse_proxy_miss_action,
    reverse_proxy_empty_account_behavior: platform.reverse_proxy_empty_account_behavior,
    reverse_proxy_fixed_account_header: platform.reverse_proxy_fixed_account_header,
    allocation_policy: platform.allocation_policy,
  };
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
      reverse_proxy_empty_account_behavior: "ACCOUNT_HEADER_RULE",
      reverse_proxy_fixed_account_header: "Authorization",
      allocation_policy: "BALANCED",
    },
  });
  const detailEmptyAccountBehavior = editForm.watch("reverse_proxy_empty_account_behavior");

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
        reverse_proxy_empty_account_behavior: formData.reverse_proxy_empty_account_behavior,
        reverse_proxy_fixed_account_header: parseHeaderLines(formData.reverse_proxy_fixed_account_header).join("\n"),
        allocation_policy: formData.allocation_policy,
      });
    },
    onSuccess: async (updated) => {
      await invalidatePlatform(updated.id);
      editForm.reset(platformToEditForm(updated));
      showToast("success", t("平台 {{name}} 已更新", { name: updated.name }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
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
      showToast("success", t("平台 {{name}} 已重置为默认配置", { name: updated.name }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
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
      showToast("success", t("平台 {{name}} 已完成节点池重建", { name: updated.name }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
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
      showToast("success", t("平台 {{name}} 已删除", { name: deleted.name }));
      navigate("/platforms", { replace: true });
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const onEditSubmit = editForm.handleSubmit(async (values) => {
    await updateMutation.mutateAsync(values);
  });

  const handleDelete = async () => {
    if (!platform) {
      return;
    }
    const confirmed = window.confirm(t("确认删除平台 {{name}}？该操作不可撤销。", { name: platform.name }));
    if (!confirmed) {
      return;
    }
    await deleteMutation.mutateAsync();
  };

  const stickyTTL = platform ? formatGoDuration(platform.sticky_ttl, t("默认")) : t("默认");
  const regionCount = platform?.region_filters.length ?? 0;
  const regexCount = platform?.regex_filters.length ?? 0;

  return (
    <section className="platform-page platform-detail-page">
      <header className="module-header">
        <div>
          <h2>{t("平台详情")}</h2>
          <p className="module-description">{t("调整当前平台策略，并执行维护操作。")}</p>
        </div>
        <div className="platform-detail-toolbar">
          <Button variant="secondary" size="sm" onClick={() => navigate("/platforms")}>
            <ArrowLeft size={16} />
            {t("返回列表")}
          </Button>
          <Button variant="secondary" size="sm" onClick={() => platformQuery.refetch()} disabled={!platformId || platformQuery.isFetching}>
            <RefreshCw size={16} className={platformQuery.isFetching ? "spin" : undefined} />
            {t("刷新")}
          </Button>
        </div>
      </header>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      {!platformId ? (
        <div className="callout callout-error">
          <AlertTriangle size={14} />
          <span>{t("平台 ID 缺失，无法加载详情。")}</span>
        </div>
      ) : null}

      {platformQuery.isError && !platform ? (
        <div className="callout callout-error">
          <AlertTriangle size={14} />
          <span>{formatApiErrorMessage(platformQuery.error, t)}</span>
        </div>
      ) : null}

      {platformQuery.isLoading && !platform ? (
        <Card className="platform-cards-container">
          <p className="muted">{t("正在加载平台详情...")}</p>
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
                  {platform.id === ZERO_UUID ? t("内置平台") : t("自定义平台")}
                </Badge>
                <span>{t("更新于 {{time}}", { time: formatRelativeTime(platform.updated_at) })}</span>
              </div>
            </div>
            <div className="platform-detail-header-footer">
              <div className="platform-tile-facts">
                <span className="platform-fact">
                  <span>{t("区域")}</span>
                  <strong>{regionCount}</strong>
                </span>
                <span className="platform-fact">
                  <span>{t("正则")}</span>
                  <strong>{regexCount}</strong>
                </span>
                <span className="platform-fact">
                  <span>{t("租约时长")}</span>
                  <strong>{stickyTTL}</strong>
                </span>
                <span className="platform-fact">
                  <span>{t("策略")}</span>
                  <strong>{t(allocationPolicyLabel[platform.allocation_policy])}</strong>
                </span>
                <span className="platform-fact">
                  <span>{t("未命中策略")}</span>
                  <strong>{t(missActionLabel[platform.reverse_proxy_miss_action])}</strong>
                </span>
                <span className="platform-fact">
                  <span>{t("空账号行为")}</span>
                  <strong>{t(emptyAccountBehaviorLabel[platform.reverse_proxy_empty_account_behavior])}</strong>
                </span>
              </div>
              <Link to={`/nodes?platform_id=${encodeURIComponent(platform.id)}`} className="platform-detail-node-link">
                <Link2 size={14} />
                <span>{t("可路由节点")}</span>
              </Link>
            </div>
          </Card>

          <Card className="platform-cards-container platform-detail-main-card">
            <div className="platform-detail-tabs" role="tablist" aria-label={t("平台详情板块")}>
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
                    title={t(tab.hint)}
                    onClick={() => setActiveTab(tab.key)}
                  >
                    <span>{t(tab.label)}</span>
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
                  <h4>{t("平台配置")}</h4>
                  <p>{t("修改过滤策略与路由策略后点击保存。")}</p>
                </div>

                <form className="form-grid platform-config-form" onSubmit={onEditSubmit}>
                  <div className="field-group">
                    <label className="field-label" htmlFor="detail-edit-name">
                      {t("名称")}
                    </label>
                    <Input id="detail-edit-name" invalid={Boolean(editForm.formState.errors.name)} {...editForm.register("name")} />
                    {editForm.formState.errors.name?.message ? (
                      <p className="field-error">{t(editForm.formState.errors.name.message)}</p>
                    ) : null}
                  </div>

                  <div className="field-group">
                    <label className="field-label" htmlFor="detail-edit-sticky">
                      {t("租约保持时长")}
                    </label>
                    <Input
                      id="detail-edit-sticky"
                      placeholder={t("例如 168h")}
                      invalid={Boolean(editForm.formState.errors.sticky_ttl)}
                      {...editForm.register("sticky_ttl")}
                    />
                  </div>

                  <div className="field-group">
                    <label className="field-label" htmlFor="detail-edit-miss-action">
                      {t("反向代理未命中策略")}
                    </label>
                    <Select id="detail-edit-miss-action" {...editForm.register("reverse_proxy_miss_action")}>
                      {missActions.map((item) => (
                        <option key={item} value={item}>
                          {t(missActionLabel[item])}
                        </option>
                      ))}
                    </Select>
                  </div>

                  <div className="field-group">
                    <label className="field-label" htmlFor="detail-edit-policy">
                      {t("节点分配策略")}
                    </label>
                    <Select id="detail-edit-policy" {...editForm.register("allocation_policy")}>
                      {allocationPolicies.map((item) => (
                        <option key={item} value={item}>
                          {t(allocationPolicyLabel[item])}
                        </option>
                      ))}
                    </Select>
                  </div>

                  <div className="field-group">
                    <label className="field-label" htmlFor="detail-edit-empty-account-behavior">
                      {t("反向代理账号为空行为")}
                    </label>
                    <Select id="detail-edit-empty-account-behavior" {...editForm.register("reverse_proxy_empty_account_behavior")}>
                      {emptyAccountBehaviors.map((item) => (
                        <option key={item} value={item}>
                          {t(emptyAccountBehaviorLabel[item])}
                        </option>
                      ))}
                    </Select>
                  </div>

                  <div
                    className={`account-headers-collapse ${detailEmptyAccountBehavior === "FIXED_HEADER" ? "account-headers-collapse-open" : ""}`}
                    aria-hidden={detailEmptyAccountBehavior !== "FIXED_HEADER"}
                  >
                    <div className="field-group">
                      <label className="field-label" htmlFor="detail-edit-fixed-account-header">
                        {t("用于提取 Account 的 Headers（每行一个）")}
                      </label>
                      <Textarea
                        id="detail-edit-fixed-account-header"
                        rows={4}
                        placeholder={t("每行一个，例如 Authorization 或 X-Account-Id")}
                        {...editForm.register("reverse_proxy_fixed_account_header")}
                      />
                      {editForm.formState.errors.reverse_proxy_fixed_account_header?.message ? (
                        <p className="field-error">{t(editForm.formState.errors.reverse_proxy_fixed_account_header.message)}</p>
                      ) : null}
                    </div>
                  </div>

                  <div className="field-group">
                    <label className="field-label" htmlFor="detail-edit-regex">
                      {t("节点名正则过滤规则")}
                    </label>
                    <Textarea id="detail-edit-regex" rows={6} placeholder={t("每行一条")} {...editForm.register("regex_filters_text")} />
                  </div>

                  <div className="field-group">
                    <label className="field-label" htmlFor="detail-edit-region">
                      {t("地区过滤规则")}
                    </label>
                    <Textarea
                      id="detail-edit-region"
                      rows={6}
                      placeholder={t("每行一条，如 hk / us")}
                      {...editForm.register("region_filters_text")}
                    />
                  </div>

                  <div className="platform-config-actions">
                    <Button type="submit" disabled={updateMutation.isPending}>
                      {updateMutation.isPending ? t("保存中...") : t("保存配置")}
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
                  <h4>{t("运维操作")}</h4>
                  <p>{t("以下操作会直接作用于当前平台，请谨慎执行。")}</p>
                </div>

                <div className="platform-ops-list">
                  <div className="platform-op-item">
                    <div className="platform-op-copy">
                      <h5>{t("重建路由池")}</h5>
                      <p className="platform-op-hint">{t("重新整理当前平台可用节点，不会修改配置。")}</p>
                    </div>
                    <Button variant="secondary" onClick={() => void rebuildMutation.mutateAsync()} disabled={rebuildMutation.isPending}>
                      {rebuildMutation.isPending ? t("重建中...") : t("重建路由池")}
                    </Button>
                  </div>

                  <div className="platform-op-item">
                    <div className="platform-op-copy">
                      <h5>{t("重置为默认配置")}</h5>
                      <p className="platform-op-hint">{t("恢复默认设置，并覆盖当前修改。")}</p>
                    </div>
                    <Button variant="secondary" onClick={() => void resetMutation.mutateAsync()} disabled={resetMutation.isPending}>
                      {resetMutation.isPending ? t("重置中...") : t("重置为默认配置")}
                    </Button>
                  </div>

                  <div className="platform-op-item">
                    <div className="platform-op-copy">
                      <h5>{t("删除平台")}</h5>
                      <p className="platform-op-hint">{t("永久删除当前平台及其配置，操作不可撤销。")}</p>
                    </div>
                    <Button variant="danger" onClick={() => void handleDelete()} disabled={deleteMutation.isPending}>
                      {deleteMutation.isPending ? t("删除中...") : t("删除平台")}
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
