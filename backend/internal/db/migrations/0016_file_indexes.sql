-- +goose Up
-- +goose StatementBegin
-- Global retention (DeleteFilesOlderThan) deletes by created_at across all
-- users; the only index on it so far leads with user_id, so every janitor pass
-- scanned the whole table.
CREATE INDEX files_created_at_idx ON files (created_at);
-- +goose StatementEnd
-- +goose StatementBegin
-- DeleteExpiredFiles filters on the per-file expiry and view limit. Almost no
-- row sets them, so partial indexes stay tiny and turn the janitor's query
-- into an index probe instead of a sequential scan.
CREATE INDEX files_expires_at_idx ON files (expires_at) WHERE expires_at IS NOT NULL;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX files_max_views_idx ON files (id) WHERE max_views IS NOT NULL;
-- +goose StatementEnd
-- +goose StatementBegin
-- The orphan scan checks batches of on-disk keys against the rows that
-- reference them (store.ReferencedKeys).
CREATE INDEX files_storage_key_idx ON files (storage_key);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX files_thumb_key_idx ON files (thumb_key) WHERE thumb_key IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS files_thumb_key_idx;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS files_storage_key_idx;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS files_max_views_idx;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS files_expires_at_idx;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS files_created_at_idx;
-- +goose StatementEnd
