-- +goose Up
-- +goose StatementBegin
-- The public abuse/DMCA report feature was removed; its table goes with it.
DROP TABLE IF EXISTS abuse_reports;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Rebuild the table in its 0013 shape (reports themselves are not restored).
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
-- +goose StatementBegin
CREATE UNIQUE INDEX abuse_reports_open_reporter_uniq
    ON abuse_reports (slug, lower(reporter_email)) WHERE status = 'open';
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX abuse_reports_slug_idx ON abuse_reports (slug);
-- +goose StatementEnd
