-- +migrate notransaction
DROP INDEX CONCURRENTLY IF EXISTS idx_example_sessions_expires_at;
