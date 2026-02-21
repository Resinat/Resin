import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, RefreshCw, RotateCcw, Save, Sparkles } from "lucide-react";
import { useMemo, useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { Switch } from "../../components/ui/Switch";
import { Textarea } from "../../components/ui/Textarea";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { ApiError } from "../../lib/api-client";
import { getEnvConfig, patchSystemConfig, getSystemConfig, getDefaultSystemConfig } from "./api";
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
  user_agent: "请求 User Agent",
  request_log_enabled: "启用请求日志",
  reverse_proxy_log_detail_enabled: "记录详细反代日志",
  reverse_proxy_log_req_headers_max_bytes: "请求头最大字节数",
  reverse_proxy_log_req_body_max_bytes: "请求体最大字节数",
  reverse_proxy_log_resp_headers_max_bytes: "响应头最大字节数",
  reverse_proxy_log_resp_body_max_bytes: "响应体最大字节数",
  max_consecutive_failures: "最大连续失败次数",
  max_latency_test_interval: "节点延迟最大测试间隔",
  max_authority_latency_test_interval: "权威域名最大测试间隔",
  max_egress_test_interval: "出口 IP 更新检查间隔",
  latency_test_url: "延迟测试目标 URL",
  latency_authorities: "延迟测试权威域名列表",
  p2c_latency_window: "P2C 延迟衰减窗口",
  latency_decay_window: "历史延迟衰减窗口",
  cache_flush_interval: "缓存异步刷盘间隔",
  cache_flush_dirty_threshold: "缓存刷盘脏阈值",
  ephemeral_node_evict_delay: "临时节点驱逐延迟",
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
  const [customPatchText, setCustomPatchText] = useState<string | null>(null);
  const { toasts, showToast, dismissToast } = useToast();
  const queryClient = useQueryClient();

  const configQuery = useQuery({
    queryKey: ["system-config"],
    queryFn: getSystemConfig,
    staleTime: 30_000,
  });

  const defaultConfigQuery = useQuery({
    queryKey: ["system-config-default"],
    queryFn: getDefaultSystemConfig,
    staleTime: 30_000,
  });

  const envConfigQuery = useQuery({
    queryKey: ["system-config-env"],
    queryFn: getEnvConfig,
    staleTime: Infinity, // Env config does not change at runtime
  });

  const baseline = configQuery.data ?? null;
  const defaultBaseline = defaultConfigQuery.data ?? null;
  const envBaseline = envConfigQuery.data ?? null;

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
      let patchToSend: RuntimeConfigPatch;
      if (customPatchText !== null) {
        try {
          patchToSend = JSON.parse(customPatchText);
        } catch {
          throw new Error("手动编辑的 JSON 格式有误，请检查");
        }
      } else {
        const parsed = parseForm(form);
        patchToSend = buildPatch(baseline, parsed);
      }

      const changedCount = Object.keys(patchToSend).length;
      if (!changedCount) {
        throw new Error("没有可提交的变更");
      }
      const updated = await patchSystemConfig(patchToSend);
      return { updated, changedCount };
    },
    onSuccess: ({ updated, changedCount }) => {
      queryClient.setQueryData(["system-config"], updated);
      setDraftForm(null);
      setCustomPatchText(null);
      showToast("success", `配置已更新（${changedCount} 项变更）`);
    },
    onError: (error) => {
      showToast("error", fromApiError(error));
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

  const handleRestoreDefault = (key: keyof RuntimeConfigForm) => {
    if (!defaultBaseline || !baseline) {
      showToast("error", "默认配置尚未加载");
      return;
    }

    const defaultForm = configToForm(defaultBaseline);
    const value = defaultForm[key];

    setDraftForm((prev) => {
      const source = prev ?? configToForm(baseline);
      return { ...source, [key]: value };
    });
  };

  const renderRestoreButton = (fieldKey: keyof RuntimeConfigForm) => {
    const displayVal = defaultBaseline ? (() => {
      const val = configToForm(defaultBaseline)[fieldKey];
      if (typeof val === "boolean") return val ? "开启" : "关闭";
      if (val === "") return "空";
      return String(val);
    })() : "";

    return (
      <button
        type="button"
        title={displayVal ? `恢复为默认值: ${displayVal}` : "恢复为默认值"}
        onClick={() => handleRestoreDefault(fieldKey)}
        style={{
          background: "transparent",
          border: "none",
          cursor: "pointer",
          display: "inline-flex",
          alignItems: "center",
          justifyContent: "center",
          color: "var(--text-muted, #888)",
          padding: "4px",
          marginLeft: "4px",
          opacity: 0.6,
          transition: "opacity 0.2s"
        }}
        onMouseEnter={(e) => e.currentTarget.style.opacity = "1"}
        onMouseLeave={(e) => e.currentTarget.style.opacity = "0.6"}
      >
        <RotateCcw size={14} />
      </button>
    );
  };

  const resetDraft = () => {
    setDraftForm(null);
    setCustomPatchText(null);
  };

  const reloadFromServer = async () => {
    if (hasUnsavedChanges) {
      const confirmed = window.confirm("当前有未保存变更，确认丢弃并重新加载运行时配置？");
      if (!confirmed) {
        return;
      }
    }

    setDraftForm(null);
    setCustomPatchText(null);
    const result = await configQuery.refetch();
    if (result.data) {
      showToast("success", "已加载最新运行时配置");
    }
  };

  const handlePatchEdit = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    setCustomPatchText(e.target.value);
  };

  const defaultPatchText = useMemo(() => {
    return JSON.stringify(patchPreview, null, 2);
  }, [patchPreview]);

  const displayedPatchText = customPatchText ?? defaultPatchText;

  const isSaveDisabled = saveMutation.isPending || (customPatchText === null && (Boolean(parsedResult.error) || !hasUnsavedChanges));

  return (
    <section className="syscfg-page">
      <header className="module-header">
        <div>
          <h2>系统配置</h2>
          <p className="module-description">分组编辑 RuntimeConfig，保存时仅提交差异字段并展示 PATCH 预览。</p>
        </div>
      </header>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      {!form ? (
        <Card className="syscfg-form-card platform-directory-card">
          {(configQuery.isLoading || envConfigQuery.isLoading) ? <p className="muted">正在加载配置...</p> : null}
          {configQuery.isError ? (
            <div className="callout callout-error">
              <AlertTriangle size={14} />
              <span>{fromApiError(configQuery.error)}</span>
            </div>
          ) : null}
          {envConfigQuery.isError ? (
            <div className="callout callout-error">
              <AlertTriangle size={14} />
              <span>静态配置加载失败: {fromApiError(envConfigQuery.error)}</span>
            </div>
          ) : null}
        </Card>
      ) : (
        <div className="syscfg-layout">
          <div className="syscfg-main" style={{ display: "flex", flexDirection: "column", gap: "24px" }}>
            <Card className="syscfg-form-card platform-directory-card">
              <div className="detail-header">
                <div>
                  <h3>Runtime Settings</h3>
                  <p>按功能分组编辑，支持立即回滚草稿。</p>
                </div>
                <Button variant="secondary" size="sm" onClick={() => void reloadFromServer()} disabled={configQuery.isFetching}>
                  <RefreshCw size={16} className={configQuery.isFetching ? "spin" : undefined} />
                  刷新
                </Button>
              </div>

              <section className="syscfg-section">
                <h4>基础与健康检查</h4>
                <div className="form-grid">
                  <div className="field-group">
                    <div style={{ display: "flex", alignItems: "center" }}>
                      <label className="field-label" htmlFor="sys-user-agent" style={{ margin: 0 }}>
                        请求 User Agent
                      </label>
                      {renderRestoreButton("user_agent")}
                    </div>
                    <Input
                      id="sys-user-agent"
                      value={form.user_agent}
                      onChange={(event) => setFormField("user_agent", event.target.value)}
                    />
                  </div>

                  <div className="field-group">
                    <div style={{ display: "flex", alignItems: "center" }}>
                      <label className="field-label" htmlFor="sys-max-fail" style={{ margin: 0 }}>
                        最大连续失败次数
                      </label>
                      {renderRestoreButton("max_consecutive_failures")}
                    </div>
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
                <div className="syscfg-checkbox-grid" style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: "16px" }}>
                  <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", background: "var(--surface-sunken, rgba(0,0,0,0.02))", padding: "12px 16px", borderRadius: "8px", border: "1px solid var(--border)" }}>
                    <div style={{ display: "flex", alignItems: "center" }}>
                      <span className="field-label" style={{ margin: 0, fontWeight: 500 }}>启用请求日志</span>
                      {renderRestoreButton("request_log_enabled")}
                    </div>
                    <Switch
                      checked={form.request_log_enabled}
                      onChange={(event) => setFormField("request_log_enabled", event.target.checked)}
                    />
                  </div>
                  <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", background: "var(--surface-sunken, rgba(0,0,0,0.02))", padding: "12px 16px", borderRadius: "8px", border: "1px solid var(--border)" }}>
                    <div style={{ display: "flex", alignItems: "center" }}>
                      <span className="field-label" style={{ margin: 0, fontWeight: 500 }}>记录详细反代日志</span>
                      {renderRestoreButton("reverse_proxy_log_detail_enabled")}
                    </div>
                    <Switch
                      checked={form.reverse_proxy_log_detail_enabled}
                      onChange={(event) => setFormField("reverse_proxy_log_detail_enabled", event.target.checked)}
                    />
                  </div>
                </div>

                <div className="form-grid" style={{ marginTop: "16px" }}>
                  <div className="field-group">
                    <div style={{ display: "flex", alignItems: "center" }}>
                      <label className="field-label" htmlFor="sys-req-h-max" style={{ margin: 0 }}>
                        请求头最大字节数
                      </label>
                      {renderRestoreButton("reverse_proxy_log_req_headers_max_bytes")}
                    </div>
                    <Input
                      id="sys-req-h-max"
                      type="number"
                      min={0}
                      value={form.reverse_proxy_log_req_headers_max_bytes}
                      onChange={(event) => setFormField("reverse_proxy_log_req_headers_max_bytes", event.target.value)}
                    />
                  </div>

                  <div className="field-group">
                    <div style={{ display: "flex", alignItems: "center" }}>
                      <label className="field-label" htmlFor="sys-req-b-max" style={{ margin: 0 }}>
                        请求体最大字节数
                      </label>
                      {renderRestoreButton("reverse_proxy_log_req_body_max_bytes")}
                    </div>
                    <Input
                      id="sys-req-b-max"
                      type="number"
                      min={0}
                      value={form.reverse_proxy_log_req_body_max_bytes}
                      onChange={(event) => setFormField("reverse_proxy_log_req_body_max_bytes", event.target.value)}
                    />
                  </div>

                  <div className="field-group">
                    <div style={{ display: "flex", alignItems: "center" }}>
                      <label className="field-label" htmlFor="sys-resp-h-max" style={{ margin: 0 }}>
                        响应头最大字节数
                      </label>
                      {renderRestoreButton("reverse_proxy_log_resp_headers_max_bytes")}
                    </div>
                    <Input
                      id="sys-resp-h-max"
                      type="number"
                      min={0}
                      value={form.reverse_proxy_log_resp_headers_max_bytes}
                      onChange={(event) => setFormField("reverse_proxy_log_resp_headers_max_bytes", event.target.value)}
                    />
                  </div>

                  <div className="field-group">
                    <div style={{ display: "flex", alignItems: "center" }}>
                      <label className="field-label" htmlFor="sys-resp-b-max" style={{ margin: 0 }}>
                        响应体最大字节数
                      </label>
                      {renderRestoreButton("reverse_proxy_log_resp_body_max_bytes")}
                    </div>
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
                    <div style={{ display: "flex", alignItems: "center" }}>
                      <label className="field-label" htmlFor="sys-latency-url" style={{ margin: 0 }}>
                        延迟测试目标 URL
                      </label>
                      {renderRestoreButton("latency_test_url")}
                    </div>
                    <Input
                      id="sys-latency-url"
                      value={form.latency_test_url}
                      onChange={(event) => setFormField("latency_test_url", event.target.value)}
                    />
                  </div>

                  <div className="field-group">
                    <div style={{ display: "flex", alignItems: "center" }}>
                      <label className="field-label" htmlFor="sys-max-latency-int" style={{ margin: 0 }}>
                        节点延迟最大测试间隔
                      </label>
                      {renderRestoreButton("max_latency_test_interval")}
                    </div>
                    <Input
                      id="sys-max-latency-int"
                      value={form.max_latency_test_interval}
                      onChange={(event) => setFormField("max_latency_test_interval", event.target.value)}
                    />
                  </div>

                  <div className="field-group">
                    <div style={{ display: "flex", alignItems: "center" }}>
                      <label className="field-label" htmlFor="sys-max-auth-latency-int" style={{ margin: 0 }}>
                        权威域名最大测试间隔
                      </label>
                      {renderRestoreButton("max_authority_latency_test_interval")}
                    </div>
                    <Input
                      id="sys-max-auth-latency-int"
                      value={form.max_authority_latency_test_interval}
                      onChange={(event) => setFormField("max_authority_latency_test_interval", event.target.value)}
                    />
                  </div>

                  <div className="field-group">
                    <div style={{ display: "flex", alignItems: "center" }}>
                      <label className="field-label" htmlFor="sys-max-egress-int" style={{ margin: 0 }}>
                        出口 IP 更新检查间隔
                      </label>
                      {renderRestoreButton("max_egress_test_interval")}
                    </div>
                    <Input
                      id="sys-max-egress-int"
                      value={form.max_egress_test_interval}
                      onChange={(event) => setFormField("max_egress_test_interval", event.target.value)}
                    />
                  </div>

                  <div className="field-group">
                    <div style={{ display: "flex", alignItems: "center" }}>
                      <label className="field-label" htmlFor="sys-p2c-window" style={{ margin: 0 }}>
                        P2C 延迟衰减窗口
                      </label>
                      {renderRestoreButton("p2c_latency_window")}
                    </div>
                    <Input
                      id="sys-p2c-window"
                      value={form.p2c_latency_window}
                      onChange={(event) => setFormField("p2c_latency_window", event.target.value)}
                    />
                  </div>

                  <div className="field-group">
                    <div style={{ display: "flex", alignItems: "center" }}>
                      <label className="field-label" htmlFor="sys-decay-window" style={{ margin: 0 }}>
                        历史延迟衰减窗口
                      </label>
                      {renderRestoreButton("latency_decay_window")}
                    </div>
                    <Input
                      id="sys-decay-window"
                      value={form.latency_decay_window}
                      onChange={(event) => setFormField("latency_decay_window", event.target.value)}
                    />
                  </div>

                  <div className="field-group field-span-2">
                    <div style={{ display: "flex", alignItems: "center" }}>
                      <label className="field-label" htmlFor="sys-latency-authorities" style={{ margin: 0 }}>
                        延迟测试权威域名列表
                      </label>
                      {renderRestoreButton("latency_authorities_raw")}
                    </div>
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
                    <div style={{ display: "flex", alignItems: "center" }}>
                      <label className="field-label" htmlFor="sys-cache-flush-int" style={{ margin: 0 }}>
                        缓存异步刷盘间隔
                      </label>
                      {renderRestoreButton("cache_flush_interval")}
                    </div>
                    <Input
                      id="sys-cache-flush-int"
                      value={form.cache_flush_interval}
                      onChange={(event) => setFormField("cache_flush_interval", event.target.value)}
                    />
                  </div>

                  <div className="field-group">
                    <div style={{ display: "flex", alignItems: "center" }}>
                      <label className="field-label" htmlFor="sys-cache-threshold" style={{ margin: 0 }}>
                        缓存刷盘脏阈值
                      </label>
                      {renderRestoreButton("cache_flush_dirty_threshold")}
                    </div>
                    <Input
                      id="sys-cache-threshold"
                      type="number"
                      min={0}
                      value={form.cache_flush_dirty_threshold}
                      onChange={(event) => setFormField("cache_flush_dirty_threshold", event.target.value)}
                    />
                  </div>

                  <div className="field-group">
                    <div style={{ display: "flex", alignItems: "center" }}>
                      <label className="field-label" htmlFor="sys-evict-delay" style={{ margin: 0 }}>
                        临时节点驱逐延迟
                      </label>
                      {renderRestoreButton("ephemeral_node_evict_delay")}
                    </div>
                    <Input
                      id="sys-evict-delay"
                      value={form.ephemeral_node_evict_delay}
                      onChange={(event) => setFormField("ephemeral_node_evict_delay", event.target.value)}
                    />
                  </div>
                </div>
              </section>
            </Card>

            {envBaseline && (
              <Card className="syscfg-form-card platform-directory-card syscfg-static-card">
                <div className="detail-header">
                  <div>
                    <h3>静态配置</h3>
                    <p>来自环境变量和启动参数的只读配置。</p>
                  </div>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={async () => {
                      const result = await envConfigQuery.refetch();
                      if (result.data) showToast("success", "已加载最新静态配置");
                    }}
                    disabled={envConfigQuery.isFetching}
                  >
                    <RefreshCw size={16} className={envConfigQuery.isFetching ? "spin" : undefined} />
                    刷新
                  </Button>
                </div>

                <section className="syscfg-section">
                  <h4>目录与端口</h4>
                  <div className="form-grid">
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>数据缓存目录</label>
                      <Input readOnly disabled value={envBaseline.cache_dir} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>状态存储目录</label>
                      <Input readOnly disabled value={envBaseline.state_dir} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>日志保留目录</label>
                      <Input readOnly disabled value={envBaseline.log_dir} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>控制面 API 端口</label>
                      <Input readOnly disabled value={String(envBaseline.api_port)} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>正向代理端口</label>
                      <Input readOnly disabled value={String(envBaseline.forward_proxy_port)} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>混合反代端口</label>
                      <Input readOnly disabled value={String(envBaseline.reverse_proxy_port)} />
                    </div>
                  </div>
                </section>

                <section className="syscfg-section">
                  <h4>全局限额与性能调优</h4>
                  <div className="form-grid">
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>控制面最大请求体</label>
                      <Input readOnly disabled value={String(envBaseline.api_max_body_bytes)} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>最大延迟表条目数</label>
                      <Input readOnly disabled value={String(envBaseline.max_latency_table_entries)} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>节点拨测并发数</label>
                      <Input readOnly disabled value={String(envBaseline.probe_concurrency)} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>拨测超时时间</label>
                      <Input readOnly disabled value={envBaseline.probe_timeout} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>资源获取超时时间</label>
                      <Input readOnly disabled value={envBaseline.resource_fetch_timeout} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>GeoIP 更新计划</label>
                      <Input readOnly disabled value={envBaseline.geoip_update_schedule} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>代理传输最大空闲连接</label>
                      <Input readOnly disabled value={String(envBaseline.proxy_transport_max_idle_conns)} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>单主机最大空闲连接</label>
                      <Input readOnly disabled value={String(envBaseline.proxy_transport_max_idle_conns_per_host)} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>空闲连接超时时间</label>
                      <Input readOnly disabled value={envBaseline.proxy_transport_idle_conn_timeout} />
                    </div>
                  </div>
                </section>

                <section className="syscfg-section">
                  <h4>默认平台回退规则</h4>
                  <div className="form-grid">
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>默认粘性会话 TTL</label>
                      <Input readOnly disabled value={envBaseline.default_platform_sticky_ttl} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>默认节点分配策略</label>
                      <Input readOnly disabled value={envBaseline.default_platform_allocation_policy} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>默认反代不匹配行为</label>
                      <Input readOnly disabled value={envBaseline.default_platform_reverse_proxy_miss_action} />
                    </div>
                    <div className="field-group field-span-2">
                      <label className="field-label" style={{ margin: 0 }}>默认正则黑名单</label>
                      <Textarea readOnly disabled rows={3} value={envBaseline.default_platform_regex_filters?.join("\n") || "无"} />
                    </div>
                    <div className="field-group field-span-2">
                      <label className="field-label" style={{ margin: 0 }}>默认地区黑名单</label>
                      <Textarea readOnly disabled rows={2} value={envBaseline.default_platform_region_filters?.join(",") || "无"} />
                    </div>
                  </div>
                </section>

                <section className="syscfg-section">
                  <h4>请求日志落库</h4>
                  <div className="form-grid">
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>队列大小</label>
                      <Input readOnly disabled value={String(envBaseline.request_log_queue_size)} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>落盘批大小</label>
                      <Input readOnly disabled value={String(envBaseline.request_log_queue_flush_batch_size)} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>落盘间隔</label>
                      <Input readOnly disabled value={envBaseline.request_log_queue_flush_interval} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>数据库保留阈值</label>
                      <Input readOnly disabled value={envBaseline.request_log_db_max_mb + " MB"} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>数据库旧分片保留数</label>
                      <Input readOnly disabled value={String(envBaseline.request_log_db_retain_count)} />
                    </div>
                  </div>
                </section>

                <section className="syscfg-section">
                  <h4>可观测性指标</h4>
                  <div className="form-grid">
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>吞吐量抽样间隔</label>
                      <Input readOnly disabled value={envBaseline.metric_throughput_interval_seconds + "s"} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>吞吐量保留时间</label>
                      <Input readOnly disabled value={envBaseline.metric_throughput_retention_seconds + "s"} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>连接数抽样间隔</label>
                      <Input readOnly disabled value={envBaseline.metric_connections_interval_seconds + "s"} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>连接数保留时间</label>
                      <Input readOnly disabled value={envBaseline.metric_connections_retention_seconds + "s"} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>租期与连接指标分桶数</label>
                      <Input readOnly disabled value={envBaseline.metric_bucket_seconds + "s"} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>租期抽样间隔</label>
                      <Input readOnly disabled value={envBaseline.metric_leases_interval_seconds + "s"} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>租期保留时间</label>
                      <Input readOnly disabled value={envBaseline.metric_leases_retention_seconds + "s"} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>延迟统计桶宽</label>
                      <Input readOnly disabled value={envBaseline.metric_latency_bin_width_ms + "ms"} />
                    </div>
                    <div className="field-group">
                      <label className="field-label" style={{ margin: 0 }}>延迟统计截断值</label>
                      <Input readOnly disabled value={envBaseline.metric_latency_bin_overflow_ms + "ms"} />
                    </div>
                  </div>
                </section>

                <section className="syscfg-section">
                  <h4>服务鉴权状态</h4>
                  <div className="syscfg-checkbox-grid" style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: "16px" }}>
                    <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", background: "var(--surface-sunken, rgba(0,0,0,0.02))", padding: "12px 16px", borderRadius: "8px", border: "1px solid var(--border)", opacity: 0.7 }}>
                      <span className="field-label" style={{ margin: 0, fontWeight: 500 }}>已配置 Admin Token</span>
                      <Switch checked={envBaseline.admin_token_set} disabled />
                    </div>
                    <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", background: "var(--surface-sunken, rgba(0,0,0,0.02))", padding: "12px 16px", borderRadius: "8px", border: "1px solid var(--border)", opacity: 0.7 }}>
                      <span className="field-label" style={{ margin: 0, fontWeight: 500 }}>已配置 Proxy Token</span>
                      <Switch checked={envBaseline.proxy_token_set} disabled />
                    </div>
                  </div>
                </section>
              </Card>
            )}
          </div>

          <div className="syscfg-side">
            <Card className="syscfg-summary-card platform-directory-card">
              <div className="detail-header">
                <div>
                  <h3>变更摘要</h3>
                  <p>{hasUnsavedChanges ? `${changedKeys.length} 项待提交` : "当前无未保存改动"}</p>
                </div>
              </div>

              {parsedResult.error && customPatchText === null ? (
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

              <div style={{ marginTop: "16px" }}>
                <p style={{ fontSize: "12px", color: "var(--text-muted)", marginBottom: "8px" }}>
                  PATCH Preview {customPatchText !== null && <span style={{ color: "var(--primary)" }}>(已手动修改)</span>}
                </p>
                <Textarea
                  value={displayedPatchText}
                  onChange={handlePatchEdit}
                  rows={10}
                  style={{ fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace', fontSize: "12px", width: "100%", resize: "vertical", backgroundColor: "var(--surface-sunken)", border: "1px solid var(--border)", borderRadius: "var(--radius)" }}
                  spellCheck={false}
                />
              </div>

              <div className="detail-actions" style={{ justifyContent: "flex-end", marginTop: "16px" }}>
                <Button
                  onClick={() => void saveMutation.mutateAsync()}
                  disabled={isSaveDisabled}
                >
                  <Save size={14} />
                  {saveMutation.isPending ? "保存中..." : "保存配置"}
                </Button>
                <Button variant="ghost" onClick={resetDraft} disabled={(customPatchText === null && !hasUnsavedChanges) || saveMutation.isPending}>
                  <RotateCcw size={14} />
                  重置草稿
                </Button>
              </div>
            </Card>
          </div>
        </div>
      )}
    </section>
  );
}
