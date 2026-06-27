import { apiRequest } from "../../lib/api-client";
import type {
  PageResponse,
  Subscription,
  SubscriptionCreateInput,
  SubscriptionPageResponse,
  SubscriptionSummary,
  SubscriptionUpdateInput,
} from "./types";

const basePath = "/api/v1/subscriptions";
const metricsBasePath = "/api/v1/metrics";

type ApiSubscription = Omit<Subscription, "last_checked" | "last_updated" | "last_error"> & {
  source_type?: "remote" | "local";
  content?: string;
  last_checked?: string | null;
  last_updated?: string | null;
  last_error?: string | null;
  usage?: Subscription["usage"] | null;
};

type ApiSubscriptionPage = PageResponse<ApiSubscription> & {
  summary?: Partial<SubscriptionSummary> | null;
};

type ApiHistoryTrafficResponse = {
  items?: Array<{
    ingress_bytes?: number | null;
    egress_bytes?: number | null;
  }> | null;
};

function toNumber(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function normalizeSubscription(raw: ApiSubscription): Subscription {
  return {
    ...raw,
    source_type: raw.source_type ?? "remote",
    content: raw.content ?? "",
    last_checked: raw.last_checked || "",
    last_updated: raw.last_updated || "",
    last_error: raw.last_error || "",
    usage: raw.usage ?? undefined,
  };
}

function normalizeSubscriptionSummary(raw: Partial<SubscriptionSummary> | null | undefined): SubscriptionSummary {
  return {
    enabled_count: toNumber(raw?.enabled_count),
    disabled_count: toNumber(raw?.disabled_count),
    usage_used_bytes: toNumber(raw?.usage_used_bytes),
    usage_total_bytes: toNumber(raw?.usage_total_bytes),
    usage_remaining_bytes: toNumber(raw?.usage_remaining_bytes),
    healthy_node_count: toNumber(raw?.healthy_node_count),
    node_count: toNumber(raw?.node_count),
  };
}

function normalizeSubscriptionPage(raw: ApiSubscriptionPage): SubscriptionPageResponse {
  return {
    ...raw,
    items: raw.items.map(normalizeSubscription),
    summary: normalizeSubscriptionSummary(raw.summary),
  };
}

export type ListSubscriptionsInput = {
  enabled?: boolean;
  limit?: number;
  offset?: number;
  keyword?: string;
};

export async function listSubscriptions(input: ListSubscriptionsInput = {}): Promise<SubscriptionPageResponse> {
  const query = new URLSearchParams({
    limit: String(input.limit ?? 50),
    offset: String(input.offset ?? 0),
    sort_by: "status",
    sort_order: "asc",
  });

  if (input.enabled !== undefined) {
    query.set("enabled", String(input.enabled));
  }
  const keyword = input.keyword?.trim();
  if (keyword) {
    query.set("keyword", keyword);
  }

  const data = await apiRequest<ApiSubscriptionPage>(`${basePath}?${query.toString()}`);
  return normalizeSubscriptionPage(data);
}

export async function getHistoryTrafficTotal(input: { from: string; to: string }): Promise<number> {
  const query = new URLSearchParams({ from: input.from, to: input.to });
  const data = await apiRequest<ApiHistoryTrafficResponse>(`${metricsBasePath}/history/traffic?${query.toString()}`);
  return (data.items ?? []).reduce((sum, item) => sum + toNumber(item.ingress_bytes) + toNumber(item.egress_bytes), 0);
}

export async function createSubscription(input: SubscriptionCreateInput): Promise<Subscription> {
  const data = await apiRequest<ApiSubscription>(basePath, {
    method: "POST",
    body: input,
  });
  return normalizeSubscription(data);
}

export async function updateSubscription(id: string, input: SubscriptionUpdateInput): Promise<Subscription> {
  const data = await apiRequest<ApiSubscription>(`${basePath}/${id}`, {
    method: "PATCH",
    body: input,
  });
  return normalizeSubscription(data);
}

export async function deleteSubscription(id: string): Promise<void> {
  await apiRequest<void>(`${basePath}/${id}`, {
    method: "DELETE",
  });
}

export async function refreshSubscription(id: string): Promise<void> {
  await apiRequest<{ status: "ok" }>(`${basePath}/${id}/actions/refresh`, {
    method: "POST",
  });
}

export async function cleanupSubscriptionCircuitOpenNodes(id: string): Promise<number> {
  const data = await apiRequest<{ cleaned_count: number }>(`${basePath}/${id}/actions/cleanup-circuit-open-nodes`, {
    method: "POST",
  });
  return data.cleaned_count;
}
