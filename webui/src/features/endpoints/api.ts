import { apiRequest } from "../../lib/api-client";
import type { Endpoint, EndpointInput, EndpointListResponse, EndpointPatch } from "./types";

const basePath = "/api/v1/endpoints";

function normalizeEndpoint(raw: Endpoint): Endpoint {
  return {
    id: raw.id || "",
    port: Number(raw.port) || 0,
    enabled: Boolean(raw.enabled),
    allow_management: Boolean(raw.allow_management),
    allow_proxy: Boolean(raw.allow_proxy),
    require_proxy_auth_info: Boolean(raw.require_proxy_auth_info),
    allow_http_forward: Boolean(raw.allow_http_forward),
    allow_http_reverse: Boolean(raw.allow_http_reverse),
    allow_socks5: Boolean(raw.allow_socks5),
    source: raw.source || "database",
    read_only: Boolean(raw.read_only),
    status: raw.status || "inactive",
    last_error: raw.last_error || "",
    created_at: raw.created_at || "",
    updated_at: raw.updated_at || "",
  };
}

export type ListEndpointsInput = {
  limit?: number;
  offset?: number;
};

export async function listEndpoints(input: ListEndpointsInput = {}): Promise<EndpointListResponse> {
  const query = new URLSearchParams({
    limit: String(input.limit ?? 20),
    offset: String(input.offset ?? 0),
  });
  const data = await apiRequest<EndpointListResponse>(`${basePath}?${query.toString()}`);
  const items = Array.isArray(data.items) ? data.items.map(normalizeEndpoint) : [];
  return {
    items,
    total: Number.isFinite(data.total) ? data.total : items.length,
    limit: Number.isFinite(data.limit) ? data.limit : input.limit ?? 20,
    offset: Number.isFinite(data.offset) ? data.offset : input.offset ?? 0,
  };
}

export async function createEndpoint(input: EndpointInput): Promise<Endpoint> {
  const data = await apiRequest<Endpoint>(basePath, {
    method: "POST",
    body: input,
  });
  return normalizeEndpoint(data);
}

export async function updateEndpoint(id: string, input: EndpointPatch): Promise<Endpoint> {
  const data = await apiRequest<Endpoint>(`${basePath}/${encodeURIComponent(id)}`, {
    method: "PATCH",
    body: input,
  });
  return normalizeEndpoint(data);
}

export async function deleteEndpoint(id: string): Promise<void> {
  await apiRequest<void>(`${basePath}/${encodeURIComponent(id)}`, {
    method: "DELETE",
  });
}
