ALTER TABLE subscriptions DROP COLUMN usage_updated_at_ns;
ALTER TABLE subscriptions DROP COLUMN usage_expire_unix;
ALTER TABLE subscriptions DROP COLUMN usage_total_bytes;
ALTER TABLE subscriptions DROP COLUMN usage_download_bytes;
ALTER TABLE subscriptions DROP COLUMN usage_upload_bytes;
