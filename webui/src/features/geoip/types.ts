export type GeoIPStatus = {
  db_mtime: string;
  next_scheduled_update: string;
};

export type GeoIPLookupResult = {
  ip: string;
  region: string;
};

export type GeoIPBatchLookupResponse = {
  results: GeoIPLookupResult[];
};

export type GeoIPUpdateResponse = {
  status: string;
};
