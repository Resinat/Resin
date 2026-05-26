import { apiRequest } from "../../lib/api-client";
import type {
  EgressProbeResult,
  LatencyProbeResult,
  NodeLease,
  NodeListQuery,
  NodeSummary,
  PageResponse,
} from "./types";

const basePath = "/api/v1/nodes";

type ApiNodeSummary = Omit<NodeSummary, "tags" | "lease_count"> & {
  tags?: NodeSummary["tags"] | null;
  enabled?: boolean | null;
  manually_disabled?: boolean | null;
  display_tag?: string | null;
  last_error?: string | null;
  circuit_open_since?: string | null;
  egress_ip?: string | null;
  reference_latency_ms?: number | null;
  region?: string | null;
  last_egress_update?: string | null;
  last_latency_probe_attempt?: string | null;
  last_authority_latency_probe_attempt?: string | null;
  last_egress_update_attempt?: string | null;
  lease_count?: number | null;
};

function normalizeNode(raw: ApiNodeSummary): NodeSummary {
  const { reference_latency_ms, ...rest } = raw;
  const normalized: NodeSummary = {
    ...rest,
    enabled: raw.enabled !== false,
    manually_disabled: Boolean(raw.manually_disabled),
    display_tag: raw.display_tag || "",
    tags: Array.isArray(raw.tags) ? raw.tags : [],
    last_error: raw.last_error || "",
    circuit_open_since: raw.circuit_open_since || "",
    egress_ip: raw.egress_ip || "",
    region: raw.region || "",
    last_egress_update: raw.last_egress_update || "",
    last_latency_probe_attempt: raw.last_latency_probe_attempt || "",
    last_authority_latency_probe_attempt: raw.last_authority_latency_probe_attempt || "",
    last_egress_update_attempt: raw.last_egress_update_attempt || "",
    lease_count: typeof raw.lease_count === "number" ? raw.lease_count : 0,
  };

  // Backend uses `omitempty`; field missing means "no reference latency".
  if (typeof reference_latency_ms === "number") {
    normalized.reference_latency_ms = reference_latency_ms;
  }

  return normalized;
}

export async function listNodes(filters: NodeListQuery): Promise<PageResponse<NodeSummary>> {
  const query = new URLSearchParams({
    limit: String(filters.limit ?? 50),
    offset: String(filters.offset ?? 0),
    sort_by: filters.sort_by || "tag",
    sort_order: filters.sort_order || "asc",
  });

  const appendIfNotEmpty = (key: string, value?: string) => {
    if (!value) {
      return;
    }
    const trimmed = value.trim();
    if (!trimmed) {
      return;
    }
    query.set(key, trimmed);
  };

  appendIfNotEmpty("platform_id", filters.platform_id);
  appendIfNotEmpty("subscription_id", filters.subscription_id);
  appendIfNotEmpty("tag_keyword", filters.tag_keyword);
  appendIfNotEmpty("region", filters.region?.toLowerCase());
  appendIfNotEmpty("egress_ip", filters.egress_ip);
  appendIfNotEmpty("probed_since", filters.probed_since);

  if (filters.circuit_open !== undefined) {
    query.set("circuit_open", String(filters.circuit_open));
  }
  if (filters.has_outbound !== undefined) {
    query.set("has_outbound", String(filters.has_outbound));
  }
  if (filters.enabled !== undefined) {
    query.set("enabled", String(filters.enabled));
  }
  if (filters.manually_disabled !== undefined) {
    query.set("manually_disabled", String(filters.manually_disabled));
  }

  const data = await apiRequest<PageResponse<ApiNodeSummary>>(`${basePath}?${query.toString()}`);
  return {
    ...data,
    items: data.items.map(normalizeNode),
  };
}

export async function getNode(hash: string): Promise<NodeSummary> {
  const data = await apiRequest<ApiNodeSummary>(`${basePath}/${hash}`);
  return normalizeNode(data);
}

export async function probeEgress(hash: string): Promise<EgressProbeResult> {
  return apiRequest<EgressProbeResult>(`${basePath}/${hash}/actions/probe-egress`, {
    method: "POST",
  });
}

export async function probeLatency(hash: string): Promise<LatencyProbeResult> {
  return apiRequest<LatencyProbeResult>(`${basePath}/${hash}/actions/probe-latency`, {
    method: "POST",
  });
}

export async function listNodeLeases(hash: string, platformId?: string): Promise<NodeLease[]> {
  const query = new URLSearchParams();
  const pid = platformId?.trim();
  if (pid) {
    query.set("platform_id", pid);
  }
  const qs = query.toString();
  const path = qs ? `${basePath}/${hash}/leases?${qs}` : `${basePath}/${hash}/leases`;
  const data = await apiRequest<NodeLease[] | null>(path);
  return Array.isArray(data) ? data : [];
}

export type DisableNodeResult = {
  released_lease_count: number;
};

export async function disableNode(hash: string): Promise<DisableNodeResult> {
  return apiRequest<DisableNodeResult>(`${basePath}/${hash}/actions/disable`, {
    method: "POST",
  });
}

export async function enableNode(hash: string): Promise<DisableNodeResult> {
  return apiRequest<DisableNodeResult>(`${basePath}/${hash}/actions/enable`, {
    method: "POST",
  });
}
