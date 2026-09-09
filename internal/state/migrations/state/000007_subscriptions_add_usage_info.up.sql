ALTER TABLE subscriptions ADD COLUMN usage_upload_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE subscriptions ADD COLUMN usage_download_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE subscriptions ADD COLUMN usage_total_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE subscriptions ADD COLUMN usage_expire_unix INTEGER NOT NULL DEFAULT 0;
ALTER TABLE subscriptions ADD COLUMN usage_updated_at_ns INTEGER NOT NULL DEFAULT 0;
