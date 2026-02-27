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
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatGoDuration, formatRelativeTime } from "../../lib/time";
import { createPlatform, listPlatforms } from "./api";
import {
  allocationPolicies,
  allocationPolicyLabel,
  emptyAccountBehaviorLabel,
  emptyAccountBehaviors,
  missActionLabel,
  missActions,
} from "./constants";
import { parseHeaderLines, parseLinesToList } from "./formParsers";
import type { Platform } from "./types";

const platformCreateSchema = z.object({
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
      message: "固定账号 Header 列表不能为空",
    });
  }
});

type PlatformCreateForm = z.infer<typeof platformCreateSchema>;

const ZERO_UUID = "00000000-0000-0000-0000-000000000000";
const EMPTY_PLATFORMS: Platform[] = [];
const PAGE_SIZE_OPTIONS = [12, 24, 48, 96] as const;

export function PlatformPage() {
  const { t } = useI18n();
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
      reverse_proxy_empty_account_behavior: "ACCOUNT_HEADER_RULE",
      reverse_proxy_fixed_account_header: "Authorization",
      allocation_policy: "BALANCED",
    },
  });
  const createEmptyAccountBehavior = createForm.watch("reverse_proxy_empty_account_behavior");

  const createMutation = useMutation({
    mutationFn: createPlatform,
    onSuccess: async (created) => {
      await queryClient.invalidateQueries({ queryKey: ["platforms"] });
      setCreateModalOpen(false);
      createForm.reset();
      showToast("success", t("平台 {{name}} 创建成功", { name: created.name }));
      navigate(`/platforms/${created.id}`);
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const onCreateSubmit = createForm.handleSubmit(async (values) => {
    await createMutation.mutateAsync({
      name: values.name.trim(),
      sticky_ttl: values.sticky_ttl?.trim() || undefined,
      regex_filters: parseLinesToList(values.regex_filters_text),
      region_filters: parseLinesToList(values.region_filters_text, (value) => value.toLowerCase()),
      reverse_proxy_miss_action: values.reverse_proxy_miss_action,
      reverse_proxy_empty_account_behavior: values.reverse_proxy_empty_account_behavior,
      reverse_proxy_fixed_account_header: parseHeaderLines(values.reverse_proxy_fixed_account_header).join("\n"),
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
          <h2>{t("平台管理")}</h2>
          <p className="module-description">{t("集中维护平台策略与节点分配规则。")}</p>
        </div>
      </header>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <Card className="platform-list-card platform-directory-card">
        <div className="list-card-header">
          <div>
            <h3>{t("平台列表")}</h3>
            <p>{t("共 {{count}} 个平台", { count: totalPlatforms })}</p>
          </div>
          <div style={{ display: "flex", gap: "0.5rem", alignItems: "center" }}>
            <label className="search-box" htmlFor="platform-search" style={{ maxWidth: 200, margin: 0, gap: 6 }}>
              <Search size={16} />
              <Input
                id="platform-search"
                placeholder={t("搜索平台")}
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
              onClick={() => platformsQuery.refetch()}
              disabled={platformsQuery.isFetching}
            >
              <RefreshCw size={16} className={platformsQuery.isFetching ? "spin" : undefined} />
              {t("刷新")}
            </Button>
          </div>
        </div>
      </Card>

      <Card className="platform-cards-container">
        {platformsQuery.isLoading ? <p className="muted">{t("正在加载平台数据...")}</p> : null}

        {platformsQuery.isError ? (
          <div className="callout callout-error">
            <AlertTriangle size={14} />
            <span>{formatApiErrorMessage(platformsQuery.error, t)}</span>
          </div>
        ) : null}

        {!platformsQuery.isLoading && !platforms.length ? (
          <div className="empty-box">
            <Sparkles size={16} />
            <p>{t("没有匹配的平台")}</p>
          </div>
        ) : null}

        <div className="platform-card-grid">
          {platforms.map((platform) => {
            const regionCount = platform.region_filters.length;
            const regexCount = platform.regex_filters.length;
            const stickyTTL = formatGoDuration(platform.sticky_ttl, t("默认"));

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
                    {platform.id === ZERO_UUID ? t("内置平台") : t("自定义平台")}
                  </Badge>
                </div>
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
                </div>
                <div className="platform-tile-foot">
                  <span className="platform-tile-meta">
                    {t("{{count}} 个可用节点", { count: platform.routable_node_count })}
                  </span>
                  <span className="platform-tile-meta platform-tile-updated">
                    {t("更新于 {{time}}", { time: formatRelativeTime(platform.updated_at) })}
                  </span>
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
              <h3>{t("新建平台")}</h3>
              <Button variant="ghost" size="sm" onClick={() => setCreateModalOpen(false)}>
                {t("关闭")}
              </Button>
            </div>

            <form className="form-grid" onSubmit={onCreateSubmit}>
              <div className="field-group">
                <label className="field-label" htmlFor="create-name">
                  {t("名称")}
                </label>
                <Input id="create-name" invalid={Boolean(createForm.formState.errors.name)} {...createForm.register("name")} />
                {createForm.formState.errors.name?.message ? (
                  <p className="field-error">{t(createForm.formState.errors.name.message)}</p>
                ) : null}
              </div>

              <div className="field-group">
                <label className="field-label" htmlFor="create-sticky">
                  {t("租约保持时长（可选）")}
                </label>
                <Input id="create-sticky" placeholder={t("例如 168h")} {...createForm.register("sticky_ttl")} />
              </div>

              <div className="field-group">
                <label className="field-label" htmlFor="create-miss-action">
                  {t("反向代理未命中策略")}
                </label>
                <Select id="create-miss-action" {...createForm.register("reverse_proxy_miss_action")}>
                  {missActions.map((item) => (
                    <option key={item} value={item}>
                      {t(missActionLabel[item])}
                    </option>
                  ))}
                </Select>
              </div>

              <div className="field-group">
                <label className="field-label" htmlFor="create-policy">
                  {t("节点分配策略")}
                </label>
                <Select id="create-policy" {...createForm.register("allocation_policy")}>
                  {allocationPolicies.map((item) => (
                    <option key={item} value={item}>
                      {t(allocationPolicyLabel[item])}
                    </option>
                  ))}
                </Select>
              </div>

              <div className="field-group">
                <label className="field-label" htmlFor="create-empty-account-behavior">
                  {t("反向代理账号为空行为")}
                </label>
                <Select id="create-empty-account-behavior" {...createForm.register("reverse_proxy_empty_account_behavior")}>
                  {emptyAccountBehaviors.map((item) => (
                    <option key={item} value={item}>
                      {t(emptyAccountBehaviorLabel[item])}
                    </option>
                  ))}
                </Select>
              </div>

              <div className="field-group">
                <label className="field-label" htmlFor="create-fixed-account-header">
                  {t("固定账号 Header 列表（仅固定 Header 模式）")}
                </label>
                <Textarea
                  id="create-fixed-account-header"
                  rows={3}
                  placeholder={t("每行一个，例如 Authorization 或 X-Account-Id")}
                  disabled={createEmptyAccountBehavior !== "FIXED_HEADER"}
                  {...createForm.register("reverse_proxy_fixed_account_header")}
                />
                {createForm.formState.errors.reverse_proxy_fixed_account_header?.message ? (
                  <p className="field-error">{t(createForm.formState.errors.reverse_proxy_fixed_account_header.message)}</p>
                ) : null}
              </div>

              <div className="field-group">
                <label className="field-label" htmlFor="create-regex">
                  {t("节点名正则过滤规则（可选）")}
                </label>
                <Textarea id="create-regex" rows={4} placeholder={t("每行一条")} {...createForm.register("regex_filters_text")} />
              </div>

              <div className="field-group">
                <label className="field-label" htmlFor="create-region">
                  {t("地区过滤规则（可选）")}
                </label>
                <Textarea id="create-region" rows={4} placeholder={t("每行一条，如 hk / us")} {...createForm.register("region_filters_text")} />
              </div>

              <div className="detail-actions">
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
