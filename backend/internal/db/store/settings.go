package store

import "context"

// GetAllSettings returns every runtime setting as a key/value map.
func (s *Store) GetAllSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SetSetting upserts a single runtime setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO settings (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		key, value)
	return err
}

// SetSettings upserts several settings in one transaction: either every value
// is stored or, if any write fails, none is. Writing them one by one left a
// save half-applied when a key in the middle was rejected.
func (s *Store) SetSettings(ctx context.Context, kv map[string]string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for k, v := range kv {
		if _, err := tx.Exec(ctx,
			`INSERT INTO settings (key, value) VALUES ($1, $2)
			 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
			k, v); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// SetSettingIfAbsent inserts a setting only if the key does not yet exist.
// Used to seed defaults on first run without clobbering user edits.
func (s *Store) SetSettingIfAbsent(ctx context.Context, key, value string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING`,
		key, value)
	return err
}
