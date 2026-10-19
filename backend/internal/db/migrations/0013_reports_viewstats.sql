-- +goose Up
-- +goose StatementBegin
-- Abuse reports: "one report per slug, forever" let an uploader pre-empt real
-- reports by filing a bogus one against their own file. Allow many reports per
-- slug; only collapse an exact duplicate (same slug + reporter e-mail, where
-- anonymous is one reporter) while the earlier report is still open.
DROP INDEX IF EXISTS abuse_reports_slug_uniq;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE UNIQUE INDEX abuse_reports_open_reporter_uniq
    ON abuse_reports (slug, lower(reporter_email)) WHERE status = 'open';
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX abuse_reports_slug_idx ON abuse_reports (slug);
-- +goose StatementEnd

-- +goose StatementBegin
-- View stats are keyed by slug, not by a file FK, so nothing removed them when
-- a file was purged. Clean them up on every row delete (purge, trash expiry,
-- retention, account deletion via ON DELETE CASCADE) with one trigger rather
-- than in each of the store's many DELETE paths.
CREATE FUNCTION file_view_stats_purge() RETURNS trigger AS $$
BEGIN
    DELETE FROM file_view_stats WHERE slug = OLD.slug;
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER files_purge_view_stats
    AFTER DELETE ON files
    FOR EACH ROW EXECUTE FUNCTION file_view_stats_purge();
-- +goose StatementEnd
-- +goose StatementBegin
-- Drop the rows already orphaned by earlier purges.
DELETE FROM file_view_stats v
WHERE NOT EXISTS (SELECT 1 FROM files f WHERE f.slug = v.slug);
-- +goose StatementEnd
-- +goose StatementBegin
-- New views store hosts lowercased with one trailing dot removed (see
-- normalizeReferrerHost). Rewrite existing rows the same way, merging counts,
-- or "Example.ORG" and "example.org" would stay two rows for one host.
INSERT INTO file_view_stats (slug, referrer_host, ua_category, count)
SELECT slug, regexp_replace(lower(referrer_host), '\.$', ''), ua_category, sum(count)
FROM file_view_stats
WHERE referrer_host NOT IN ('(direct)', '(other)')
  AND referrer_host <> regexp_replace(lower(referrer_host), '\.$', '')
GROUP BY slug, regexp_replace(lower(referrer_host), '\.$', ''), ua_category
ON CONFLICT (slug, referrer_host, ua_category)
DO UPDATE SET count = file_view_stats.count + EXCLUDED.count;
-- +goose StatementEnd
-- +goose StatementBegin
DELETE FROM file_view_stats
WHERE referrer_host NOT IN ('(direct)', '(other)')
  AND referrer_host <> regexp_replace(lower(referrer_host), '\.$', '');
-- +goose StatementEnd
-- +goose StatementBegin
-- Fold referrer hosts that the new normalisation would reject (over-long or not
-- hostname-shaped: client-supplied Referer junk) into the "(other)" bucket.
INSERT INTO file_view_stats (slug, referrer_host, ua_category, count)
SELECT slug, '(other)', ua_category, sum(count)
FROM file_view_stats
WHERE referrer_host NOT IN ('(direct)', '(other)')
  AND (length(referrer_host) > 253
       OR referrer_host !~* '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$')
GROUP BY slug, ua_category
ON CONFLICT (slug, referrer_host, ua_category)
DO UPDATE SET count = file_view_stats.count + EXCLUDED.count;
-- +goose StatementEnd
-- +goose StatementBegin
DELETE FROM file_view_stats
WHERE referrer_host NOT IN ('(direct)', '(other)')
  AND (length(referrer_host) > 253
       OR referrer_host !~* '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS files_purge_view_stats ON files;
-- +goose StatementEnd
-- +goose StatementBegin
DROP FUNCTION IF EXISTS file_view_stats_purge();
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS abuse_reports_slug_idx;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS abuse_reports_open_reporter_uniq;
-- +goose StatementEnd
-- +goose StatementBegin
-- The old one-report-per-slug index cannot be rebuilt while a slug has several
-- reports: keep the oldest report per slug (destructive, as any down is).
DELETE FROM abuse_reports a
USING abuse_reports b
WHERE a.slug = b.slug AND (a.created_at, a.id) > (b.created_at, b.id);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE UNIQUE INDEX abuse_reports_slug_uniq ON abuse_reports(slug);
-- +goose StatementEnd
