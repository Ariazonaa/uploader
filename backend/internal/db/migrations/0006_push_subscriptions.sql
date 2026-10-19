-- +goose Up
-- +goose StatementBegin
CREATE TABLE push_subscriptions (
    id         UUID PRIMARY KEY,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    endpoint   TEXT NOT NULL UNIQUE,
    p256dh     TEXT NOT NULL,
    auth       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX push_subscriptions_user_idx ON push_subscriptions(user_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS push_subscriptions;
-- +goose StatementEnd
