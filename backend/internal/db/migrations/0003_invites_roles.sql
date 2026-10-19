-- +goose Up
-- +goose StatementBegin
ALTER TABLE users ADD COLUMN is_admin BOOLEAN NOT NULL DEFAULT false;
-- +goose StatementEnd
-- +goose StatementBegin
-- Existing accounts were created via the CLI (operator) → promote them to admin
-- so the single-user setup keeps full access after this migration.
UPDATE users SET is_admin = true;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE invite_codes (
    code       TEXT PRIMARY KEY,
    created_by UUID REFERENCES users(id) ON DELETE SET NULL,
    used_by    UUID REFERENCES users(id) ON DELETE SET NULL,
    used_at    TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose StatementBegin
-- A given file (slug) can be reported at most once — anti-spam.
CREATE UNIQUE INDEX abuse_reports_slug_uniq ON abuse_reports(slug);
-- +goose StatementEnd

-- +goose StatementBegin
-- Login matches usernames/emails case-insensitively, so enforce uniqueness the
-- same way — otherwise registration could create "Admin" alongside "admin" and
-- cause ambiguous logins / impersonation.
CREATE UNIQUE INDEX users_username_lower_uniq ON users (lower(username));
-- +goose StatementEnd
-- +goose StatementBegin
CREATE UNIQUE INDEX users_email_lower_uniq ON users (lower(email));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS users_email_lower_uniq;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS users_username_lower_uniq;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS abuse_reports_slug_uniq;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS invite_codes;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE users DROP COLUMN IF EXISTS is_admin;
-- +goose StatementEnd
