import { Check, Copy, X } from "lucide-react";
import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { useI18n } from "../../i18n";
import { copyText } from "../../lib/clipboard";
import { formatDateTime, formatRelativeTime } from "../../lib/time";
import {
  displayableReferenceLatencyMs,
  firstTag,
  formatLatency,
  getNodeDisplayStatus,
  referenceLatencyColor,
  regionToFlag,
} from "./nodeFormat";
import type { NodeSummary } from "./types";

type Props = {
  node: NodeSummary;
  onClose: () => void;
  onProbeEgress: (hash: string) => void;
  onProbeLatency: (hash: string) => void;
  isEgressProbePending: (hash: string) => boolean;
  isLatencyProbePending: (hash: string) => boolean;
};

function CopyButton({ value }: { value: string }) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);

  const handleCopy = async () => {
    try {
      await copyText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  };

  return (
    <Button variant="secondary" size="sm" onClick={() => void handleCopy()} className="node-detail-copy-btn">
      {copied ? <Check size={14} /> : <Copy size={14} />}
      {copied ? t("已复制") : t("复制")}
    </Button>
  );
}

export function NodeDetailDrawer({
  node,
  onClose,
  onProbeEgress,
  onProbeLatency,
  isEgressProbePending,
  isLatencyProbePending,
}: Props) {
  const { t } = useI18n();

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        onClose();
      }
    };

    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [onClose]);

  const title = firstTag(node);
  const egressPending = isEgressProbePending(node.node_hash);
  const latencyPending = isLatencyProbePending(node.node_hash);
  const outboundJSON = node.outbound == null ? "" : JSON.stringify(node.outbound, null, 2) ?? "";
  const proxyUrls = node.proxy_urls ?? [];

  return createPortal(
    <div
      className="drawer-overlay"
      role="dialog"
      aria-modal="true"
      aria-label={t("节点详情 {{name}}", { name: title })}
      onClick={onClose}
    >
      <Card className="drawer-panel" onClick={(event) => event.stopPropagation()}>
        <div className="drawer-header">
          <div>
            <h3>{title}</h3>
            <p>{node.node_hash}</p>
          </div>
          <div className="drawer-header-actions">
            <Button variant="ghost" size="sm" aria-label={t("关闭详情面板")} onClick={onClose}>
              <X size={16} />
            </Button>
          </div>
        </div>

        <div className="platform-drawer-layout">
          <section className="platform-drawer-section">
            <div className="platform-drawer-section-head">
              <h4>{t("节点状态")}</h4>
              <p>{t("节点的网络出口、探测状态以及失败历史。")}</p>
            </div>

            <div className="stats-grid">
              <div>
                <span>{t("创建时间")}</span>
                <p>{formatDateTime(node.created_at)}</p>
              </div>
              <div>
                <span>{t("连续失败")}</span>
                <p>{!node.has_outbound ? "-" : node.failure_count}</p>
              </div>
              <div>
                <span>{t("状态")}</span>
                <div>
                  {(() => {
                    const status = getNodeDisplayStatus(node);
                    return (
                      <div style={{ display: "flex", alignItems: "baseline", gap: "4px", flexWrap: "wrap" }}>
                        {status === "manually_disabled" ? (
                          <Badge variant="danger">{t("手动禁用")}</Badge>
                        ) : status === "error" ? (
                          <Badge variant="danger">{t("错误")}</Badge>
                        ) : status === "disabled" ? (
                          <Badge variant="neutral">{t("禁用")}</Badge>
                        ) : status === "pending_test" ? (
                          <Badge variant="muted">{t("待测")}</Badge>
                        ) : status === "circuit_open" ? (
                          <Badge variant="warning">{t("熔断")}</Badge>
                        ) : (
                          <Badge variant="success">{t("健康")}</Badge>
                        )}
                        {(status === "circuit_open" || status === "pending_test") && node.circuit_open_since ? (
                          <span style={{ fontSize: "11px", color: "var(--text-muted)", fontWeight: "normal" }}>
                            ({formatRelativeTime(node.circuit_open_since)})
                          </span>
                        ) : null}
                      </div>
                    );
                  })()}
                </div>
              </div>
              <div>
                <span>{t("出口 / 区域")}</span>
                <p>
                  {node.egress_ip || "-"} / {regionToFlag(node.region)}
                </p>
              </div>
              <div>
                <span>{t("参考延迟")}</span>
                {(() => {
                  const latencyMs = displayableReferenceLatencyMs(node);
                  if (latencyMs === null) {
                    return <p>-</p>;
                  }
                  return <p style={{ color: referenceLatencyColor(latencyMs) }}>{formatLatency(latencyMs)}</p>;
                })()}
              </div>
              <div>
                <span>{t("上次探测")}</span>
                <p>{formatDateTime(node.last_latency_probe_attempt || "")}</p>
              </div>
            </div>

            {node.last_error ? (
              <div className="callout callout-error">{t("最近错误：{{message}}", { message: node.last_error })}</div>
            ) : null}
          </section>

          <section className="platform-drawer-section">
            <div className="platform-drawer-section-head">
              <h4>{t("节点别名")}</h4>
            </div>
            {!node.tags.length ? (
              <p className="muted">{t("无节点名信息")}</p>
            ) : (
              <div className="tag-list">
                {node.tags.map((tag) => (
                  <div key={`${tag.subscription_id}:${tag.tag}`} className="tag-item">
                    <p>{tag.tag}</p>
                    <span>{tag.subscription_name}</span>
                    <code>{tag.subscription_id}</code>
                  </div>
                ))}
              </div>
            )}
          </section>

          {outboundJSON ? (
            <section className="platform-drawer-section">
              <div className="platform-drawer-section-head node-detail-section-head-row">
                <div>
                  <h4>{t("Outbound JSON")}</h4>
                  <p>{t("单个 outbound 对象，可粘贴到 sing-box 配置的 outbounds 数组中。")}</p>
                </div>
                <CopyButton value={outboundJSON} />
              </div>
              <pre className="logs-payload-box node-outbound-json">{outboundJSON}</pre>
            </section>
          ) : null}

          {proxyUrls.length ? (
            <section className="platform-drawer-section">
              <div className="platform-drawer-section-head">
                <h4>{t("可复制代理地址")}</h4>
                <p>{t("已按后端可安全拼接的协议生成。")}</p>
              </div>
              <div className="node-proxy-url-list">
                {proxyUrls.map((item) => (
                  <div key={`${item.type}:${item.url}`} className="node-proxy-url-item">
                    <Badge variant="neutral">{item.type.toUpperCase()}</Badge>
                    <code title={item.url}>{item.url}</code>
                    <CopyButton value={item.url} />
                  </div>
                ))}
              </div>
            </section>
          ) : null}

          <section className="platform-drawer-section platform-ops-section">
            <div className="platform-drawer-section-head">
              <h4>{t("运维操作")}</h4>
            </div>
            <div className="platform-ops-list">
              <div className="platform-op-item">
                <div className="platform-op-copy">
                  <h5>{t("出口探测")}</h5>
                  <p className="platform-op-hint">{t("检查节点当前出口 IP。")}</p>
                </div>
                <Button variant="secondary" onClick={() => onProbeEgress(node.node_hash)} disabled={egressPending}>
                  {egressPending ? t("探测中...") : t("触发出口探测")}
                </Button>
              </div>
              <div className="platform-op-item">
                <div className="platform-op-copy">
                  <h5>{t("延迟探测")}</h5>
                  <p className="platform-op-hint">{t("检测节点网络延迟。")}</p>
                </div>
                <Button variant="secondary" onClick={() => onProbeLatency(node.node_hash)} disabled={latencyPending}>
                  {latencyPending ? t("探测中...") : t("触发延迟探测")}
                </Button>
              </div>
            </div>
          </section>
        </div>
      </Card>
    </div>,
    document.body,
  );
}
