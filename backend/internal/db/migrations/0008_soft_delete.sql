-- +goose Up
-- +goose StatementBegin
ALTER TABLE files ADD COLUMN deleted_at TIMESTAMPTZ;
-- +goose StatementEnd
-- +goose StatementBegin
-- Trash (soft-delete): rows with deleted_at set are hidden from listings and
-- public serving but keep their blobs until the janitor purges them.
CREATE INDEX files_deleted_at_idx ON files(deleted_at) WHERE deleted_at IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS files_deleted_at_idx;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE files DROP COLUMN IF EXISTS deleted_at;
-- +goose StatementEnd
