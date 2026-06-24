export type SubscriptionUsage = {
  upload_bytes: number;
  download_bytes: number;
  total_bytes: number;
  expire_unix?: number;
  updated_at: string;
};

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
  incremental_alive_nodes: boolean;
  ephemeral_node_evict_delay: string;
  enabled: boolean;
  created_at: string;
  last_checked?: string;
  last_updated?: string;
  last_error?: string;
  usage?: SubscriptionUsage;
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
  incremental_alive_nodes?: boolean;
  ephemeral_node_evict_delay?: string;
};

export type SubscriptionUpdateInput = {
  name?: string;
  url?: string;
  content?: string;
  update_interval?: string;
  enabled?: boolean;
  ephemeral?: boolean;
  incremental_alive_nodes?: boolean;
  ephemeral_node_evict_delay?: string;
};
