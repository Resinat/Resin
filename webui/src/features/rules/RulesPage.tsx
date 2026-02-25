import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createColumnHelper } from "@tanstack/react-table";
import { AlertTriangle, Bug, Pencil, Plus, RefreshCw, Search, Sparkles, Trash2, Wand2, X } from "lucide-react";
import { type FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { DataTable } from "../../components/ui/DataTable";
import { Input } from "../../components/ui/Input";
import { Textarea } from "../../components/ui/Textarea";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { deleteRule, listRules, resolveRule, upsertRule } from "./api";
import type { ResolveResult, Rule } from "./types";

const EMPTY_RULES: Rule[] = [];

function parseHeaderList(raw: string): string[] {
  return raw
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function getBadgeStyle(text: string): React.CSSProperties {
  let hash = 0;
  for (let i = 0; i < text.length; i++) {
    hash = text.charCodeAt(i) + ((hash << 5) - hash);
  }
  const hue = Math.abs(hash) % 360;
  return {
    color: `hsl(${hue}, 80%, 35%)`,
    backgroundColor: `hsla(${hue}, 80%, 45%, 0.14)`,
  };
}

function RuleHeadersPreview({ rule }: { rule: Rule }) {
  if (!rule.headers.length) {
    return <span className="muted">-</span>;
  }

  const displayHeaders = rule.headers.slice(0, 20);
  const extraCount = rule.headers.length - 20;

  return (
    <div style={{ display: "flex", gap: "4px", flexWrap: "wrap" }}>
      {displayHeaders.map((header) => (
        <Badge key={header} style={getBadgeStyle(header)}>
          {header}
        </Badge>
      ))}
      {extraCount > 0 && <Badge variant="neutral">+{extraCount}</Badge>}
    </div>
  );
}

function isFallbackRule(rule: Rule): boolean {
  return rule.url_prefix === "*";
}

export function RulesPage() {
  const { t } = useI18n();
  const [search, setSearch] = useState("");
  const [selectedPrefix, setSelectedPrefix] = useState("");
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [createModalOpen, setCreateModalOpen] = useState(false);
  const [formPrefix, setFormPrefix] = useState("");
  const [formHeadersRaw, setFormHeadersRaw] = useState("");
  const [createPrefix, setCreatePrefix] = useState("");
  const [createHeadersRaw, setCreateHeadersRaw] = useState("");
  const [resolveModalOpen, setResolveModalOpen] = useState(false);
  const [resolveURL, setResolveURL] = useState("");
  const [resolveOutput, setResolveOutput] = useState<ResolveResult | null>(null);
  const { toasts, showToast, dismissToast } = useToast();

  const queryClient = useQueryClient();

  const rulesQuery = useQuery({
    queryKey: ["header-rules", search],
    queryFn: () => listRules(search),
    refetchInterval: 30_000,
  });

  const rules = rulesQuery.data ?? EMPTY_RULES;

  const selectedRule = useMemo(() => {
    if (!selectedPrefix) {
      return null;
    }
    return rules.find((item) => item.url_prefix === selectedPrefix) ?? null;
  }, [rules, selectedPrefix]);

  const syncFormFromRule = useCallback((rule: Rule) => {
    setFormPrefix(rule.url_prefix);
    setFormHeadersRaw(rule.headers.join("\n"));
    setSelectedPrefix(rule.url_prefix);
  }, []);

  const openDrawerForRule = useCallback((rule: Rule) => {
    syncFormFromRule(rule);
    setDrawerOpen(true);
  }, [syncFormFromRule]);

  const invalidateRules = async () => {
    await queryClient.invalidateQueries({ queryKey: ["header-rules"] });
  };

  const createMutation = useMutation({
    mutationFn: async () => {
      const prefix = createPrefix.trim();
      const headers = parseHeaderList(createHeadersRaw);
      if (!prefix) {
        throw new Error("地址前缀不能为空");
      }
      if (!headers.length) {
        throw new Error("请求头不能为空");
      }
      return upsertRule(prefix, headers);
    },
    onSuccess: async (rule) => {
      await invalidateRules();
      setCreateModalOpen(false);
      setCreatePrefix("");
      setCreateHeadersRaw("");
      showToast("success", t("规则 {{prefix}} 已创建", { prefix: rule.url_prefix }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const updateMutation = useMutation({
    mutationFn: async () => {
      const prefix = formPrefix.trim();
      const headers = parseHeaderList(formHeadersRaw);
      if (!prefix) {
        throw new Error("地址前缀不能为空");
      }
      if (!headers.length) {
        throw new Error("请求头不能为空");
      }
      return upsertRule(prefix, headers);
    },
    onSuccess: async (rule) => {
      await invalidateRules();
      syncFormFromRule(rule);
      showToast("success", t("规则 {{prefix}} 已保存", { prefix: rule.url_prefix }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async (prefix: string) => {
      await deleteRule(prefix);
      return prefix;
    },
    onSuccess: async (prefix) => {
      await invalidateRules();
      if (selectedPrefix === prefix) {
        setSelectedPrefix("");
        setDrawerOpen(false);
      }
      showToast("success", t("规则 {{prefix}} 已删除", { prefix }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });
  const deleteRuleMutateAsync = deleteMutation.mutateAsync;
  const isDeletePending = deleteMutation.isPending;

  const resolveMutation = useMutation({
    mutationFn: async () => {
      const targetURL = resolveURL.trim();
      if (!targetURL) {
        throw new Error("请输入 URL");
      }
      return resolveRule(targetURL);
    },
    onSuccess: (result) => {
      setResolveOutput(result);
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const handleDelete = useCallback(async (rule: Rule) => {
    if (isFallbackRule(rule)) {
      showToast("error", '兜底规则 "*" 不允许删除');
      return;
    }
    const confirmed = window.confirm(t("确认删除规则 {{prefix}} 吗？", { prefix: rule.url_prefix }));
    if (!confirmed) {
      return;
    }
    await deleteRuleMutateAsync(rule.url_prefix);
  }, [deleteRuleMutateAsync, showToast, t]);

  const handleUpdateSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    void updateMutation.mutateAsync();
  };

  const handleCreateSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    void createMutation.mutateAsync();
  };

  useEffect(() => {
    if (!drawerOpen && !resolveModalOpen && !createModalOpen) {
      return;
    }

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") {
        return;
      }
      if (createModalOpen) {
        setCreateModalOpen(false);
        return;
      }
      if (resolveModalOpen) {
        setResolveModalOpen(false);
        return;
      }
      setDrawerOpen(false);
    };

    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [createModalOpen, drawerOpen, resolveModalOpen]);

  const col = useMemo(() => createColumnHelper<Rule>(), []);

  const ruleColumns = useMemo(
    () => [
      col.accessor("url_prefix", {
        header: t("URL 前缀"),
        cell: (info) => <span title={info.getValue()}>{info.getValue()}</span>,
      }),
      col.display({
        id: "headers",
        header: t("请求头"),
        cell: (info) => <RuleHeadersPreview rule={info.row.original} />,
      }),
      col.display({
        id: "actions",
        header: t("操作"),
        cell: (info) => {
          const rule = info.row.original;
          return (
            <div className="subscriptions-row-actions" onClick={(event) => event.stopPropagation()}>
              <Button size="sm" variant="ghost" onClick={() => openDrawerForRule(rule)} title={t("编辑")}>
                <Pencil size={14} />
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => void handleDelete(rule)}
                disabled={isDeletePending || isFallbackRule(rule)}
                title={isFallbackRule(rule) ? t('兜底规则 "*" 不可删除') : t("删除")}
                style={{ color: "var(--delete-btn-color, #c27070)" }}
              >
                <Trash2 size={14} />
              </Button>
            </div>
          );
        },
      }),
    ],
    [col, handleDelete, isDeletePending, openDrawerForRule, t]
  );

  return (
    <section className="rules-page">
      <header className="module-header">
        <div>
          <h2>{t("请求头规则")}</h2>
          <p className="module-description">{t("为不同地址设置请求头规则，并先测试后应用。")}</p>
        </div>
      </header>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <Card className="platform-list-card platform-directory-card rules-list-card">
        <div className="list-card-header">
          <div>
            <h3>{t("规则列表")}</h3>
            <p>{t("共 {{count}} 条", { count: rules.length })}</p>
          </div>
          <div style={{ display: "flex", gap: "0.5rem", alignItems: "center" }}>
            <label className="search-box" htmlFor="rules-search" style={{ maxWidth: 200, margin: 0, gap: 6 }}>
              <Search size={16} />
              <Input
                id="rules-search"
                placeholder={t("搜索规则")}
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                style={{ padding: "6px 10px", borderRadius: 8 }}
              />
            </label>
            <Button variant="secondary" size="sm" onClick={() => setCreateModalOpen(true)}>
              <Plus size={16} />
              {t("新建")}
            </Button>
            <Button variant="secondary" size="sm" onClick={() => setResolveModalOpen(true)}>
              <Bug size={16} />
              {t("调试")}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => void rulesQuery.refetch()}
              disabled={rulesQuery.isFetching}
            >
              <RefreshCw size={16} className={rulesQuery.isFetching ? "spin" : undefined} />
              {t("刷新")}
            </Button>
          </div>
        </div>
      </Card>

      <Card className="platform-cards-container subscriptions-table-card rules-table-card">
        {rulesQuery.isLoading ? <p className="muted">{t("正在加载规则...")}</p> : null}

        {rulesQuery.isError ? (
          <div className="callout callout-error">
            <AlertTriangle size={14} />
            <span>{formatApiErrorMessage(rulesQuery.error, t)}</span>
          </div>
        ) : null}

        {!rulesQuery.isLoading && !rules.length ? (
          <div className="empty-box">
            <Sparkles size={16} />
            <p>{t("没有匹配规则")}</p>
          </div>
        ) : null}

        {rules.length ? (
          <DataTable
            data={rules}
            columns={ruleColumns}
            onRowClick={openDrawerForRule}
            getRowId={(r) => r.url_prefix}
            className="data-table-rules"
          />
        ) : null}
      </Card>

      {drawerOpen ? (
        <div className="drawer-overlay" role="dialog" aria-modal="true" aria-label={t("规则编辑抽屉")} onClick={() => setDrawerOpen(false)}>
          <Card className="drawer-panel" onClick={(event) => event.stopPropagation()}>
            <div className="drawer-header">
              <div>
                <h3>{selectedRule?.url_prefix || t("规则编辑")}</h3>
                <p>{t("编辑当前规则")}</p>
              </div>
              <div className="drawer-header-actions">
                <Button variant="ghost" size="sm" onClick={() => setDrawerOpen(false)}>
                  <X size={16} />
                </Button>
              </div>
            </div>

            <div className="platform-drawer-layout">
              <section className="platform-drawer-section">
                <div className="platform-drawer-section-head">
                  <h4>{t("规则编辑")}</h4>
                  <p>{t("修改地址前缀和请求头后保存。")}</p>
                </div>

                <form className="form-grid single-column" onSubmit={handleUpdateSubmit}>
                  <div className="field-group">
                    <label className="field-label" htmlFor="rule-prefix">
                      {t("地址前缀")}
                    </label>
                    <Input
                      id="rule-prefix"
                      placeholder={t("例如 api.example.com/v1")}
                      value={formPrefix}
                      onChange={(event) => setFormPrefix(event.target.value)}
                    />
                  </div>

                  <div className="field-group">
                    <label className="field-label" htmlFor="rule-headers">
                      {t("请求头")}
                    </label>
                    <Textarea
                      id="rule-headers"
                      rows={5}
                      placeholder={t("每行一个 header，例如 Authorization")}
                      value={formHeadersRaw}
                      onChange={(event) => setFormHeadersRaw(event.target.value)}
                    />
                  </div>
                  <div className="detail-actions" style={{ justifyContent: "flex-end" }}>
                    <Button type="submit" disabled={updateMutation.isPending}>
                      <Wand2 size={14} />
                      {updateMutation.isPending ? t("保存中...") : t("保存规则")}
                    </Button>
                  </div>
                </form>
              </section>

              {selectedRule ? (
                <section className="platform-drawer-section platform-ops-section">
                  <div className="platform-drawer-section-head">
                    <h4>{t("运维操作")}</h4>
                  </div>
                  <div className="platform-ops-list">
                    <article className="platform-op-item">
                      <div className="platform-op-copy">
                        <h5>{t("删除规则")}</h5>
                        <p className="platform-op-hint">
                          {isFallbackRule(selectedRule)
                            ? t('兜底规则 "*" 仅允许编辑，不允许删除。')
                            : t("删除后该规则将不再生效。")}
                        </p>
                      </div>
                      <Button
                        variant="danger"
                        onClick={() => void handleDelete(selectedRule)}
                        disabled={deleteMutation.isPending || isFallbackRule(selectedRule)}
                      >
                        {t("删除")}
                      </Button>
                    </article>
                  </div>
                </section>
              ) : null}
            </div>
          </Card>
        </div>
      ) : null}

      {resolveModalOpen ? (
        <div className="modal-overlay" role="dialog" aria-modal="true" aria-label={t("规则测试")}>
          <Card className="modal-card rules-resolve-modal-card">
            <div className="modal-header">
              <div>
                <h3>{t("规则测试")}</h3>
                <p>{t("输入地址查看命中规则和请求头。")}</p>
              </div>
              <Button variant="ghost" size="sm" onClick={() => setResolveModalOpen(false)}>
                <X size={16} />
              </Button>
            </div>

            <div className="rules-resolve-modal-body">
              <div className="field-group">
                <label className="field-label" htmlFor="resolve-url">
                  {t("目标地址")}
                </label>
                <Input
                  id="resolve-url"
                  placeholder="https://api.example.com/v1/orders/123"
                  value={resolveURL}
                  onChange={(event) => setResolveURL(event.target.value)}
                />
              </div>

              <div className="detail-actions">
                <Button
                  variant="secondary"
                  onClick={() => void resolveMutation.mutateAsync()}
                  disabled={resolveMutation.isPending}
                >
                  {resolveMutation.isPending ? t("测试中...") : t("开始测试")}
                </Button>
              </div>

              {resolveOutput ? (
                <div className="resolve-result">
                  <p>
                    <strong>{t("命中前缀：")}</strong> {resolveOutput.matched_url_prefix || t("无")}
                  </p>
                  <div className="resolve-headers">
                    <strong>{t("命中请求头：")}</strong>
                    {resolveOutput.headers?.length ? (
                      <div className="resolve-badges">
                        {resolveOutput.headers.map((header) => (
                          <Badge key={header} style={getBadgeStyle(header)}>
                            {header}
                          </Badge>
                        ))}
                      </div>
                    ) : (
                      <p className="muted">{t("无")}</p>
                    )}
                  </div>
                </div>
              ) : null}
            </div>
          </Card>
        </div>
      ) : null}

      {createModalOpen ? (
        <div className="modal-overlay" role="dialog" aria-modal="true">
          <Card className="modal-card">
            <div className="modal-header">
              <h3>{t("新建规则")}</h3>
              <Button variant="ghost" size="sm" onClick={() => setCreateModalOpen(false)}>
                <X size={16} />
              </Button>
            </div>

            <form className="form-grid single-column" onSubmit={handleCreateSubmit}>
              <div className="field-group">
                <label className="field-label" htmlFor="create-rule-prefix">
                  {t("地址前缀")}
                </label>
                <Input
                  id="create-rule-prefix"
                  placeholder={t("例如 api.example.com/v1")}
                  value={createPrefix}
                  onChange={(event) => setCreatePrefix(event.target.value)}
                />
              </div>

              <div className="field-group">
                <label className="field-label" htmlFor="create-rule-headers">
                  {t("请求头")}
                </label>
                <Textarea
                  id="create-rule-headers"
                  rows={5}
                  placeholder={t("每行一个 header，例如 Authorization")}
                  value={createHeadersRaw}
                  onChange={(event) => setCreateHeadersRaw(event.target.value)}
                />
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
