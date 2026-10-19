package store

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5"
)

// Reserved referrer buckets. ReferrerDirect is a view without a Referer;
// ReferrerOther collects unparseable/invalid referrers and the overflow once a
// slug has MaxReferrersPerSlug distinct referrer hosts.
const (
	ReferrerDirect = "(direct)"
	ReferrerOther  = "(other)"
)

// MaxReferrersPerSlug caps the distinct referrer hosts tracked per file. The
// Referer header is client-controlled and the blob routes are public, so
// without a cap every request with a made-up host would add a permanent row.
// The reserved ReferrerDirect/ReferrerOther buckets are not hosts and do not
// count toward it.
const MaxReferrersPerSlug = 50

// RecordView increments the aggregated view counter for a (slug, referrer host,
// UA category) bucket. No IPs, timestamps or raw user-agents are stored.
//
// referrerHost must already be normalised by the caller (see api.refererHost);
// this method enforces the per-slug cap: a host that is not yet tracked for the
// slug is folded into ReferrerOther once MaxReferrersPerSlug distinct hosts
// exist. Concurrent first views of new hosts may overshoot the cap by the
// number of in-flight writers; the API batches views per instance, so that is
// at most one flush per instance at a time. Views
// of a slug that no longer exists (purged between the request and this async
// write) are dropped rather than recreating an orphan row.
func (s *Store) RecordView(ctx context.Context, slug, referrerHost, uaCategory string) error {
	return s.RecordViews(ctx, []ViewDelta{{Slug: slug, ReferrerHost: referrerHost, UACategory: uaCategory, Count: 1}})
}

// recordViewSQL adds $5 views to one (slug, referrer host, UA category)
// bucket, folding a new host into ReferrerOther once the slug has $4 hosts.
const recordViewSQL = `INSERT INTO file_view_stats (slug, referrer_host, ua_category, count)
 SELECT $1::text,
   CASE WHEN $2::text IN ('(direct)', '(other)')
          OR EXISTS (SELECT 1 FROM file_view_stats
                     WHERE slug = $1::text AND referrer_host = $2::text)
          OR (SELECT count(DISTINCT referrer_host) FROM file_view_stats
              WHERE slug = $1::text
                AND referrer_host NOT IN ('(direct)', '(other)')) < $4::int
        THEN $2::text ELSE '(other)' END,
   $3::text, $5::bigint
 WHERE EXISTS (SELECT 1 FROM files WHERE slug = $1::text)
 ON CONFLICT (slug, referrer_host, ua_category)
 DO UPDATE SET count = file_view_stats.count + EXCLUDED.count`

// ViewDelta is an aggregate of views of one file from one (referrer host, UA
// category) bucket. Count goes to the breakdown in file_view_stats; Views is
// how many of them also count toward the file's public view counter (the share
// page records a breakdown entry without counting a view).
type ViewDelta struct {
	Slug         string
	ReferrerHost string
	UACategory   string
	Count        int64
	Views        int64
}

// RecordViews applies a batch of aggregated views (see api's view batcher) in
// one round trip and one implicit transaction: each bucket through the same
// capped upsert as RecordView, then one counter update per slug. Buckets are
// applied in (slug, host, category) order and counters in slug order, so two
// instances flushing at once take their row locks in the same order instead of
// deadlocking. Like RecordView, views of a slug with no file row are dropped.
func (s *Store) RecordViews(ctx context.Context, deltas []ViewDelta) error {
	if len(deltas) == 0 {
		return nil
	}
	sorted := make([]ViewDelta, len(deltas))
	copy(sorted, deltas)
	for i := range sorted {
		if sorted[i].ReferrerHost == "" {
			sorted[i].ReferrerHost = ReferrerDirect
		}
		if sorted[i].UACategory == "" {
			sorted[i].UACategory = "other"
		}
	}
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Slug != b.Slug {
			return a.Slug < b.Slug
		}
		if a.ReferrerHost != b.ReferrerHost {
			return a.ReferrerHost < b.ReferrerHost
		}
		return a.UACategory < b.UACategory
	})

	batch := &pgx.Batch{}
	views := map[string]int64{}
	var slugs []string
	for _, d := range sorted {
		if d.Count > 0 {
			batch.Queue(recordViewSQL, d.Slug, d.ReferrerHost, d.UACategory, MaxReferrersPerSlug, d.Count)
		}
		if d.Views > 0 {
			if _, ok := views[d.Slug]; !ok {
				slugs = append(slugs, d.Slug) // already in slug order
			}
			views[d.Slug] += d.Views
		}
	}
	for _, slug := range slugs {
		batch.Queue(`UPDATE files SET views = views + $2 WHERE slug = $1`, slug, views[slug])
	}
	if batch.Len() == 0 {
		return nil
	}
	return s.pool.SendBatch(ctx, batch).Close()
}

// NameCount is a generic label/value pair for aggregated stats.
type NameCount struct {
	Name  string
	Count int64
}

// TopReferrers returns the most frequent referrer hosts across all files.
// Like every other statistic, it leaves out files in the trash; their rows
// are kept, so restoring a file brings its views back.
func (s *Store) TopReferrers(ctx context.Context, limit int) ([]NameCount, error) {
	return s.aggViewStats(ctx,
		`SELECT v.referrer_host, sum(v.count) FROM file_view_stats v
		 JOIN files f ON f.slug = v.slug AND f.deleted_at IS NULL
		 GROUP BY v.referrer_host ORDER BY 2 DESC LIMIT $1`, limit)
}

// TopUACategories returns view counts grouped by user-agent category, without
// files in the trash (see TopReferrers).
func (s *Store) TopUACategories(ctx context.Context, limit int) ([]NameCount, error) {
	return s.aggViewStats(ctx,
		`SELECT v.ua_category, sum(v.count) FROM file_view_stats v
		 JOIN files f ON f.slug = v.slug AND f.deleted_at IS NULL
		 GROUP BY v.ua_category ORDER BY 2 DESC LIMIT $1`, limit)
}

func (s *Store) aggViewStats(ctx context.Context, query string, limit int) ([]NameCount, error) {
	rows, err := s.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NameCount
	for rows.Next() {
		var nc NameCount
		if err := rows.Scan(&nc.Name, &nc.Count); err != nil {
			return nil, err
		}
		out = append(out, nc)
	}
	return out, rows.Err()
}

// DeleteOrphanViewStats removes stats rows whose slug has no file row (trashed
// files keep theirs) and returns how many it removed. The 0013 trigger deletes
// a slug's stats when its file is purged, but RecordView races it under READ
// COMMITTED: a view whose statement snapshot still saw the file inserts after
// the purging transaction's trigger ran, and nothing removed that row. Such a
// row would also be inherited by a later file reusing the slug. The janitor
// calls this periodically.
func (s *Store) DeleteOrphanViewStats(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM file_view_stats v
		 WHERE NOT EXISTS (SELECT 1 FROM files f WHERE f.slug = v.slug)`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
