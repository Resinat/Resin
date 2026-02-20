import { apiRequest } from "../../lib/api-client";
import type {
  PageResponse,
  Subscription,
  SubscriptionCreateInput,
  SubscriptionUpdateInput,
} from "./types";

const basePath = "/api/v1/subscriptions";

type ApiSubscription = Omit<Subscription, "last_checked" | "last_updated" | "last_error"> & {
  last_checked?: string | null;
  last_updated?: string | null;
  last_error?: string | null;
};

function normalizeSubscription(raw: ApiSubscription): Subscription {
  return {
    ...raw,
    last_checked: raw.last_checked || "",
    last_updated: raw.last_updated || "",
    last_error: raw.last_error || "",
  };
}

export async function listSubscriptions(enabled?: boolean): Promise<Subscription[]> {
  const query = new URLSearchParams({
    limit: "1000",
    offset: "0",
    sort_by: "created_at",
    sort_order: "desc",
  });

  if (enabled !== undefined) {
    query.set("enabled", String(enabled));
  }

  const data = await apiRequest<PageResponse<ApiSubscription>>(`${basePath}?${query.toString()}`);
  return data.items.map(normalizeSubscription);
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
