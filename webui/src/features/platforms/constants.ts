import type {
  PlatformAllocationPolicy,
  PlatformEmptyAccountBehavior,
  PlatformMissAction,
} from "./types";

export const allocationPolicies: PlatformAllocationPolicy[] = [
  "BALANCED",
  "PREFER_LOW_LATENCY",
  "PREFER_IDLE_IP",
];

export const missActions: PlatformMissAction[] = ["TREAT_AS_EMPTY", "REJECT"];

export const emptyAccountBehaviors: PlatformEmptyAccountBehavior[] = [
  "RANDOM",
  "FIXED_HEADER",
  "ACCOUNT_HEADER_RULE",
];

export const allocationPolicyLabel: Record<PlatformAllocationPolicy, string> = {
  BALANCED: "均衡",
  PREFER_LOW_LATENCY: "优先低延迟",
  PREFER_IDLE_IP: "优先空闲出口 IP",
};

export const missActionLabel: Record<PlatformMissAction, string> = {
  TREAT_AS_EMPTY: "按空账号处理",
  REJECT: "拒绝代理请求",
};

export const emptyAccountBehaviorLabel: Record<PlatformEmptyAccountBehavior, string> = {
  RANDOM: "随机路由",
  FIXED_HEADER: "提取指定请求头作为 Account",
  ACCOUNT_HEADER_RULE: "按照全局请求头规则提取 Account",
};
