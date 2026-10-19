package store

import "context"

// ExistingSlugs returns which of slugs still have a file row, trashed files
// included. The orphan scan uses it to tell an HLS rendition whose file is
// gone from one that is still in use. It reads the primary: a lagging replica
// would report a just-uploaded file as missing.
func (s *Store) ExistingSlugs(ctx context.Context, slugs []string) (map[string]struct{}, error) {
	rows, err := s.pool.Query(ctx, `SELECT slug FROM files WHERE slug = ANY($1)`, slugs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := make(map[string]struct{}, len(slugs))
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, err
		}
		set[slug] = struct{}{}
	}
	return set, rows.Err()
}
