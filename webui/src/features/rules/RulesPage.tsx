import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Bug, Pencil, Plus, RefreshCw, Search, Sparkles, Trash2, Wand2, X } from "lucide-react";
import { type FormEvent, useEffect, useMemo, useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { Textarea } from "../../components/ui/Textarea";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { ApiError } from "../../lib/api-client";
import { deleteRule, listRules, resolveRule, upsertRule } from "./api";
import type { ResolveResult, Rule } from "./types";

const EMPTY_RULES: Rule[] = [];

function fromApiError(error: unknown): string {
  if (error instanceof ApiError) {
    return `${error.code}: ${error.message}`;
  }
  if (error instanceof Error) {
    return error.message;
  }
  return "未知错误";
}

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

  const syncFormFromRule = (rule: Rule) => {
    setFormPrefix(rule.url_prefix);
    setFormHeadersRaw(rule.headers.join("\n"));
    setSelectedPrefix(rule.url_prefix);
  };

  const openDrawerForRule = (rule: Rule) => {
    syncFormFromRule(rule);
    setDrawerOpen(true);
  };

  const invalidateRules = async () => {
    await queryClient.invalidateQueries({ queryKey: ["header-rules"] });
  };

  const createMutation = useMutation({
    mutationFn: async () => {
      const prefix = createPrefix.trim();
      const headers = parseHeaderList(createHeadersRaw);
      if (!prefix) {
        throw new Error("url_prefix 不能为空");
      }
      if (!headers.length) {
        throw new Error("headers 不能为空");
      }
      return upsertRule(prefix, headers);
    },
    onSuccess: async (rule) => {
      await invalidateRules();
      setCreateModalOpen(false);
      setCreatePrefix("");
      setCreateHeadersRaw("");
      showToast("success", `规则 ${rule.url_prefix} 已创建`);
    },
    onError: (error) => {
      showToast("error", fromApiError(error));
    },
  });

  const updateMutation = useMutation({
    mutationFn: async () => {
      const prefix = formPrefix.trim();
      const headers = parseHeaderList(formHeadersRaw);
      if (!prefix) {
        throw new Error("url_prefix 不能为空");
      }
      if (!headers.length) {
        throw new Error("headers 不能为空");
      }
      return upsertRule(prefix, headers);
    },
    onSuccess: async (rule) => {
      await invalidateRules();
      syncFormFromRule(rule);
      showToast("success", `规则 ${rule.url_prefix} 已保存`);
    },
    onError: (error) => {
      showToast("error", fromApiError(error));
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
      showToast("success", `规则 ${prefix} 已删除`);
    },
    onError: (error) => {
      showToast("error", fromApiError(error));
    },
  });

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
      showToast("error", fromApiError(error));
    },
  });

  const handleDelete = async (rule: Rule) => {
    if (isFallbackRule(rule)) {
      showToast("error", '兜底规则 "*" 不允许删除');
      return;
    }
    const confirmed = window.confirm(`确认删除规则 ${rule.url_prefix} 吗？`);
    if (!confirmed) {
      return;
    }
    await deleteMutation.mutateAsync(rule.url_prefix);
  };



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

  return (
    <section className="rules-page">
      <header className="module-header">
        <div>
          <h2>Header 规则</h2>
          <p className="module-description">管理 URL 前缀匹配规则，并实时调试 resolve 结果。</p>
        </div>
      </header>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <Card className="platform-list-card platform-directory-card rules-list-card">
        <div className="list-card-header">
          <div>
            <h3>规则列表</h3>
            <p>共 {rules.length} 条</p>
          </div>
          <div style={{ display: "flex", gap: "0.5rem", alignItems: "center" }}>
            <label className="search-box" htmlFor="rules-search" style={{ maxWidth: 200, margin: 0, gap: 6 }}>
              <Search size={16} />
              <Input
                id="rules-search"
                placeholder="搜索规则"
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                style={{ padding: "6px 10px", borderRadius: 8 }}
              />
            </label>
            <Button variant="secondary" size="sm" onClick={() => setCreateModalOpen(true)}>
              <Plus size={16} />
              新建
            </Button>
            <Button variant="secondary" size="sm" onClick={() => setResolveModalOpen(true)}>
              <Bug size={16} />
              调试
            </Button>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => void rulesQuery.refetch()}
              disabled={rulesQuery.isFetching}
            >
              <RefreshCw size={16} className={rulesQuery.isFetching ? "spin" : undefined} />
              刷新
            </Button>
          </div>
        </div>
      </Card>

      <Card className="platform-cards-container subscriptions-table-card rules-table-card">
        {rulesQuery.isLoading ? <p className="muted">正在加载规则...</p> : null}

        {rulesQuery.isError ? (
          <div className="callout callout-error">
            <AlertTriangle size={14} />
            <span>{fromApiError(rulesQuery.error)}</span>
          </div>
        ) : null}

        {!rulesQuery.isLoading && !rules.length ? (
          <div className="empty-box">
            <Sparkles size={16} />
            <p>没有匹配规则</p>
          </div>
        ) : null}

        {rules.length ? (
          <div className="rules-table-wrap">
            <table className="rules-table">
              <thead>
                <tr>
                  <th>URL 前缀</th>
                  <th>请求头</th>
                  <th>操作</th>
                </tr>
              </thead>
              <tbody>
                {rules.map((rule) => (
                  <tr
                    key={rule.url_prefix}
                    className="clickable-row"
                    onClick={() => openDrawerForRule(rule)}
                  >
                    <td title={rule.url_prefix}>{rule.url_prefix}</td>
                    <td>
                      <RuleHeadersPreview rule={rule} />
                    </td>
                    <td>
                      <div className="subscriptions-row-actions" onClick={(event) => event.stopPropagation()}>
                        <Button size="sm" variant="ghost" onClick={() => openDrawerForRule(rule)} title="编辑">
                          <Pencil size={14} />
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() => void handleDelete(rule)}
                          disabled={deleteMutation.isPending || isFallbackRule(rule)}
                          title={isFallbackRule(rule) ? '兜底规则 "*" 不可删除' : "删除"}
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
      </Card>

      {drawerOpen ? (
        <div className="drawer-overlay" role="dialog" aria-modal="true" aria-label="规则编辑抽屉" onClick={() => setDrawerOpen(false)}>
          <Card className="drawer-panel" onClick={(event) => event.stopPropagation()}>
            <div className="drawer-header">
              <div>
                <h3>{selectedRule?.url_prefix || "规则编辑"}</h3>
                <p>编辑当前 Header 规则</p>
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
                  <h4>Rule Editor</h4>
                  <p>编辑 prefix 与 headers 并保存。</p>
                </div>

                <form className="form-grid single-column" onSubmit={handleUpdateSubmit}>
                  <div className="field-group">
                    <label className="field-label" htmlFor="rule-prefix">
                      URL Prefix
                    </label>
                    <Input
                      id="rule-prefix"
                      placeholder="例如 api.example.com/v1"
                      value={formPrefix}
                      onChange={(event) => setFormPrefix(event.target.value)}
                    />
                  </div>

                  <div className="field-group">
                    <label className="field-label" htmlFor="rule-headers">
                      Headers
                    </label>
                    <Textarea
                      id="rule-headers"
                      rows={5}
                      placeholder="每行一个 header，例如 Authorization"
                      value={formHeadersRaw}
                      onChange={(event) => setFormHeadersRaw(event.target.value)}
                    />
                  </div>
                  <div className="detail-actions" style={{ justifyContent: "flex-end" }}>
                    <Button type="submit" disabled={updateMutation.isPending}>
                      <Wand2 size={14} />
                      {updateMutation.isPending ? "保存中..." : "保存规则"}
                    </Button>
                  </div>
                </form>
              </section>

              {selectedRule ? (
                <section className="platform-drawer-section platform-ops-section">
                  <div className="platform-drawer-section-head">
                    <h4>运维操作</h4>
                  </div>
                  <div className="platform-ops-list">
                    <article className="platform-op-item">
                      <div className="platform-op-copy">
                        <h5>删除规则</h5>
                        <p className="platform-op-hint">
                          {isFallbackRule(selectedRule)
                            ? '兜底规则 "*" 仅允许编辑，不允许删除。'
                            : "删除后该 prefix 将不再生效。"}
                        </p>
                      </div>
                      <Button
                        variant="danger"
                        onClick={() => void handleDelete(selectedRule)}
                        disabled={deleteMutation.isPending || isFallbackRule(selectedRule)}
                      >
                        删除
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
        <div className="modal-overlay" role="dialog" aria-modal="true" aria-label="Resolve 调试">
          <Card className="modal-card rules-resolve-modal-card">
            <div className="modal-header">
              <div>
                <h3>Resolve 调试</h3>
                <p>输入 URL 查看命中的规则与 headers</p>
              </div>
              <Button variant="ghost" size="sm" onClick={() => setResolveModalOpen(false)}>
                <X size={16} />
              </Button>
            </div>

            <div className="rules-resolve-modal-body">
              <div className="field-group">
                <label className="field-label" htmlFor="resolve-url">
                  URL
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
                  {resolveMutation.isPending ? "解析中..." : "执行 Resolve"}
                </Button>
              </div>

              {resolveOutput ? (
                <div className="resolve-result">
                  <p>
                    <strong>Matched Prefix:</strong> {resolveOutput.matched_url_prefix || "(none)"}
                  </p>
                  <div className="resolve-headers">
                    <strong>Headers:</strong>
                    {resolveOutput.headers?.length ? (
                      <div className="resolve-badges">
                        {resolveOutput.headers.map((header) => (
                          <Badge key={header} style={getBadgeStyle(header)}>
                            {header}
                          </Badge>
                        ))}
                      </div>
                    ) : (
                      <p className="muted">(none)</p>
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
              <h3>新建规则</h3>
              <Button variant="ghost" size="sm" onClick={() => setCreateModalOpen(false)}>
                <X size={16} />
              </Button>
            </div>

            <form className="form-grid single-column" onSubmit={handleCreateSubmit}>
              <div className="field-group">
                <label className="field-label" htmlFor="create-rule-prefix">
                  URL Prefix
                </label>
                <Input
                  id="create-rule-prefix"
                  placeholder="例如 api.example.com/v1"
                  value={createPrefix}
                  onChange={(event) => setCreatePrefix(event.target.value)}
                />
              </div>

              <div className="field-group">
                <label className="field-label" htmlFor="create-rule-headers">
                  Headers
                </label>
                <Textarea
                  id="create-rule-headers"
                  rows={5}
                  placeholder="每行一个 header，例如 Authorization"
                  value={createHeadersRaw}
                  onChange={(event) => setCreateHeadersRaw(event.target.value)}
                />
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
