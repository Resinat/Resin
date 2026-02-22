import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Plus, RefreshCw, Search, Sparkles, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useForm } from "react-hook-form";
import { z } from "zod";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { Textarea } from "../../components/ui/Textarea";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { ApiError } from "../../lib/api-client";
import { formatGoDuration, formatRelativeTime } from "../../lib/time";
import {
  createPlatform,
  deletePlatform,
  listPlatforms,
  rebuildPlatform,
  resetPlatform,
  updatePlatform,
} from "./api";
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
  PREFER_LOW_LATENCY: "低延迟",
  PREFER_IDLE_IP: "空闲优先",
};

const missActionLabel: Record<PlatformMissAction, string> = {
  RANDOM: "随机",
  REJECT: "拒绝",
};

const platformCreateSchema = z.object({
  name: z.string().trim().min(1, "平台名称不能为空"),
  sticky_ttl: z.string().optional(),
  regex_filters_text: z.string().optional(),
  region_filters_text: z.string().optional(),
  reverse_proxy_miss_action: z.enum(missActions),
  allocation_policy: z.enum(allocationPolicies),
});

const platformEditSchema = platformCreateSchema;

type PlatformCreateForm = z.infer<typeof platformCreateSchema>;
type PlatformEditForm = z.infer<typeof platformEditSchema>;
const ZERO_UUID = "00000000-0000-0000-0000-000000000000";
const EMPTY_PLATFORMS: Platform[] = [];

function parseLinesToList(input: string | undefined): string[] {
  if (!input) {
    return [];
  }

  return input
    .split(/\n/)
    .map((item) => item.trim())
    .filter(Boolean);
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

export function PlatformPage() {
  const [search, setSearch] = useState("");
  const [selectedPlatformId, setSelectedPlatformId] = useState("");
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [createModalOpen, setCreateModalOpen] = useState(false);
  const { toasts, showToast, dismissToast } = useToast();

  const queryClient = useQueryClient();

  const platformsQuery = useQuery({
    queryKey: ["platforms"],
    queryFn: listPlatforms,
    refetchInterval: 30_000,
  });

  const platforms = platformsQuery.data ?? EMPTY_PLATFORMS;

  const visiblePlatforms = useMemo(() => {
    const keyword = search.trim().toLowerCase();
    const filtered = keyword
      ? platforms.filter((platform) => {
        return (
          platform.name.toLowerCase().includes(keyword) ||
          platform.id.toLowerCase().includes(keyword) ||
          platform.region_filters.some((region) => region.toLowerCase().includes(keyword))
        );
      })
      : platforms;

    return [...filtered].sort((a, b) => {
      const aBuiltin = a.id === ZERO_UUID ? 0 : 1;
      const bBuiltin = b.id === ZERO_UUID ? 0 : 1;
      return aBuiltin - bBuiltin;
    });
  }, [platforms, search]);

  const selectedPlatform = useMemo(() => {
    if (!selectedPlatformId) {
      return null;
    }
    return platforms.find((item) => item.id === selectedPlatformId) ?? null;
  }, [platforms, selectedPlatformId]);

  const createForm = useForm<PlatformCreateForm>({
    resolver: zodResolver(platformCreateSchema),
    defaultValues: {
      name: "",
      sticky_ttl: "",
      regex_filters_text: "",
      region_filters_text: "",
      reverse_proxy_miss_action: "RANDOM",
      allocation_policy: "BALANCED",
    },
  });

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
    if (!selectedPlatform) {
      return;
    }
    editForm.reset(platformToEditForm(selectedPlatform));
  }, [selectedPlatform, editForm]);

  useEffect(() => {
    if (!drawerOpen) {
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
  }, [drawerOpen]);

  const invalidatePlatforms = async () => {
    await queryClient.invalidateQueries({ queryKey: ["platforms"] });
  };

  const createMutation = useMutation({
    mutationFn: createPlatform,
    onSuccess: async (created) => {
      await invalidatePlatforms();
      setCreateModalOpen(false);
      createForm.reset();
      showToast("success", `平台 ${created.name} 创建成功`);
    },
    onError: (error) => {
      showToast("error", fromApiError(error));
    },
  });

  const updateMutation = useMutation({
    mutationFn: async (formData: PlatformEditForm) => {
      if (!selectedPlatform) {
        throw new Error("请选择要编辑的平台");
      }

      return updatePlatform(selectedPlatform.id, {
        name: formData.name.trim(),
        sticky_ttl: formData.sticky_ttl?.trim() || "",
        regex_filters: parseLinesToList(formData.regex_filters_text),
        region_filters: parseLinesToList(formData.region_filters_text),
        reverse_proxy_miss_action: formData.reverse_proxy_miss_action,
        allocation_policy: formData.allocation_policy,
      });
    },
    onSuccess: async (updated) => {
      await invalidatePlatforms();
      setSelectedPlatformId(updated.id);
      showToast("success", `平台 ${updated.name} 已更新`);
    },
    onError: (error) => {
      showToast("error", fromApiError(error));
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async (platform: Platform) => {
      await deletePlatform(platform.id);
      return platform;
    },
    onSuccess: async (deleted) => {
      await invalidatePlatforms();
      if (selectedPlatformId === deleted.id) {
        setDrawerOpen(false);
        setSelectedPlatformId("");
      }
      showToast("success", `平台 ${deleted.name} 已删除`);
    },
    onError: (error) => {
      showToast("error", fromApiError(error));
    },
  });

  const resetMutation = useMutation({
    mutationFn: async (platform: Platform) => {
      return resetPlatform(platform.id);
    },
    onSuccess: async (updated) => {
      await invalidatePlatforms();
      setSelectedPlatformId(updated.id);
      showToast("success", `平台 ${updated.name} 已重置为默认配置`);
    },
    onError: (error) => {
      showToast("error", fromApiError(error));
    },
  });

  const rebuildMutation = useMutation({
    mutationFn: async (platform: Platform) => {
      await rebuildPlatform(platform.id);
      return platform;
    },
    onSuccess: (platform) => {
      showToast("success", `平台 ${platform.name} 已完成路由视图重建`);
    },
    onError: (error) => {
      showToast("error", fromApiError(error));
    },
  });

  const onCreateSubmit = createForm.handleSubmit(async (values) => {
    await createMutation.mutateAsync({
      name: values.name.trim(),
      sticky_ttl: values.sticky_ttl?.trim() || undefined,
      regex_filters: parseLinesToList(values.regex_filters_text),
      region_filters: parseLinesToList(values.region_filters_text),
      reverse_proxy_miss_action: values.reverse_proxy_miss_action,
      allocation_policy: values.allocation_policy,
    });
  });

  const onEditSubmit = editForm.handleSubmit(async (values) => {
    await updateMutation.mutateAsync(values);
  });

  const handleDelete = async (platform: Platform) => {
    const confirmed = window.confirm(`确认删除平台 ${platform.name}？该操作不可撤销。`);
    if (!confirmed) {
      return;
    }
    await deleteMutation.mutateAsync(platform);
  };

  const openDrawer = (platform: Platform) => {
    setSelectedPlatformId(platform.id);
    setDrawerOpen(true);
  };

  return (
    <section className="platform-page">
      <header className="module-header">
        <div>
          <h2>Platform 管理</h2>
          <p className="module-description">管理平台的过滤策略、分配策略，并执行重建/重置等运维动作。</p>
        </div>
      </header>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <Card className="platform-list-card platform-directory-card">
        <div className="list-card-header">
          <div>
            <h3>平台列表</h3>
            <p>共 {platforms.length} 个平台</p>
          </div>
          <div style={{ display: "flex", gap: "0.5rem", alignItems: "center" }}>
            <label className="search-box" htmlFor="platform-search" style={{ maxWidth: 180, margin: 0, gap: 6 }}>
              <Search size={14} />
              <Input
                id="platform-search"
                placeholder="搜索平台"
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                style={{ padding: "6px 10px", borderRadius: 8 }}
              />
            </label>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setCreateModalOpen(true)}
            >
              <Plus size={14} />
              新建
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => platformsQuery.refetch()}
              disabled={platformsQuery.isFetching}
            >
              <RefreshCw size={14} className={platformsQuery.isFetching ? "spin" : undefined} />
              刷新
            </Button>
          </div>
        </div>
      </Card>

      <Card className="platform-cards-container">
        {platformsQuery.isLoading ? <p className="muted">正在加载平台数据...</p> : null}

        {platformsQuery.isError ? (
          <div className="callout callout-error">
            <AlertTriangle size={14} />
            <span>{fromApiError(platformsQuery.error)}</span>
          </div>
        ) : null}

        {!platformsQuery.isLoading && !visiblePlatforms.length ? (
          <div className="empty-box">
            <Sparkles size={16} />
            <p>没有匹配的平台</p>
          </div>
        ) : null}

        <div className="platform-card-grid">
          {visiblePlatforms.map((platform) => {
            const isActive = drawerOpen && platform.id === selectedPlatformId;
            const regionCount = platform.region_filters.length;
            const regexCount = platform.regex_filters.length;
            const stickyTTL = formatGoDuration(platform.sticky_ttl, "默认");

            return (
              <button
                key={platform.id}
                type="button"
                className={`platform-tile ${isActive ? "platform-tile-active" : ""}`}
                onClick={() => openDrawer(platform)}
              >
                <div className="platform-tile-head">
                  <p>{platform.name}</p>
                  <Badge variant={platform.id === ZERO_UUID ? "warning" : "success"}>
                    {platform.id === ZERO_UUID ? "内置平台" : "自定义平台"}
                  </Badge>
                </div>
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
                    <span>TTL</span>
                    <strong>{stickyTTL}</strong>
                  </span>
                </div>
                <div className="platform-tile-foot">
                  <span>
                    {allocationPolicyLabel[platform.allocation_policy]} · Miss{" "}
                    {missActionLabel[platform.reverse_proxy_miss_action]}
                  </span>
                  <span className="platform-tile-updated">更新于 {formatRelativeTime(platform.updated_at)}</span>
                </div>
              </button>
            );
          })}
        </div>
      </Card>

      {drawerOpen && selectedPlatform ? (
        <div
          className="drawer-overlay"
          role="dialog"
          aria-modal="true"
          aria-label={`编辑平台 ${selectedPlatform.name}`}
          onClick={() => setDrawerOpen(false)}
        >
          <Card className="drawer-panel" onClick={(event) => event.stopPropagation()}>
            <div className="drawer-header">
              <div>
                <h3>{selectedPlatform.name}</h3>
                <p>{selectedPlatform.id}</p>
              </div>
              <div className="drawer-header-actions">
                <Badge variant={selectedPlatform.id === ZERO_UUID ? "warning" : "success"}>
                  {selectedPlatform.id === ZERO_UUID ? "内置平台" : "自定义平台"}
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
              <PlatformMonitorPanel platform={selectedPlatform} />

              <section className="platform-drawer-section">
                <div className="platform-drawer-section-head">
                  <h4>平台配置</h4>
                  <p>修改平台配置后，点击右下角保存。</p>
                </div>

                <form className="form-grid platform-config-form" onSubmit={onEditSubmit}>
                  <div className="field-group">
                    <label className="field-label" htmlFor="edit-name">
                      名称
                    </label>
                    <Input id="edit-name" invalid={Boolean(editForm.formState.errors.name)} {...editForm.register("name")} />
                    {editForm.formState.errors.name?.message ? (
                      <p className="field-error">{editForm.formState.errors.name.message}</p>
                    ) : null}
                  </div>

                  <div className="field-group">
                    <label className="field-label" htmlFor="edit-sticky">
                      Sticky TTL
                    </label>
                    <Input
                      id="edit-sticky"
                      placeholder="例如 168h"
                      invalid={Boolean(editForm.formState.errors.sticky_ttl)}
                      {...editForm.register("sticky_ttl")}
                    />
                  </div>

                  <div className="field-group">
                    <label className="field-label" htmlFor="edit-miss-action">
                      Reverse Proxy Miss Action
                    </label>
                    <Select id="edit-miss-action" {...editForm.register("reverse_proxy_miss_action")}>
                      {missActions.map((item) => (
                        <option key={item} value={item}>
                          {item}
                        </option>
                      ))}
                    </Select>
                  </div>

                  <div className="field-group">
                    <label className="field-label" htmlFor="edit-policy">
                      Allocation Policy
                    </label>
                    <Select id="edit-policy" {...editForm.register("allocation_policy")}>
                      {allocationPolicies.map((item) => (
                        <option key={item} value={item}>
                          {item}
                        </option>
                      ))}
                    </Select>
                  </div>

                  <div className="field-group">
                    <label className="field-label" htmlFor="edit-regex">
                      Regex Filters
                    </label>
                    <Textarea id="edit-regex" rows={4} placeholder="每行一条" {...editForm.register("regex_filters_text")} />
                  </div>

                  <div className="field-group">
                    <label className="field-label" htmlFor="edit-region">
                      Region Filters
                    </label>
                    <Textarea id="edit-region" rows={4} placeholder="每行一条，如 hk / us" {...editForm.register("region_filters_text")} />
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
                  <p>以下操作会直接作用于当前平台，请谨慎执行。</p>
                </div>

                <div className="platform-ops-list">
                  <div className="platform-op-item">
                    <div className="platform-op-copy">
                      <h5>重建路由池</h5>
                      <p className="platform-op-hint">重新构建当前平台的路由视图与可用节点池，不改变配置项。</p>
                    </div>
                    <Button
                      variant="secondary"
                      onClick={() => void rebuildMutation.mutateAsync(selectedPlatform)}
                      disabled={rebuildMutation.isPending}
                    >
                      {rebuildMutation.isPending ? "重建中..." : "重建路由池"}
                    </Button>
                  </div>

                  <div className="platform-op-item">
                    <div className="platform-op-copy">
                      <h5>重置为默认配置</h5>
                      <p className="platform-op-hint">恢复平台默认策略，并覆盖当前自定义配置。</p>
                    </div>
                    <Button
                      variant="secondary"
                      onClick={() => void resetMutation.mutateAsync(selectedPlatform)}
                      disabled={resetMutation.isPending}
                    >
                      {resetMutation.isPending ? "重置中..." : "重置为默认配置"}
                    </Button>
                  </div>

                  <div className="platform-op-item">
                    <div className="platform-op-copy">
                      <h5>删除平台</h5>
                      <p className="platform-op-hint">永久删除当前平台及其配置，操作不可撤销。</p>
                    </div>
                    <Button
                      variant="danger"
                      onClick={() => void handleDelete(selectedPlatform)}
                      disabled={deleteMutation.isPending}
                    >
                      {deleteMutation.isPending ? "删除中..." : "删除平台"}
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
              <h3>新建 Platform</h3>
              <Button variant="ghost" size="sm" onClick={() => setCreateModalOpen(false)}>
                关闭
              </Button>
            </div>

            <form className="form-grid" onSubmit={onCreateSubmit}>
              <div className="field-group">
                <label className="field-label" htmlFor="create-name">
                  名称
                </label>
                <Input id="create-name" invalid={Boolean(createForm.formState.errors.name)} {...createForm.register("name")} />
                {createForm.formState.errors.name?.message ? (
                  <p className="field-error">{createForm.formState.errors.name.message}</p>
                ) : null}
              </div>

              <div className="field-group">
                <label className="field-label" htmlFor="create-sticky">
                  Sticky TTL（可选）
                </label>
                <Input id="create-sticky" placeholder="例如 168h" {...createForm.register("sticky_ttl")} />
              </div>

              <div className="field-group">
                <label className="field-label" htmlFor="create-miss-action">
                  Reverse Proxy Miss Action
                </label>
                <Select id="create-miss-action" {...createForm.register("reverse_proxy_miss_action")}>
                  {missActions.map((item) => (
                    <option key={item} value={item}>
                      {item}
                    </option>
                  ))}
                </Select>
              </div>

              <div className="field-group">
                <label className="field-label" htmlFor="create-policy">
                  Allocation Policy
                </label>
                <Select id="create-policy" {...createForm.register("allocation_policy")}>
                  {allocationPolicies.map((item) => (
                    <option key={item} value={item}>
                      {item}
                    </option>
                  ))}
                </Select>
              </div>

              <div className="field-group">
                <label className="field-label" htmlFor="create-regex">
                  Regex Filters（可选）
                </label>
                <Textarea id="create-regex" rows={4} placeholder="每行一条" {...createForm.register("regex_filters_text")} />
              </div>

              <div className="field-group">
                <label className="field-label" htmlFor="create-region">
                  Region Filters（可选）
                </label>
                <Textarea id="create-region" rows={4} placeholder="每行一条，如 hk / us" {...createForm.register("region_filters_text")} />
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
