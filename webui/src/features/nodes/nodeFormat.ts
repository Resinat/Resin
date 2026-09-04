import type { NodeSummary } from "./types";
import { getRegionName } from "./regions";

export type NodeDisplayStatus =
  | "healthy"
  | "circuit_open"
  | "pending_test"
  | "error"
  | "disabled"
  | "manually_disabled";

export function firstTag(node: { display_tag?: string; tags: { tag: string }[] }): string {
  if (node.display_tag && node.display_tag.trim()) {
    return node.display_tag;
  }
  if (!node.tags.length) {
    return "-";
  }
  return node.tags[0].tag;
}

export function hasReferenceLatency(
  node: NodeSummary,
): node is NodeSummary & { reference_latency_ms: number } {
  return typeof node.reference_latency_ms === "number";
}

export function isPendingTestNode(node: NodeSummary): boolean {
  return Boolean(node.circuit_open_since) && node.failure_count === 0;
}

export function getNodeDisplayStatus(node: NodeSummary): NodeDisplayStatus {
  if (node.manually_disabled) {
    return "manually_disabled";
  }
  if (!node.enabled) {
    return "disabled";
  }
  if (!node.has_outbound) {
    return "error";
  }
  if (isPendingTestNode(node)) {
    return "pending_test";
  }
  if (node.circuit_open_since) {
    return "circuit_open";
  }
  return "healthy";
}

export function referenceLatencyColor(latencyMs: number): string {
  if (!Number.isFinite(latencyMs)) {
    return "var(--text-secondary)";
  }
  if (latencyMs <= 400) {
    return "var(--success)";
  }
  if (latencyMs <= 1000) {
    return "var(--warning)";
  }
  return "var(--danger)";
}

export function displayableReferenceLatencyMs(node: NodeSummary): number | null {
  if (getNodeDisplayStatus(node) !== "healthy") {
    return null;
  }
  if (!hasReferenceLatency(node)) {
    return null;
  }
  return node.reference_latency_ms;
}

export function formatLatency(value: number): string {
  if (!Number.isFinite(value)) {
    return "-";
  }
  return `${value.toFixed(0)} ms`;
}

export function regionToFlag(region: string | undefined): string {
  if (!region || region.length !== 2) {
    return region || "-";
  }
  const code = region.toUpperCase();
  const flag = String.fromCodePoint(...[...code].map((c) => c.charCodeAt(0) + 127397));
  const name = getRegionName(code);
  return name ? `${flag} ${code} (${name})` : `${flag} ${code}`;
}
