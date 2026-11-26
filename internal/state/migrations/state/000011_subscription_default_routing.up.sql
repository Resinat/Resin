ALTER TABLE subscriptions ADD COLUMN default_routing_disabled INTEGER NOT NULL DEFAULT 0 CHECK (default_routing_disabled IN (0, 1));
