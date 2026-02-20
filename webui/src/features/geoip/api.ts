import { apiRequest } from "../../lib/api-client";
import type { GeoIPBatchLookupResponse, GeoIPLookupResult, GeoIPStatus, GeoIPUpdateResponse } from "./types";

const basePath = "/api/v1/geoip";

function normalizeLookupResult(raw: GeoIPLookupResult): GeoIPLookupResult {
  return {
    ip: raw.ip || "",
    region: raw.region || "",
  };
}

export async function getGeoIPStatus(): Promise<GeoIPStatus> {
  const data = await apiRequest<GeoIPStatus>(`${basePath}/status`);
  return {
    db_mtime: data.db_mtime || "",
    next_scheduled_update: data.next_scheduled_update || "",
  };
}

export async function lookupIP(ip: string): Promise<GeoIPLookupResult> {
  const query = new URLSearchParams({ ip });
  const data = await apiRequest<GeoIPLookupResult>(`${basePath}/lookup?${query.toString()}`);
  return normalizeLookupResult(data);
}

export async function lookupIPBatch(ips: string[]): Promise<GeoIPLookupResult[]> {
  const data = await apiRequest<GeoIPBatchLookupResponse>(`${basePath}/lookup`, {
    method: "POST",
    body: { ips },
  });
  if (!Array.isArray(data.results)) {
    return [];
  }
  return data.results.map(normalizeLookupResult);
}

export async function updateGeoIPNow(): Promise<GeoIPUpdateResponse> {
  const data = await apiRequest<GeoIPUpdateResponse>(`${basePath}/actions/update-now`, {
    method: "POST",
  });
  return {
    status: data.status || "",
  };
}
