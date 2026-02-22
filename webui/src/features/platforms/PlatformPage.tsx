import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Plus, RefreshCw, Search, Sparkles } from "lucide-react";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { useNavigate } from "react-router-dom";
import { z } from "zod";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { OffsetPagination } from "../../components/ui/OffsetPagination";
import { Select } from "../../components/ui/Select";
import { Textarea } from "../../components/ui/Textarea";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { ApiError } from "../../lib/api-client";
import { formatGoDuration, formatRelativeTime } from "../../lib/time";
import { createPlatform, listPlatforms } from "./api";
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

const platformCreateSchema = z.object({
  name: z.string().trim().min(1, "平台名称不能为空"),
  sticky_ttl: z.string().optional(),
  regex_filters_text: z.string().optional(),
  region_filters_text: z.string().optional(),
  reverse_proxy_miss_action: z.enum(missActions),
  allocation_policy: z.enum(allocationPolicies),
});

type PlatformCreateForm = z.infer<typeof platformCreateSchema>;

const ZERO_UUID = "00000000-0000-0000-0000-000000000000";
const EMPTY_PLATFORMS: Platform[] = [];
const PAGE_SIZE_OPTIONS = [12, 24, 48, 96] as const;

function parseLinesToList(input: string | undefined): string[] {
  if (!input) {
    return [];
  }

  return input
    .split(/\n/)
    .map((item) => item.trim())
    .filter(Boolean);
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
  const navigate = useNavigate();
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState<number>(24);
  const [createModalOpen, setCreateModalOpen] = useState(false);
  const { toasts, showToast, dismissToast } = useToast();

  const queryClient = useQueryClient();

  const platformsQuery = useQuery({
    queryKey: ["platforms", "page", page, pageSize, search],
    queryFn: () =>
      listPlatforms({
        limit: pageSize,
        offset: page * pageSize,
        keyword: search,
      }),
    refetchInterval: 30_000,
    placeholderData: (prev) => prev,
  });

  const platforms = platformsQuery.data?.items ?? EMPTY_PLATFORMS;

  const totalPlatforms = platformsQuery.data?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(totalPlatforms / pageSize));
  const currentPage = Math.min(page, totalPages - 1);

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

  const createMutation = useMutation({
    mutationFn: createPlatform,
    onSuccess: async (created) => {
      await queryClient.invalidateQueries({ queryKey: ["platforms"] });
      setCreateModalOpen(false);
      createForm.reset();
      showToast("success", `平台 ${created.name} 创建成功`);
      navigate(`/platforms/${created.id}`);
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

  const changePageSize = (next: number) => {
    setPageSize(next);
    setPage(0);
  };

  return (
    <section className="platform-page">
      <header className="module-header">
        <div>
          <h2>平台管理</h2>
          <p className="module-description">集中维护平台策略与节点分配规则。</p>
        </div>
      </header>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <Card className="platform-list-card platform-directory-card">
        <div className="list-card-header">
          <div>
            <h3>平台列表</h3>
            <p>共 {totalPlatforms} 个平台</p>
          </div>
          <div style={{ display: "flex", gap: "0.5rem", alignItems: "center" }}>
            <label className="search-box" htmlFor="platform-search" style={{ maxWidth: 200, margin: 0, gap: 6 }}>
              <Search size={16} />
              <Input
                id="platform-search"
                placeholder="搜索平台"
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
              onClick={() => platformsQuery.refetch()}
              disabled={platformsQuery.isFetching}
            >
              <RefreshCw size={16} className={platformsQuery.isFetching ? "spin" : undefined} />
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

        {!platformsQuery.isLoading && !platforms.length ? (
          <div className="empty-box">
            <Sparkles size={16} />
            <p>没有匹配的平台</p>
          </div>
        ) : null}

        <div className="platform-card-grid">
          {platforms.map((platform) => {
            const regionCount = platform.region_filters.length;
            const regexCount = platform.regex_filters.length;
            const stickyTTL = formatGoDuration(platform.sticky_ttl, "默认");

            return (
              <button
                key={platform.id}
                type="button"
                className="platform-tile"
                onClick={() => navigate(`/platforms/${platform.id}`)}
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
                    <span>租约时长</span>
                    <strong>{stickyTTL}</strong>
                  </span>
                </div>
                <div className="platform-tile-foot">
                  <span className="platform-tile-meta">节点 {platform.routable_node_count}</span>
                  <span className="platform-tile-meta platform-tile-updated">更新于 {formatRelativeTime(platform.updated_at)}</span>
                </div>
              </button>
            );
          })}
        </div>

        <OffsetPagination
          page={currentPage}
          totalPages={totalPages}
          totalItems={totalPlatforms}
          pageSize={pageSize}
          pageSizeOptions={PAGE_SIZE_OPTIONS}
          onPageChange={setPage}
          onPageSizeChange={changePageSize}
        />
      </Card>

      {createModalOpen ? (
        <div className="modal-overlay" role="dialog" aria-modal="true">
          <Card className="modal-card">
            <div className="modal-header">
              <h3>新建平台</h3>
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
                  租约保持时长（可选）
                </label>
                <Input id="create-sticky" placeholder="例如 168h" {...createForm.register("sticky_ttl")} />
              </div>

              <div className="field-group">
                <label className="field-label" htmlFor="create-miss-action">
                  反向代理未命中策略
                </label>
                <Select id="create-miss-action" {...createForm.register("reverse_proxy_miss_action")}>
                  {missActions.map((item) => (
                    <option key={item} value={item}>
                      {missActionLabel[item]}
                    </option>
                  ))}
                </Select>
              </div>

              <div className="field-group">
                <label className="field-label" htmlFor="create-policy">
                  节点分配策略
                </label>
                <Select id="create-policy" {...createForm.register("allocation_policy")}>
                  {allocationPolicies.map((item) => (
                    <option key={item} value={item}>
                      {allocationPolicyLabel[item]}
                    </option>
                  ))}
                </Select>
              </div>

              <div className="field-group">
                <label className="field-label" htmlFor="create-regex">
                  节点名正则过滤规则（可选）
                </label>
                <Textarea id="create-regex" rows={4} placeholder="每行一条" {...createForm.register("regex_filters_text")} />
              </div>

              <div className="field-group">
                <label className="field-label" htmlFor="create-region">
                  地区过滤规则（可选）
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
