-- +goose Up
-- +goose StatementBegin
ALTER TABLE files ADD COLUMN encrypted BOOLEAN NOT NULL DEFAULT false;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
ALTER TABLE files DROP COLUMN IF EXISTS encrypted;
-- +goose StatementEnd
