import { apiRequest } from "../../lib/api-client";
import type { PageResponse, ResolveResult, Rule } from "./types";

const basePath = "/api/v1/account-header-rules";

function normalizeRule(raw: Rule): Rule {
  return {
    ...raw,
    headers: Array.isArray(raw.headers) ? raw.headers : [],
  };
}

export async function listRules(keyword?: string): Promise<Rule[]> {
  const query = new URLSearchParams({
    limit: "1000",
    offset: "0",
  });
  const trimmedKeyword = keyword?.trim();
  if (trimmedKeyword) {
    query.set("keyword", trimmedKeyword);
  }
  const data = await apiRequest<PageResponse<Rule>>(`${basePath}?${query.toString()}`);
  return data.items.map(normalizeRule);
}

function encodeRulePrefix(prefix: string): string {
  return encodeURIComponent(prefix);
}

export async function upsertRule(prefix: string, headers: string[]): Promise<Rule> {
  const encodedPrefix = encodeRulePrefix(prefix);
  const data = await apiRequest<Rule>(`${basePath}/${encodedPrefix}`, {
    method: "PUT",
    body: { headers },
  });
  return normalizeRule(data);
}

export async function deleteRule(prefix: string): Promise<void> {
  const encodedPrefix = encodeRulePrefix(prefix);
  await apiRequest<void>(`${basePath}/${encodedPrefix}`, {
    method: "DELETE",
  });
}

export async function resolveRule(url: string): Promise<ResolveResult> {
  const data = await apiRequest<ResolveResult>("/api/v1/account-header-rules:resolve", {
    method: "POST",
    body: { url },
  });

  return {
    matched_url_prefix: data.matched_url_prefix || "",
    headers: Array.isArray(data.headers) ? data.headers : [],
  };
}
