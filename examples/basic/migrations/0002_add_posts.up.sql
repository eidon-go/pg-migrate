CREATE TABLE example_posts (
    id        BIGSERIAL PRIMARY KEY,
    author_id BIGINT NOT NULL REFERENCES example_users (id) ON DELETE CASCADE,
    title     TEXT   NOT NULL,
    body      TEXT   NOT NULL DEFAULT ''
);

CREATE INDEX idx_example_posts_author ON example_posts (author_id);
