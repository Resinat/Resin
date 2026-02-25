export type Subscription = {
  id: string;
  name: string;
  source_type: "remote" | "local";
  url: string;
  content: string;
  update_interval: string;
  node_count: number;
  healthy_node_count: number;
  ephemeral: boolean;
  ephemeral_node_evict_delay: string;
  enabled: boolean;
  created_at: string;
  last_checked?: string;
  last_updated?: string;
  last_error?: string;
};

export type PageResponse<T> = {
  items: T[];
  total: number;
  limit: number;
  offset: number;
};

export type SubscriptionCreateInput = {
  name: string;
  source_type?: "remote" | "local";
  url?: string;
  content?: string;
  update_interval?: string;
  enabled?: boolean;
  ephemeral?: boolean;
  ephemeral_node_evict_delay?: string;
};

export type SubscriptionUpdateInput = {
  name?: string;
  url?: string;
  content?: string;
  update_interval?: string;
  enabled?: boolean;
  ephemeral?: boolean;
  ephemeral_node_evict_delay?: string;
};
