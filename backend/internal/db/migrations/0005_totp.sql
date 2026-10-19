-- +goose Up
-- +goose StatementBegin
ALTER TABLE users ADD COLUMN totp_secret TEXT;            -- AES-GCM encrypted base32 secret
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE users ADD COLUMN totp_enabled BOOLEAN NOT NULL DEFAULT false;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE users ADD COLUMN totp_recovery TEXT NOT NULL DEFAULT ''; -- newline-separated bcrypt hashes
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE users DROP COLUMN IF EXISTS totp_recovery;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE users DROP COLUMN IF EXISTS totp_enabled;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE users DROP COLUMN IF EXISTS totp_secret;
-- +goose StatementEnd
