-- +goose Up
-- +goose StatementBegin
-- Highest TOTP time step already accepted for this user. A code is only valid
-- if its step is strictly greater, so an intercepted code cannot be replayed
-- within its (±1 step) validity window — including across app instances, since
-- the guard lives in the database rather than in per-process memory.
ALTER TABLE users ADD COLUMN totp_last_step BIGINT NOT NULL DEFAULT 0;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
ALTER TABLE users DROP COLUMN IF EXISTS totp_last_step;
-- +goose StatementEnd
