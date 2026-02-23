export type Subscription = {
  id: string;
  name: string;
  url: string;
  update_interval: string;
  node_count: number;
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
  url: string;
  update_interval?: string;
  enabled?: boolean;
  ephemeral?: boolean;
  ephemeral_node_evict_delay?: string;
};

export type SubscriptionUpdateInput = {
  name?: string;
  url?: string;
  update_interval?: string;
  enabled?: boolean;
  ephemeral?: boolean;
  ephemeral_node_evict_delay?: string;
};
