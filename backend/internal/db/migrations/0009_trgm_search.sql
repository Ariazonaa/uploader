-- +goose Up
-- +goose StatementBegin
-- Trigram fuzzy search over file names. pg_trgm ships with the official
-- postgres image (contrib) and is a "trusted" extension, so the app DB role
-- (owner of this database) may create it. Runs inside goose's transaction.
CREATE EXTENSION IF NOT EXISTS pg_trgm;
-- +goose StatementEnd
-- +goose StatementBegin
-- GIN trigram index accelerates ILIKE '%q%' substring search and trigram
-- similarity on original_name (typo-tolerant matching).
CREATE INDEX files_original_name_trgm_idx ON files USING gin (original_name gin_trgm_ops);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS files_original_name_trgm_idx;
-- +goose StatementEnd
-- pg_trgm is intentionally NOT dropped on rollback (shared contrib extension).
