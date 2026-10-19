-- +goose Up
-- +goose StatementBegin
ALTER TABLE files ADD COLUMN content_hash TEXT;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE files ADD COLUMN dominant_color TEXT;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX files_user_hash_idx ON files(user_id, content_hash);
-- +goose StatementEnd

-- +goose StatementBegin
-- Aggregated, privacy-first view analytics: counts per (slug, referrer host, UA
-- category). No IPs, no raw user-agents, no timestamps per view.
CREATE TABLE file_view_stats (
    slug          TEXT NOT NULL,
    referrer_host TEXT NOT NULL DEFAULT '(direct)',
    ua_category   TEXT NOT NULL DEFAULT 'other',
    count         BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (slug, referrer_host, ua_category)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS file_view_stats;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS files_user_hash_idx;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE files DROP COLUMN IF EXISTS dominant_color;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE files DROP COLUMN IF EXISTS content_hash;
-- +goose StatementEnd
