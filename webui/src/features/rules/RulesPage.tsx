import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Eraser, RefreshCw, Search, Sparkles, Wand2 } from "lucide-react";
import { useMemo, useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { Textarea } from "../../components/ui/Textarea";
import { ApiError } from "../../lib/api-client";
import { formatDateTime } from "../../lib/time";
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

function ruleHeadersPreview(rule: Rule): string {
  if (!rule.headers.length) {
    return "-";
  }

  if (rule.headers.length <= 2) {
    return rule.headers.join(", ");
  }

  return `${rule.headers.slice(0, 2).join(", ")} +${rule.headers.length - 2}`;
}

export function RulesPage() {
  const [search, setSearch] = useState("");
  const [selectedPrefix, setSelectedPrefix] = useState("");
  const [formPrefix, setFormPrefix] = useState("");
  const [formHeadersRaw, setFormHeadersRaw] = useState("");
  const [resolveURL, setResolveURL] = useState("");
  const [resolveOutput, setResolveOutput] = useState<ResolveResult | null>(null);
  const [message, setMessage] = useState<{ tone: "success" | "error"; text: string } | null>(null);

  const queryClient = useQueryClient();

  const rulesQuery = useQuery({
    queryKey: ["header-rules"],
    queryFn: listRules,
    refetchInterval: 30_000,
  });

  const rules = rulesQuery.data ?? EMPTY_RULES;

  const visibleRules = useMemo(() => {
    const keyword = search.trim().toLowerCase();
    if (!keyword) {
      return rules;
    }

    return rules.filter((rule) => {
      return (
        rule.url_prefix.toLowerCase().includes(keyword) ||
        rule.headers.some((header) => header.toLowerCase().includes(keyword))
      );
    });
  }, [rules, search]);

  const selectedRule = useMemo(() => {
    if (!visibleRules.length) {
      return null;
    }
    return visibleRules.find((item) => item.url_prefix === selectedPrefix) ?? visibleRules[0];
  }, [visibleRules, selectedPrefix]);

  const syncFormFromRule = (rule: Rule) => {
    setFormPrefix(rule.url_prefix);
    setFormHeadersRaw(rule.headers.join("\n"));
    setSelectedPrefix(rule.url_prefix);
  };

  const invalidateRules = async () => {
    await queryClient.invalidateQueries({ queryKey: ["header-rules"] });
  };

  const upsertMutation = useMutation({
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
      setMessage({ tone: "success", text: `规则 ${rule.url_prefix} 已保存` });
    },
    onError: (error) => {
      setMessage({ tone: "error", text: fromApiError(error) });
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
      }
      setMessage({ tone: "success", text: `规则 ${prefix} 已删除` });
    },
    onError: (error) => {
      setMessage({ tone: "error", text: fromApiError(error) });
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
      setMessage({ tone: "error", text: fromApiError(error) });
    },
  });

  const handleDelete = async (rule: Rule) => {
    const confirmed = window.confirm(`确认删除规则 ${rule.url_prefix} 吗？`);
    if (!confirmed) {
      return;
    }
    await deleteMutation.mutateAsync(rule.url_prefix);
  };

  const clearForm = () => {
    setFormPrefix("");
    setFormHeadersRaw("");
    setSelectedPrefix("");
  };

  return (
    <section className="rules-page">
      <header className="module-header">
        <div>
          <h2>Header 规则</h2>
          <p className="module-description">管理 URL 前缀匹配规则，并实时调试 resolve 结果。</p>
        </div>
        <Button onClick={() => void rulesQuery.refetch()} disabled={rulesQuery.isFetching}>
          <RefreshCw size={16} className={rulesQuery.isFetching ? "spin" : undefined} />
          刷新
        </Button>
      </header>

      {message ? (
        <div className={message.tone === "success" ? "callout callout-success" : "callout callout-error"}>
          {message.text}
        </div>
      ) : null}

      <div className="rules-layout">
        <Card className="rules-list-card">
          <div className="list-card-header">
            <div>
              <h3>规则列表</h3>
              <p>共 {rules.length} 条</p>
            </div>
          </div>

          <label className="search-box" htmlFor="rules-search">
            <Search size={14} />
            <Input
              id="rules-search"
              placeholder="按 prefix / header 过滤"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
          </label>

          {rulesQuery.isLoading ? <p className="muted">正在加载规则...</p> : null}

          {rulesQuery.isError ? (
            <div className="callout callout-error">
              <AlertTriangle size={14} />
              <span>{fromApiError(rulesQuery.error)}</span>
            </div>
          ) : null}

          {!rulesQuery.isLoading && !visibleRules.length ? (
            <div className="empty-box">
              <Sparkles size={16} />
              <p>没有匹配规则</p>
            </div>
          ) : null}

          {visibleRules.length ? (
            <div className="rules-table-wrap">
              <table className="rules-table">
                <thead>
                  <tr>
                    <th>URL Prefix</th>
                    <th>Headers</th>
                    <th>Updated</th>
                    <th>Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {visibleRules.map((rule) => {
                    const selected = selectedRule?.url_prefix === rule.url_prefix;
                    return (
                      <tr
                        key={rule.url_prefix}
                        className={selected ? "nodes-row-selected" : undefined}
                        onClick={() => syncFormFromRule(rule)}
                      >
                        <td title={rule.url_prefix}>{rule.url_prefix}</td>
                        <td title={rule.headers.join(", ")}>{ruleHeadersPreview(rule)}</td>
                        <td>{formatDateTime(rule.updated_at)}</td>
                        <td>
                          <div className="nodes-row-actions" onClick={(event) => event.stopPropagation()}>
                            <Button variant="secondary" size="sm" onClick={() => syncFormFromRule(rule)}>
                              编辑
                            </Button>
                            <Button
                              variant="danger"
                              size="sm"
                              onClick={() => void handleDelete(rule)}
                              disabled={deleteMutation.isPending}
                            >
                              删除
                            </Button>
                          </div>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          ) : null}
        </Card>

        <div className="rules-panels">
          <Card className="rules-editor-card">
            <div className="detail-header">
              <div>
                <h3>Rule Editor</h3>
                <p>{selectedRule ? `当前编辑：${selectedRule.url_prefix}` : "创建新规则"}</p>
              </div>
              <Button variant="ghost" size="sm" onClick={clearForm}>
                <Eraser size={14} />
                清空
              </Button>
            </div>

            <div className="form-grid single-column">
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
                  placeholder="每行一个 header，例如\nAuthorization\nX-API-Key"
                  value={formHeadersRaw}
                  onChange={(event) => setFormHeadersRaw(event.target.value)}
                />
              </div>
            </div>

            <div className="detail-actions">
              <Button onClick={() => void upsertMutation.mutateAsync()} disabled={upsertMutation.isPending}>
                <Wand2 size={14} />
                {upsertMutation.isPending ? "保存中..." : "保存规则"}
              </Button>
            </div>
          </Card>

          <Card className="rules-resolve-card">
            <div className="detail-header">
              <div>
                <h3>Resolve 调试</h3>
                <p>输入 URL 查看命中的规则与 headers</p>
              </div>
            </div>

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
              <Button variant="secondary" onClick={() => void resolveMutation.mutateAsync()} disabled={resolveMutation.isPending}>
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
                        <Badge key={header} variant="neutral">
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
          </Card>
        </div>
      </div>
    </section>
  );
}
