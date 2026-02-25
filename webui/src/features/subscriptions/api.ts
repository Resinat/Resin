import { apiRequest } from "../../lib/api-client";
import type {
  PageResponse,
  Subscription,
  SubscriptionCreateInput,
  SubscriptionUpdateInput,
} from "./types";

const basePath = "/api/v1/subscriptions";

type ApiSubscription = Omit<Subscription, "last_checked" | "last_updated" | "last_error"> & {
  source_type?: "remote" | "local";
  content?: string;
  last_checked?: string | null;
  last_updated?: string | null;
  last_error?: string | null;
};

function normalizeSubscription(raw: ApiSubscription): Subscription {
  return {
    ...raw,
    source_type: raw.source_type ?? "remote",
    content: raw.content ?? "",
    last_checked: raw.last_checked || "",
    last_updated: raw.last_updated || "",
    last_error: raw.last_error || "",
  };
}

function normalizeSubscriptionPage(raw: PageResponse<ApiSubscription>): PageResponse<Subscription> {
  return {
    ...raw,
    items: raw.items.map(normalizeSubscription),
  };
}

export type ListSubscriptionsInput = {
  enabled?: boolean;
  limit?: number;
  offset?: number;
  keyword?: string;
};

export async function listSubscriptions(input: ListSubscriptionsInput = {}): Promise<PageResponse<Subscription>> {
  const query = new URLSearchParams({
    limit: String(input.limit ?? 50),
    offset: String(input.offset ?? 0),
    sort_by: "created_at",
    sort_order: "desc",
  });

  if (input.enabled !== undefined) {
    query.set("enabled", String(input.enabled));
  }
  const keyword = input.keyword?.trim();
  if (keyword) {
    query.set("keyword", keyword);
  }

  const data = await apiRequest<PageResponse<ApiSubscription>>(`${basePath}?${query.toString()}`);
  return normalizeSubscriptionPage(data);
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
