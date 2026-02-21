export type RequestLogItem = {
  id: string;
  ts: string;
  proxy_type: number;
  client_ip: string;
  platform_id: string;
  platform_name: string;
  account: string;
  target_host: string;
  target_url: string;
  node_hash: string;
  node_tag: string;
  egress_ip: string;
  duration_ms: number;
  net_ok: boolean;
  http_method: string;
  http_status: number;
  payload_present: boolean;
  req_headers_len: number;
  req_body_len: number;
  resp_headers_len: number;
  resp_body_len: number;
  req_headers_truncated: boolean;
  req_body_truncated: boolean;
  resp_headers_truncated: boolean;
  resp_body_truncated: boolean;
};

export type RequestLogListResponse = {
  items: RequestLogItem[];
  limit: number;
  has_more: boolean;
  next_cursor?: string;
};

export type RequestLogListFilters = {
  from?: string;
  to?: string;
  platform_name?: string;
  account?: string;
  target_host?: string;
  egress_ip?: string;
  proxy_type?: number;
  net_ok?: boolean;
  http_status?: number;
  limit: number;
  cursor?: string;
};

export type RequestLogPayloads = {
  req_headers_b64: string;
  req_body_b64: string;
  resp_headers_b64: string;
  resp_body_b64: string;
  truncated: {
    req_headers: boolean;
    req_body: boolean;
    resp_headers: boolean;
    resp_body: boolean;
  };
};
