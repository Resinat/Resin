import { useMutation, useQuery } from "@tanstack/react-query";
import { AlertTriangle, ArrowDownToLine, Database, RefreshCw, Search, Sparkles } from "lucide-react";
import { useMemo, useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { ApiError } from "../../lib/api-client";
import { formatDateTime } from "../../lib/time";
import { getRegionName } from "../nodes/regions";
import { getGeoIPStatus, lookupIP, updateGeoIPNow } from "./api";
import type { GeoIPLookupResult } from "./types";

function getFlagEmoji(countryCode: string) {
  if (!countryCode || countryCode.length !== 2) return "";
  const codePoints = countryCode
    .toUpperCase()
    .split("")
    .map((char) => 127397 + char.charCodeAt(0));
  return String.fromCodePoint(...codePoints);
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

function statusVariant(hasValue: boolean): "success" | "warning" {
  return hasValue ? "success" : "warning";
}

export function GeoIPPage() {
  const [singleIP, setSingleIP] = useState("");
  const [singleResult, setSingleResult] = useState<GeoIPLookupResult | null>(null);
  const { toasts, showToast, dismissToast } = useToast();

  const statusQuery = useQuery({
    queryKey: ["geoip-status"],
    queryFn: getGeoIPStatus,
    refetchInterval: 60_000,
  });

  const lookupMutation = useMutation({
    mutationFn: async () => {
      const ip = singleIP.trim();
      if (!ip) {
        throw new Error("请输入 IP 地址");
      }
      return lookupIP(ip);
    },
    onSuccess: (result) => {
      setSingleResult(result);
    },
    onError: (error) => {
      showToast("error", fromApiError(error));
    },
  });

  const updateMutation = useMutation({
    mutationFn: updateGeoIPNow,
    onSuccess: async () => {
      await statusQuery.refetch();
      showToast("success", "GeoIP 数据库更新任务已执行");
    },
    onError: (error) => {
      showToast("error", fromApiError(error));
    },
  });

  const status = statusQuery.data;
  const hasDBTime = Boolean(status?.db_mtime);
  const hasNextSchedule = Boolean(status?.next_scheduled_update);

  const singleRegion = useMemo(() => {
    if (!singleResult || !singleResult.region) {
      return "(empty)";
    }
    const code = singleResult.region.toUpperCase();
    const name = getRegionName(code);
    if (!name) {
      return code;
    }
    const emoji = getFlagEmoji(code);
    return `${emoji} ${code} ${name}`;
  }, [singleResult]);

  return (
    <section className="geoip-page">
      <header className="module-header">
        <div>
          <h2>资源</h2>
          <p className="module-description">GeoIP 状态、单 IP 查询与数据库更新的运维工作台。</p>
        </div>
      </header>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <Card className="platform-cards-container platform-directory-card" style={{ display: "flex", flexDirection: "column", gap: "1rem" }}>
        <div className="list-card-header">
          <div>
            <h3>GeoIP</h3>
            <p>包含数据库状态与单 IP 查询模块</p>
          </div>
          <Button variant="secondary" size="sm" onClick={() => void statusQuery.refetch()} disabled={statusQuery.isFetching}>
            <RefreshCw size={16} className={statusQuery.isFetching ? "spin" : undefined} />
            刷新
          </Button>
        </div>

        <div className="geoip-layout">
          <Card className="geoip-status-card">
            <div className="detail-header">
              <div>
                <h3>数据库状态</h3>
                <p>当前加载时间与下一次计划更新时间</p>
              </div>
              <Database size={16} />
            </div>

            {statusQuery.isError ? (
              <div className="callout callout-error">
                <AlertTriangle size={14} />
                <span>{fromApiError(statusQuery.error)}</span>
              </div>
            ) : null}

            <div className="geoip-status-grid">
              <div className="geoip-kv">
                <span>DB MTime</span>
                <p>{hasDBTime ? formatDateTime(status?.db_mtime || "") : "-"}</p>
              </div>
              <div className="geoip-kv">
                <span>Next Scheduled Update</span>
                <p>{hasNextSchedule ? formatDateTime(status?.next_scheduled_update || "") : "-"}</p>
              </div>
            </div>

            <div className="geoip-actions">
              <Badge variant={statusVariant(hasDBTime)}>
                {hasDBTime ? "数据库已加载" : "数据库未加载"}
              </Badge>
              <Button size="sm" variant="secondary" onClick={() => void updateMutation.mutateAsync()} disabled={updateMutation.isPending}>
                <ArrowDownToLine size={14} className={updateMutation.isPending ? "spin" : undefined} />
                {updateMutation.isPending ? "更新中..." : "立即更新"}
              </Button>
            </div>
          </Card>

          <Card className="geoip-single-card">
            <div className="detail-header">
              <div>
                <h3>单 IP 查询</h3>
                <p>使用 GET `/api/v1/geoip/lookup`</p>
              </div>
            </div>

            <div className="form-grid single-column" style={{ marginTop: "16px", marginBottom: "16px" }}>
              <div className="field-group">
                <div style={{ display: "flex", gap: "8px" }}>
                  <Input
                    id="geoip-single-ip"
                    placeholder="输入 IP 地址例如 8.8.8.8"
                    value={singleIP}
                    onChange={(event) => setSingleIP(event.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") {
                        e.preventDefault();
                        void lookupMutation.mutateAsync();
                      }
                    }}
                  />
                  <Button
                    variant="secondary"
                    onClick={() => void lookupMutation.mutateAsync()}
                    disabled={lookupMutation.isPending}
                    style={{ padding: "0 12px" }}
                    title="查询"
                  >
                    <Search size={16} className={lookupMutation.isPending ? "spin" : undefined} />
                  </Button>
                </div>
              </div>
            </div>

            {singleResult ? (
              <div className="geoip-result">
                <div>
                  <span>IP</span>
                  <p>{singleResult.ip}</p>
                </div>
                <div>
                  <span>Region</span>
                  <p>{singleRegion}</p>
                </div>
              </div>
            ) : (
              <div className="empty-box">
                <Sparkles size={16} />
                <p>输入 IP 执行查询</p>
              </div>
            )}
          </Card>
        </div>
      </Card>
    </section>
  );
}
