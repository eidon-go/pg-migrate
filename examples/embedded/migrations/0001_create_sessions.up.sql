CREATE TABLE example_sessions (
    token      TEXT PRIMARY KEY,
    user_id    BIGINT      NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
