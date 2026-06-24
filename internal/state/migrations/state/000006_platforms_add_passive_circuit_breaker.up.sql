ALTER TABLE platforms
ADD COLUMN passive_circuit_breaker_disabled INTEGER NOT NULL DEFAULT 0;
