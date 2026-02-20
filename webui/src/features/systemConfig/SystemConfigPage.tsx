import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, RefreshCw, RotateCcw, Save, Sparkles } from "lucide-react";
import { useMemo, useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { Textarea } from "../../components/ui/Textarea";
import { ApiError } from "../../lib/api-client";
import { patchSystemConfig, getSystemConfig } from "./api";
import type { RuntimeConfig, RuntimeConfigPatch } from "./types";

type RuntimeConfigForm = {
  user_agent: string;
  request_log_enabled: boolean;
  reverse_proxy_log_detail_enabled: boolean;
  reverse_proxy_log_req_headers_max_bytes: string;
  reverse_proxy_log_req_body_max_bytes: string;
  reverse_proxy_log_resp_headers_max_bytes: string;
  reverse_proxy_log_resp_body_max_bytes: string;
  max_consecutive_failures: string;
  max_latency_test_interval: string;
  max_authority_latency_test_interval: string;
  max_egress_test_interval: string;
  latency_test_url: string;
  latency_authorities_raw: string;
  p2c_latency_window: string;
  latency_decay_window: string;
  cache_flush_interval: string;
  cache_flush_dirty_threshold: string;
  ephemeral_node_evict_delay: string;
};

const EDITABLE_FIELDS: Array<keyof RuntimeConfig> = [
  "user_agent",
  "request_log_enabled",
  "reverse_proxy_log_detail_enabled",
  "reverse_proxy_log_req_headers_max_bytes",
  "reverse_proxy_log_req_body_max_bytes",
  "reverse_proxy_log_resp_headers_max_bytes",
  "reverse_proxy_log_resp_body_max_bytes",
  "max_consecutive_failures",
  "max_latency_test_interval",
  "max_authority_latency_test_interval",
  "max_egress_test_interval",
  "latency_test_url",
  "latency_authorities",
  "p2c_latency_window",
  "latency_decay_window",
  "cache_flush_interval",
  "cache_flush_dirty_threshold",
  "ephemeral_node_evict_delay",
];

const FIELD_LABELS: Record<keyof RuntimeConfig, string> = {
  user_agent: "User Agent",
  request_log_enabled: "Request Log Enabled",
  reverse_proxy_log_detail_enabled: "Reverse Proxy Detail Log",
  reverse_proxy_log_req_headers_max_bytes: "Req Headers Max Bytes",
  reverse_proxy_log_req_body_max_bytes: "Req Body Max Bytes",
  reverse_proxy_log_resp_headers_max_bytes: "Resp Headers Max Bytes",
  reverse_proxy_log_resp_body_max_bytes: "Resp Body Max Bytes",
  max_consecutive_failures: "Max Consecutive Failures",
  max_latency_test_interval: "Max Latency Test Interval",
  max_authority_latency_test_interval: "Max Authority Latency Test Interval",
  max_egress_test_interval: "Max Egress Test Interval",
  latency_test_url: "Latency Test URL",
  latency_authorities: "Latency Authorities",
  p2c_latency_window: "P2C Latency Window",
  latency_decay_window: "Latency Decay Window",
  cache_flush_interval: "Cache Flush Interval",
  cache_flush_dirty_threshold: "Cache Flush Dirty Threshold",
  ephemeral_node_evict_delay: "Ephemeral Node Evict Delay",
};

function fromApiError(error: unknown): string {
  if (error instanceof ApiError) {
    return `${error.code}: ${error.message}`;
  }
  if (error instanceof Error) {
    return error.message;
  }
  return "未知错误";
}

function configToForm(config: RuntimeConfig): RuntimeConfigForm {
  return {
    user_agent: config.user_agent,
    request_log_enabled: config.request_log_enabled,
    reverse_proxy_log_detail_enabled: config.reverse_proxy_log_detail_enabled,
    reverse_proxy_log_req_headers_max_bytes: String(config.reverse_proxy_log_req_headers_max_bytes),
    reverse_proxy_log_req_body_max_bytes: String(config.reverse_proxy_log_req_body_max_bytes),
    reverse_proxy_log_resp_headers_max_bytes: String(config.reverse_proxy_log_resp_headers_max_bytes),
    reverse_proxy_log_resp_body_max_bytes: String(config.reverse_proxy_log_resp_body_max_bytes),
    max_consecutive_failures: String(config.max_consecutive_failures),
    max_latency_test_interval: config.max_latency_test_interval,
    max_authority_latency_test_interval: config.max_authority_latency_test_interval,
    max_egress_test_interval: config.max_egress_test_interval,
    latency_test_url: config.latency_test_url,
    latency_authorities_raw: config.latency_authorities.join("\n"),
    p2c_latency_window: config.p2c_latency_window,
    latency_decay_window: config.latency_decay_window,
    cache_flush_interval: config.cache_flush_interval,
    cache_flush_dirty_threshold: String(config.cache_flush_dirty_threshold),
    ephemeral_node_evict_delay: config.ephemeral_node_evict_delay,
  };
}

function parseNonNegativeInt(field: string, raw: string): number {
  const value = raw.trim();
  if (!value) {
    throw new Error(`${field} 不能为空`);
  }
  const parsed = Number(value);
  if (!Number.isInteger(parsed) || parsed < 0) {
    throw new Error(`${field} 必须是非负整数`);
  }
  return parsed;
}

function parseDurationField(field: string, raw: string): string {
  const value = raw.trim();
  if (!value) {
    throw new Error(`${field} 不能为空`);
  }
  return value;
}

function parseAuthorities(raw: string): string[] {
  const items = raw
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter(Boolean);

  return Array.from(new Set(items));
}

function parseForm(form: RuntimeConfigForm): RuntimeConfig {
  const userAgent = form.user_agent.trim();
  if (!userAgent) {
    throw new Error("User Agent 不能为空");
  }

  const latencyURL = form.latency_test_url.trim();
  if (!latencyURL) {
    throw new Error("Latency Test URL 不能为空");
  }
  if (!latencyURL.startsWith("http://") && !latencyURL.startsWith("https://")) {
    throw new Error("Latency Test URL 必须是 http/https 地址");
  }

  return {
    user_agent: userAgent,
    request_log_enabled: form.request_log_enabled,
    reverse_proxy_log_detail_enabled: form.reverse_proxy_log_detail_enabled,
    reverse_proxy_log_req_headers_max_bytes: parseNonNegativeInt(
      "Req Headers Max Bytes",
      form.reverse_proxy_log_req_headers_max_bytes,
    ),
    reverse_proxy_log_req_body_max_bytes: parseNonNegativeInt("Req Body Max Bytes", form.reverse_proxy_log_req_body_max_bytes),
    reverse_proxy_log_resp_headers_max_bytes: parseNonNegativeInt(
      "Resp Headers Max Bytes",
      form.reverse_proxy_log_resp_headers_max_bytes,
    ),
    reverse_proxy_log_resp_body_max_bytes: parseNonNegativeInt(
      "Resp Body Max Bytes",
      form.reverse_proxy_log_resp_body_max_bytes,
    ),
    max_consecutive_failures: parseNonNegativeInt("Max Consecutive Failures", form.max_consecutive_failures),
    max_latency_test_interval: parseDurationField("Max Latency Test Interval", form.max_latency_test_interval),
    max_authority_latency_test_interval: parseDurationField(
      "Max Authority Latency Test Interval",
      form.max_authority_latency_test_interval,
    ),
    max_egress_test_interval: parseDurationField("Max Egress Test Interval", form.max_egress_test_interval),
    latency_test_url: latencyURL,
    latency_authorities: parseAuthorities(form.latency_authorities_raw),
    p2c_latency_window: parseDurationField("P2C Latency Window", form.p2c_latency_window),
    latency_decay_window: parseDurationField("Latency Decay Window", form.latency_decay_window),
    cache_flush_interval: parseDurationField("Cache Flush Interval", form.cache_flush_interval),
    cache_flush_dirty_threshold: parseNonNegativeInt("Cache Flush Dirty Threshold", form.cache_flush_dirty_threshold),
    ephemeral_node_evict_delay: parseDurationField("Ephemeral Node Evict Delay", form.ephemeral_node_evict_delay),
  };
}

function arrayEquals(a: string[], b: string[]): boolean {
  if (a.length !== b.length) {
    return false;
  }
  for (let i = 0; i < a.length; i += 1) {
    if (a[i] !== b[i]) {
      return false;
    }
  }
  return true;
}

function buildPatch(current: RuntimeConfig, next: RuntimeConfig): RuntimeConfigPatch {
  const patch: RuntimeConfigPatch = {};
  const patchMutable = patch as Record<string, unknown>;

  for (const field of EDITABLE_FIELDS) {
    const currentValue = current[field];
    const nextValue = next[field];

    if (Array.isArray(currentValue) && Array.isArray(nextValue)) {
      if (!arrayEquals(currentValue, nextValue)) {
        patchMutable[field] = nextValue;
      }
      continue;
    }

    if (currentValue !== nextValue) {
      patchMutable[field] = nextValue;
    }
  }

  return patch;
}

export function SystemConfigPage() {
  const [draftForm, setDraftForm] = useState<RuntimeConfigForm | null>(null);
  const [message, setMessage] = useState<{ tone: "success" | "error"; text: string } | null>(null);
  const queryClient = useQueryClient();

  const configQuery = useQuery({
    queryKey: ["system-config"],
    queryFn: getSystemConfig,
    staleTime: 30_000,
  });

  const baseline = configQuery.data ?? null;
  const form = useMemo(() => {
    if (!baseline) {
      return null;
    }
    return draftForm ?? configToForm(baseline);
  }, [baseline, draftForm]);

  const parsedResult = useMemo(() => {
    if (!form) {
      return { config: null as RuntimeConfig | null, error: "" };
    }

    try {
      return { config: parseForm(form), error: "" };
    } catch (error) {
      return { config: null, error: fromApiError(error) };
    }
  }, [form]);

  const patchPreview = useMemo<RuntimeConfigPatch>(() => {
    if (!baseline || !parsedResult.config) {
      return {};
    }
    return buildPatch(baseline, parsedResult.config);
  }, [baseline, parsedResult.config]);

  const changedKeys = useMemo(() => Object.keys(patchPreview) as Array<keyof RuntimeConfig>, [patchPreview]);
  const hasUnsavedChanges = changedKeys.length > 0;

  const saveMutation = useMutation({
    mutationFn: async () => {
      if (!baseline || !form) {
        throw new Error("配置尚未加载完成");
      }
      const parsed = parseForm(form);
      const patch = buildPatch(baseline, parsed);
      const changedCount = Object.keys(patch).length;
      if (!changedCount) {
        throw new Error("没有可提交的变更");
      }
      const updated = await patchSystemConfig(patch);
      return { updated, changedCount };
    },
    onSuccess: ({ updated, changedCount }) => {
      queryClient.setQueryData(["system-config"], updated);
      setDraftForm(null);
      setMessage({ tone: "success", text: `配置已更新（${changedCount} 项变更）` });
    },
    onError: (error) => {
      setMessage({ tone: "error", text: fromApiError(error) });
    },
  });

  const setFormField = <K extends keyof RuntimeConfigForm>(key: K, value: RuntimeConfigForm[K]) => {
    setDraftForm((prev) => {
      if (!baseline) {
        return prev;
      }
      const source = prev ?? configToForm(baseline);
      return { ...source, [key]: value };
    });
  };

  const resetDraft = () => {
    setDraftForm(null);
    setMessage(null);
  };

  const reloadFromServer = async () => {
    if (hasUnsavedChanges) {
      const confirmed = window.confirm("当前有未保存变更，确认丢弃并重新加载服务器配置？");
      if (!confirmed) {
        return;
      }
    }

    setDraftForm(null);
    const result = await configQuery.refetch();
    if (result.data) {
      setMessage({ tone: "success", text: "已加载服务器最新配置" });
    }
  };

  const previewText = useMemo(() => {
    if (parsedResult.error) {
      return `// 表单校验失败\n// ${parsedResult.error}`;
    }
    return JSON.stringify(patchPreview, null, 2);
  }, [patchPreview, parsedResult.error]);

  return (
    <section className="syscfg-page">
      <header className="module-header">
        <div>
          <h2>系统配置</h2>
          <p className="module-description">分组编辑 RuntimeConfig，保存时仅提交差异字段并展示 PATCH 预览。</p>
        </div>
        <Button onClick={() => void reloadFromServer()} disabled={configQuery.isFetching}>
          <RefreshCw size={16} className={configQuery.isFetching ? "spin" : undefined} />
          重新加载
        </Button>
      </header>

      {message ? (
        <div className={message.tone === "success" ? "callout callout-success" : "callout callout-error"}>
          {message.text}
        </div>
      ) : null}

      {!form ? (
        <Card className="syscfg-form-card">
          {configQuery.isLoading ? <p className="muted">正在加载系统配置...</p> : null}
          {configQuery.isError ? (
            <div className="callout callout-error">
              <AlertTriangle size={14} />
              <span>{fromApiError(configQuery.error)}</span>
            </div>
          ) : null}
        </Card>
      ) : (
        <div className="syscfg-layout">
          <Card className="syscfg-form-card">
            <div className="detail-header">
              <div>
                <h3>Runtime Settings</h3>
                <p>按功能分组编辑，支持立即回滚草稿。</p>
              </div>
            </div>

            <section className="syscfg-section">
              <h4>基础与健康检查</h4>
              <div className="form-grid">
                <div className="field-group">
                  <label className="field-label" htmlFor="sys-user-agent">
                    User Agent
                  </label>
                  <Input
                    id="sys-user-agent"
                    value={form.user_agent}
                    onChange={(event) => setFormField("user_agent", event.target.value)}
                  />
                </div>

                <div className="field-group">
                  <label className="field-label" htmlFor="sys-max-fail">
                    Max Consecutive Failures
                  </label>
                  <Input
                    id="sys-max-fail"
                    type="number"
                    min={0}
                    value={form.max_consecutive_failures}
                    onChange={(event) => setFormField("max_consecutive_failures", event.target.value)}
                  />
                </div>
              </div>
            </section>

            <section className="syscfg-section">
              <h4>请求日志</h4>
              <div className="syscfg-checkbox-grid">
                <label className="checkbox-line">
                  <input
                    type="checkbox"
                    checked={form.request_log_enabled}
                    onChange={(event) => setFormField("request_log_enabled", event.target.checked)}
                  />
                  <span>Request Log Enabled</span>
                </label>
                <label className="checkbox-line">
                  <input
                    type="checkbox"
                    checked={form.reverse_proxy_log_detail_enabled}
                    onChange={(event) => setFormField("reverse_proxy_log_detail_enabled", event.target.checked)}
                  />
                  <span>Reverse Proxy Detail Log</span>
                </label>
              </div>

              <div className="form-grid">
                <div className="field-group">
                  <label className="field-label" htmlFor="sys-req-h-max">
                    Req Headers Max Bytes
                  </label>
                  <Input
                    id="sys-req-h-max"
                    type="number"
                    min={0}
                    value={form.reverse_proxy_log_req_headers_max_bytes}
                    onChange={(event) => setFormField("reverse_proxy_log_req_headers_max_bytes", event.target.value)}
                  />
                </div>

                <div className="field-group">
                  <label className="field-label" htmlFor="sys-req-b-max">
                    Req Body Max Bytes
                  </label>
                  <Input
                    id="sys-req-b-max"
                    type="number"
                    min={0}
                    value={form.reverse_proxy_log_req_body_max_bytes}
                    onChange={(event) => setFormField("reverse_proxy_log_req_body_max_bytes", event.target.value)}
                  />
                </div>

                <div className="field-group">
                  <label className="field-label" htmlFor="sys-resp-h-max">
                    Resp Headers Max Bytes
                  </label>
                  <Input
                    id="sys-resp-h-max"
                    type="number"
                    min={0}
                    value={form.reverse_proxy_log_resp_headers_max_bytes}
                    onChange={(event) => setFormField("reverse_proxy_log_resp_headers_max_bytes", event.target.value)}
                  />
                </div>

                <div className="field-group">
                  <label className="field-label" htmlFor="sys-resp-b-max">
                    Resp Body Max Bytes
                  </label>
                  <Input
                    id="sys-resp-b-max"
                    type="number"
                    min={0}
                    value={form.reverse_proxy_log_resp_body_max_bytes}
                    onChange={(event) => setFormField("reverse_proxy_log_resp_body_max_bytes", event.target.value)}
                  />
                </div>
              </div>
            </section>

            <section className="syscfg-section">
              <h4>探测与路由</h4>
              <div className="form-grid">
                <div className="field-group field-span-2">
                  <label className="field-label" htmlFor="sys-latency-url">
                    Latency Test URL
                  </label>
                  <Input
                    id="sys-latency-url"
                    value={form.latency_test_url}
                    onChange={(event) => setFormField("latency_test_url", event.target.value)}
                  />
                </div>

                <div className="field-group">
                  <label className="field-label" htmlFor="sys-max-latency-int">
                    Max Latency Test Interval
                  </label>
                  <Input
                    id="sys-max-latency-int"
                    value={form.max_latency_test_interval}
                    onChange={(event) => setFormField("max_latency_test_interval", event.target.value)}
                  />
                </div>

                <div className="field-group">
                  <label className="field-label" htmlFor="sys-max-auth-latency-int">
                    Max Authority Latency Test Interval
                  </label>
                  <Input
                    id="sys-max-auth-latency-int"
                    value={form.max_authority_latency_test_interval}
                    onChange={(event) => setFormField("max_authority_latency_test_interval", event.target.value)}
                  />
                </div>

                <div className="field-group">
                  <label className="field-label" htmlFor="sys-max-egress-int">
                    Max Egress Test Interval
                  </label>
                  <Input
                    id="sys-max-egress-int"
                    value={form.max_egress_test_interval}
                    onChange={(event) => setFormField("max_egress_test_interval", event.target.value)}
                  />
                </div>

                <div className="field-group">
                  <label className="field-label" htmlFor="sys-p2c-window">
                    P2C Latency Window
                  </label>
                  <Input
                    id="sys-p2c-window"
                    value={form.p2c_latency_window}
                    onChange={(event) => setFormField("p2c_latency_window", event.target.value)}
                  />
                </div>

                <div className="field-group">
                  <label className="field-label" htmlFor="sys-decay-window">
                    Latency Decay Window
                  </label>
                  <Input
                    id="sys-decay-window"
                    value={form.latency_decay_window}
                    onChange={(event) => setFormField("latency_decay_window", event.target.value)}
                  />
                </div>

                <div className="field-group field-span-2">
                  <label className="field-label" htmlFor="sys-latency-authorities">
                    Latency Authorities
                  </label>
                  <Textarea
                    id="sys-latency-authorities"
                    rows={4}
                    placeholder={"gstatic.com\ngoogle.com\ncloudflare.com"}
                    value={form.latency_authorities_raw}
                    onChange={(event) => setFormField("latency_authorities_raw", event.target.value)}
                  />
                </div>
              </div>
            </section>

            <section className="syscfg-section">
              <h4>持久化策略</h4>
              <div className="form-grid">
                <div className="field-group">
                  <label className="field-label" htmlFor="sys-cache-flush-int">
                    Cache Flush Interval
                  </label>
                  <Input
                    id="sys-cache-flush-int"
                    value={form.cache_flush_interval}
                    onChange={(event) => setFormField("cache_flush_interval", event.target.value)}
                  />
                </div>

                <div className="field-group">
                  <label className="field-label" htmlFor="sys-cache-threshold">
                    Cache Flush Dirty Threshold
                  </label>
                  <Input
                    id="sys-cache-threshold"
                    type="number"
                    min={0}
                    value={form.cache_flush_dirty_threshold}
                    onChange={(event) => setFormField("cache_flush_dirty_threshold", event.target.value)}
                  />
                </div>

                <div className="field-group">
                  <label className="field-label" htmlFor="sys-evict-delay">
                    Ephemeral Node Evict Delay
                  </label>
                  <Input
                    id="sys-evict-delay"
                    value={form.ephemeral_node_evict_delay}
                    onChange={(event) => setFormField("ephemeral_node_evict_delay", event.target.value)}
                  />
                </div>
              </div>
            </section>
          </Card>

          <div className="syscfg-side">
            <Card className="syscfg-summary-card">
              <div className="detail-header">
                <div>
                  <h3>变更摘要</h3>
                  <p>{hasUnsavedChanges ? `${changedKeys.length} 项待提交` : "当前无未保存改动"}</p>
                </div>
              </div>

              {parsedResult.error ? (
                <div className="callout callout-error">
                  <AlertTriangle size={14} />
                  <span>{parsedResult.error}</span>
                </div>
              ) : null}

              {changedKeys.length ? (
                <div className="syscfg-change-list">
                  {changedKeys.map((field) => (
                    <Badge key={field} variant="neutral">
                      {FIELD_LABELS[field]}
                    </Badge>
                  ))}
                </div>
              ) : (
                <div className="empty-box">
                  <Sparkles size={16} />
                  <p>等待配置变更</p>
                </div>
              )}

              <div className="detail-actions">
                <Button
                  onClick={() => void saveMutation.mutateAsync()}
                  disabled={saveMutation.isPending || Boolean(parsedResult.error) || !hasUnsavedChanges}
                >
                  <Save size={14} />
                  {saveMutation.isPending ? "保存中..." : "保存配置"}
                </Button>
                <Button variant="ghost" onClick={resetDraft} disabled={!hasUnsavedChanges || saveMutation.isPending}>
                  <RotateCcw size={14} />
                  重置草稿
                </Button>
              </div>
            </Card>

            <Card className="syscfg-preview-card">
              <div className="detail-header">
                <div>
                  <h3>PATCH Preview</h3>
                  <p>提交前可确认最终发送 JSON</p>
                </div>
              </div>

              <pre className="syscfg-preview">{previewText || "{}"}</pre>
            </Card>
          </div>
        </div>
      )}
    </section>
  );
}
