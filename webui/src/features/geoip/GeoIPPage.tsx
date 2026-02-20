import { useMutation, useQuery } from "@tanstack/react-query";
import { AlertTriangle, Database, Eraser, Radar, RefreshCw, Search, Sparkles } from "lucide-react";
import { useMemo, useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { Textarea } from "../../components/ui/Textarea";
import { ApiError } from "../../lib/api-client";
import { formatDateTime } from "../../lib/time";
import { getGeoIPStatus, lookupIP, lookupIPBatch, updateGeoIPNow } from "./api";
import type { GeoIPLookupResult } from "./types";

const EMPTY_RESULTS: GeoIPLookupResult[] = [];

function fromApiError(error: unknown): string {
  if (error instanceof ApiError) {
    return `${error.code}: ${error.message}`;
  }
  if (error instanceof Error) {
    return error.message;
  }
  return "未知错误";
}

function parseBatchIPs(raw: string): string[] {
  const items = raw
    .split(/[\s,]+/)
    .map((item) => item.trim())
    .filter(Boolean);

  return Array.from(new Set(items));
}

function statusVariant(hasValue: boolean): "success" | "warning" {
  return hasValue ? "success" : "warning";
}

export function GeoIPPage() {
  const [singleIP, setSingleIP] = useState("");
  const [singleResult, setSingleResult] = useState<GeoIPLookupResult | null>(null);
  const [batchRaw, setBatchRaw] = useState("");
  const [batchResults, setBatchResults] = useState<GeoIPLookupResult[]>(EMPTY_RESULTS);
  const [message, setMessage] = useState<{ tone: "success" | "error"; text: string } | null>(null);

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
      setMessage(null);
    },
    onError: (error) => {
      setMessage({ tone: "error", text: fromApiError(error) });
    },
  });

  const batchLookupMutation = useMutation({
    mutationFn: async () => {
      const ips = parseBatchIPs(batchRaw);
      if (!ips.length) {
        throw new Error("请输入至少一个 IP");
      }
      return lookupIPBatch(ips);
    },
    onSuccess: (results) => {
      setBatchResults(results);
      setMessage({ tone: "success", text: `批量查询完成：${results.length} 条结果` });
    },
    onError: (error) => {
      setMessage({ tone: "error", text: fromApiError(error) });
    },
  });

  const updateMutation = useMutation({
    mutationFn: updateGeoIPNow,
    onSuccess: async () => {
      await statusQuery.refetch();
      setMessage({ tone: "success", text: "GeoIP 数据库更新任务已执行" });
    },
    onError: (error) => {
      setMessage({ tone: "error", text: fromApiError(error) });
    },
  });

  const status = statusQuery.data;
  const hasDBTime = Boolean(status?.db_mtime);
  const hasNextSchedule = Boolean(status?.next_scheduled_update);

  const singleRegion = useMemo(() => {
    if (!singleResult) {
      return "";
    }
    return singleResult.region || "(empty)";
  }, [singleResult]);

  return (
    <section className="geoip-page">
      <header className="module-header">
        <div>
          <h2>GeoIP</h2>
          <p className="module-description">GeoIP 状态、单 IP 与批量查询、数据库立即更新的运维工作台。</p>
        </div>
        <Button onClick={() => void statusQuery.refetch()} disabled={statusQuery.isFetching}>
          <RefreshCw size={16} className={statusQuery.isFetching ? "spin" : undefined} />
          刷新状态
        </Button>
      </header>

      {message ? (
        <div className={message.tone === "success" ? "callout callout-success" : "callout callout-error"}>
          {message.text}
        </div>
      ) : null}

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
            <Button onClick={() => void updateMutation.mutateAsync()} disabled={updateMutation.isPending}>
              <Radar size={14} className={updateMutation.isPending ? "spin" : undefined} />
              {updateMutation.isPending ? "更新中..." : "立即更新数据库"}
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

          <div className="form-grid single-column">
            <div className="field-group">
              <label className="field-label" htmlFor="geoip-single-ip">
                IP 地址
              </label>
              <Input
                id="geoip-single-ip"
                placeholder="例如 8.8.8.8"
                value={singleIP}
                onChange={(event) => setSingleIP(event.target.value)}
              />
            </div>
          </div>

          <div className="detail-actions">
            <Button variant="secondary" onClick={() => void lookupMutation.mutateAsync()} disabled={lookupMutation.isPending}>
              <Search size={14} />
              {lookupMutation.isPending ? "查询中..." : "查询"}
            </Button>
            <Button
              variant="ghost"
              onClick={() => {
                setSingleIP("");
                setSingleResult(null);
              }}
            >
              <Eraser size={14} />
              清空
            </Button>
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

      <Card className="geoip-batch-card">
        <div className="detail-header">
          <div>
            <h3>批量查询</h3>
            <p>使用 POST `/api/v1/geoip/lookup`，支持空格 / 换行 / 逗号分隔</p>
          </div>
        </div>

        <div className="form-grid single-column">
          <div className="field-group">
            <label className="field-label" htmlFor="geoip-batch-input">
              IP 列表
            </label>
            <Textarea
              id="geoip-batch-input"
              rows={5}
              placeholder={"1.1.1.1\n8.8.8.8\n223.5.5.5"}
              value={batchRaw}
              onChange={(event) => setBatchRaw(event.target.value)}
            />
          </div>
        </div>

        <div className="detail-actions">
          <Button variant="secondary" onClick={() => void batchLookupMutation.mutateAsync()} disabled={batchLookupMutation.isPending}>
            <Search size={14} />
            {batchLookupMutation.isPending ? "查询中..." : "执行批量查询"}
          </Button>
          <Button
            variant="ghost"
            onClick={() => {
              setBatchRaw("");
              setBatchResults(EMPTY_RESULTS);
            }}
          >
            <Eraser size={14} />
            清空
          </Button>
        </div>

        {!batchResults.length ? (
          <div className="empty-box">
            <Sparkles size={16} />
            <p>暂无批量查询结果</p>
          </div>
        ) : (
          <div className="geoip-table-wrap">
            <table className="geoip-table">
              <thead>
                <tr>
                  <th>IP</th>
                  <th>Region</th>
                </tr>
              </thead>
              <tbody>
                {batchResults.map((item) => (
                  <tr key={item.ip}>
                    <td>{item.ip}</td>
                    <td>{item.region || "(empty)"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </section>
  );
}
