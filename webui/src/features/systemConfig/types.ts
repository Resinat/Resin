export type RuntimeConfig = {
  user_agent: string;
  request_log_enabled: boolean;
  reverse_proxy_log_detail_enabled: boolean;
  reverse_proxy_log_req_headers_max_bytes: number;
  reverse_proxy_log_req_body_max_bytes: number;
  reverse_proxy_log_resp_headers_max_bytes: number;
  reverse_proxy_log_resp_body_max_bytes: number;
  max_consecutive_failures: number;
  max_latency_test_interval: string;
  max_authority_latency_test_interval: string;
  max_egress_test_interval: string;
  latency_test_url: string;
  latency_authorities: string[];
  p2c_latency_window: string;
  latency_decay_window: string;
  cache_flush_interval: string;
  cache_flush_dirty_threshold: number;
  ephemeral_node_evict_delay: string;
};

export type RuntimeConfigPatch = Partial<RuntimeConfig>;
