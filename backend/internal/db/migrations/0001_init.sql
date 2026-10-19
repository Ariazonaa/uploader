-- +goose Up
-- +goose StatementBegin
CREATE TABLE users (
    id            UUID PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    quota_bytes   BIGINT NOT NULL DEFAULT 0, -- 0 = unlimited; reserved for multi-user
    used_bytes    BIGINT NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE sessions (
    id         BYTEA PRIMARY KEY,            -- 32 random bytes
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    ip         TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX sessions_user_id_idx ON sessions(user_id);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX sessions_expires_at_idx ON sessions(expires_at);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE api_tokens (
    id           UUID PRIMARY KEY,
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    token_prefix TEXT NOT NULL,              -- indexed lookup hint, not a secret
    token_hash   TEXT NOT NULL,              -- bcrypt hash of full token
    last_used_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX api_tokens_user_id_idx ON api_tokens(user_id);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX api_tokens_prefix_idx ON api_tokens(token_prefix);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE files (
    id            UUID PRIMARY KEY,
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    slug          TEXT NOT NULL UNIQUE,
    original_name TEXT NOT NULL,
    mime_type     TEXT NOT NULL,
    size_bytes    BIGINT NOT NULL,
    storage_key   TEXT NOT NULL,
    thumb_key     TEXT,
    width         INTEGER,
    height        INTEGER,
    duration_ms   BIGINT,
    views         BIGINT NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Columns prepared for TODO features (expiry, view-limit, password, folders).
    -- Present now so enabling those later needs no destructive migration.
    expires_at    TIMESTAMPTZ,
    max_views     BIGINT,
    password_hash TEXT,
    folder_id     UUID
);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX files_user_id_created_idx ON files(user_id, created_at DESC);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS settings;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS files;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS api_tokens;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS sessions;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS users;
-- +goose StatementEnd
