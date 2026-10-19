-- +goose Up
-- +goose StatementBegin
ALTER TABLE files ADD COLUMN has_hls BOOLEAN NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE files DROP COLUMN IF EXISTS has_hls;
-- +goose StatementEnd
