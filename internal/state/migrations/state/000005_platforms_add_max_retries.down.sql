-- SQLite does not support DROP COLUMN before 3.35.0; this is a best-effort rollback.
-- For older SQLite versions, a table recreation would be needed.
ALTER TABLE platforms DROP COLUMN max_retries;
