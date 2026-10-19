-- +goose Up
-- +goose StatementBegin
CREATE TABLE audit_log (
    id          UUID PRIMARY KEY,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    user_id     UUID REFERENCES users(id) ON DELETE SET NULL,
    actor_label TEXT NOT NULL DEFAULT '',
    event       TEXT NOT NULL,
    target      TEXT NOT NULL DEFAULT '',
    ip          TEXT NOT NULL DEFAULT '',
    user_agent  TEXT NOT NULL DEFAULT '',
    metadata    JSONB NOT NULL DEFAULT '{}'::jsonb
);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX audit_log_created_at_idx ON audit_log(created_at DESC);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX audit_log_event_idx ON audit_log(event);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX audit_log_user_id_idx ON audit_log(user_id) WHERE user_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS audit_log;
-- +goose StatementEnd
