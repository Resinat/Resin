ALTER TABLE platforms
ADD COLUMN reverse_proxy_empty_account_behavior TEXT NOT NULL DEFAULT 'RANDOM';
