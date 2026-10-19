-- +goose Up
-- +goose StatementBegin
-- Storage keys are built from the slug, so on a case-insensitive filesystem
-- "ABCD" and "abcd" would share one blob: make slugs unique regardless of case.
-- Trashed rows still hold their slug (and blob), so the index covers them too.
-- Slugs that already differ only in case cannot be merged automatically; rather
-- than failing every boot, the index is skipped with a warning.
DO $$
DECLARE
    dupes bigint;
BEGIN
    SELECT count(*) INTO dupes FROM (SELECT 1 FROM files GROUP BY lower(slug) HAVING count(*) > 1) d;
    IF dupes > 0 THEN
        RAISE WARNING '% slug(s) exist in spellings that differ only in case; not creating the case-insensitive unique index files_slug_lower_uniq. List them with: SELECT lower(slug) FROM files GROUP BY 1 HAVING count(*) > 1', dupes;
    ELSE
        CREATE UNIQUE INDEX files_slug_lower_uniq ON files (lower(slug));
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS files_slug_lower_uniq;
-- +goose StatementEnd
