CREATE TABLE IF NOT EXISTS endpoints (
    id                      TEXT PRIMARY KEY,
    port                    INTEGER NOT NULL UNIQUE CHECK (port BETWEEN 1 AND 65535),
    allow_management        INTEGER NOT NULL,
    allow_proxy             INTEGER NOT NULL,
    require_proxy_auth_info INTEGER NOT NULL DEFAULT 0,
    allow_http_forward      INTEGER NOT NULL,
    allow_http_reverse      INTEGER NOT NULL,
    allow_socks5            INTEGER NOT NULL,
    created_at_ns           INTEGER NOT NULL,
    updated_at_ns           INTEGER NOT NULL
);
