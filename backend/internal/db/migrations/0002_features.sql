-- +goose Up
-- +goose StatementBegin
ALTER TABLE files ADD COLUMN delete_key TEXT;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE UNIQUE INDEX files_delete_key_idx ON files(delete_key) WHERE delete_key IS NOT NULL;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE abuse_reports (
    id             UUID PRIMARY KEY,
    slug           TEXT NOT NULL,
    file_id        UUID REFERENCES files(id) ON DELETE SET NULL,
    reporter_email TEXT NOT NULL DEFAULT '',
    reason         TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'open', -- open | resolved | dismissed
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX abuse_reports_status_idx ON abuse_reports(status, created_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS abuse_reports;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS files_delete_key_idx;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE files DROP COLUMN IF EXISTS delete_key;
-- +goose StatementEnd
