-- +migrate notransaction
--
-- CREATE INDEX CONCURRENTLY cannot run inside a transaction. The directive must
-- be the very first line of the file.
--
-- Every statement below runs on one dedicated connection, so the statement_timeout
-- set here applies to the CREATE INDEX that follows. That connection's session is
-- destroyed afterwards rather than returned to the caller's pool.
SET statement_timeout = 0;

-- IF NOT EXISTS matters: a CONCURRENTLY build that fails leaves an invalid index
-- behind, and the retry has to be able to get past it.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_example_sessions_expires_at
    ON example_sessions (expires_at);
