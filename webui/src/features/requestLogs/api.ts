import { apiRequest } from "../../lib/api-client";
import type {
  RequestLogItem,
  RequestLogListFilters,
  RequestLogListResponse,
  RequestLogPayloads,
} from "./types";

const basePath = "/api/v1/request-logs";

function appendIfPresent(query: URLSearchParams, key: string, value: string | number | boolean | undefined) {
  if (value === undefined || value === null) {
    return;
  }
  if (typeof value === "string") {
    const trimmed = value.trim();
    if (!trimmed) {
      return;
    }
    query.set(key, trimmed);
    return;
  }
  query.set(key, String(value));
}

export async function listRequestLogs(filters: RequestLogListFilters): Promise<RequestLogListResponse> {
  const query = new URLSearchParams();
  appendIfPresent(query, "from", filters.from);
  appendIfPresent(query, "to", filters.to);
  appendIfPresent(query, "platform_name", filters.platform_name);
  appendIfPresent(query, "account", filters.account);
  appendIfPresent(query, "target_host", filters.target_host);
  appendIfPresent(query, "egress_ip", filters.egress_ip);
  appendIfPresent(query, "proxy_type", filters.proxy_type);
  appendIfPresent(query, "net_ok", filters.net_ok);
  appendIfPresent(query, "http_status", filters.http_status);
  appendIfPresent(query, "cursor", filters.cursor);
  appendIfPresent(query, "limit", filters.limit);
  appendIfPresent(query, "fuzzy", filters.fuzzy);

  return apiRequest<RequestLogListResponse>(`${basePath}?${query.toString()}`);
}

export async function getRequestLog(id: string): Promise<RequestLogItem> {
  return apiRequest<RequestLogItem>(`${basePath}/${id}`);
}

export async function getRequestLogPayloads(id: string): Promise<RequestLogPayloads> {
  return apiRequest<RequestLogPayloads>(`${basePath}/${id}/payloads`);
}
