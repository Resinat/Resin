ALTER TABLE subscriptions ADD COLUMN chain_proxy INTEGER NOT NULL DEFAULT 0 CHECK (chain_proxy IN (0, 1));
ALTER TABLE subscriptions ADD COLUMN chain_relay INTEGER NOT NULL DEFAULT 0 CHECK (chain_relay IN (0, 1) AND NOT (chain_proxy = 1 AND chain_relay = 1));
