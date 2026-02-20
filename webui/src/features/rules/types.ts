export type Rule = {
  url_prefix: string;
  headers: string[];
  updated_at: string;
};

export type PageResponse<T> = {
  items: T[];
  total: number;
  limit: number;
  offset: number;
};

export type ResolveResult = {
  matched_url_prefix?: string;
  headers?: string[];
};
