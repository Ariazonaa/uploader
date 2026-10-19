package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CreateFile inserts a file record.
func (s *Store) CreateFile(ctx context.Context, f File) error {
	if f.ID == uuid.Nil {
		f.ID = uuid.New()
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO files (id, user_id, slug, original_name, mime_type, size_bytes, storage_key, delete_key, content_hash, encrypted)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		f.ID, f.UserID, f.Slug, f.OriginalName, f.MimeType, f.SizeBytes, f.StorageKey, f.DeleteKey, f.ContentHash, f.Encrypted)
	return err
}

// GetFileByHash returns the user's newest non-trashed file with the given
// content hash, if any (for upload deduplication). The newest copy has the most
// time left under global retention; files with their own expiry or view limit
// are never reused. Returns ErrNotFound when there is none. Runs on the primary
// pool so a just-written upload is visible.
func (s *Store) GetFileByHash(ctx context.Context, userID uuid.UUID, hash string) (File, error) {
	return s.scanFile(s.pool.QueryRow(ctx,
		fileSelect+` WHERE user_id = $1 AND content_hash = $2 AND deleted_at IS NULL
		 AND expires_at IS NULL AND max_views IS NULL
		 ORDER BY created_at DESC, id DESC LIMIT 1`, userID, hash))
}

// ReferencedKeys returns which of keys a file row (trashed or not) references
// as its storage_key or thumb_key. The orphan-blob janitor checks the keys it
// finds on disk in batches through this, instead of loading every key in the
// table. It reads the primary: a lagging replica would report a just-uploaded
// file's blob as unreferenced.
func (s *Store) ReferencedKeys(ctx context.Context, keys []string) (map[string]struct{}, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT storage_key FROM files WHERE storage_key = ANY($1)
		 UNION
		 SELECT thumb_key FROM files WHERE thumb_key = ANY($1)`, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := make(map[string]struct{}, len(keys))
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		set[k] = struct{}{}
	}
	return set, rows.Err()
}

// SetDominantColor stores the computed dominant color (hex) for a file.
func (s *Store) SetDominantColor(ctx context.Context, id uuid.UUID, color string) error {
	_, err := s.pool.Exec(ctx, `UPDATE files SET dominant_color = $2 WHERE id = $1`, id, color)
	return err
}

// SetHasHLS marks that an HLS rendition is available for a file.
func (s *Store) SetHasHLS(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE files SET has_hls = true WHERE id = $1`, id)
	return err
}

// UpdateFileMedia attaches thumbnail/dimension metadata after processing.
func (s *Store) UpdateFileMedia(ctx context.Context, id uuid.UUID, thumbKey *string, width, height *int, durationMs *int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE files SET thumb_key = $2, width = $3, height = $4, duration_ms = $5 WHERE id = $1`,
		id, thumbKey, width, height, durationMs)
	return err
}

// GetFileBySlug loads a non-trashed file by its public slug. It reads the
// replica, which carries every public route, and asks the primary on a miss:
// under replication lag a link shared right after its upload would otherwise
// 404, and unfurlers such as Discord cache that answer.
func (s *Store) GetFileBySlug(ctx context.Context, slug string) (File, error) {
	f, err := s.scanFile(s.reader().QueryRow(ctx, fileSelect+` WHERE slug = $1 AND deleted_at IS NULL`, slug))
	if errors.Is(err, ErrNotFound) && s.read != s.pool {
		return s.GetFileBySlugPrimary(ctx, slug)
	}
	return f, err
}

// GetFileBySlugPrimary is GetFileBySlug read from the primary. Use it to read
// back a row this request (or job) just wrote: under replication lag the
// replica may not have it yet, and a miss there would turn a committed upload
// into a 500.
func (s *Store) GetFileBySlugPrimary(ctx context.Context, slug string) (File, error) {
	return s.scanFile(s.pool.QueryRow(ctx, fileSelect+` WHERE slug = $1 AND deleted_at IS NULL`, slug))
}

// FileExists reports whether the file row still exists at all — trashed or not
// — reading from the primary. Background jobs use it to notice that the file
// they are working on was purged while they ran.
func (s *Store) FileExists(ctx context.Context, id uuid.UUID) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM files WHERE id = $1)`, id).Scan(&exists)
	return exists, err
}

// SlugStorageKey returns the storage key of the row that holds slug, trashed
// or not, reading from the primary; taken is false when no row has it. A purge
// asks this after deleting a row: the slug is free again from that moment,
// and all of a file's blob keys derive from it.
func (s *Store) SlugStorageKey(ctx context.Context, slug string) (key string, taken bool, err error) {
	err = s.pool.QueryRow(ctx, `SELECT storage_key FROM files WHERE slug = $1`, slug).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return key, err == nil, err
}

// GetFileByID loads a non-trashed file owned by the given user.
func (s *Store) GetFileByID(ctx context.Context, id, userID uuid.UUID) (File, error) {
	return s.scanFile(s.pool.QueryRow(ctx, fileSelect+` WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`, id, userID))
}

// GetFileByDeleteKey loads a non-trashed file by its (secret) delete key — used
// by the public ShareX deletion URL.
func (s *Store) GetFileByDeleteKey(ctx context.Context, key string) (File, error) {
	return s.scanFile(s.pool.QueryRow(ctx, fileSelect+` WHERE delete_key = $1 AND deleted_at IS NULL`, key))
}

// FileQuery holds the optional filter/sort parameters for ListFiles.
type FileQuery struct {
	Search     string // substring or fuzzy (trigram) match of original_name
	MimePrefix string // e.g. "image/", "video/"
	Sort       string // created | size | views | name (default created)
	Desc       bool   // true = descending
	Limit      int
	Offset     int
}

// sortColumns whitelists user-supplied sort keys → real columns (prevents SQL
// injection via the ORDER BY clause, which cannot be parameterized).
var sortColumns = map[string]string{
	"created": "created_at",
	"size":    "size_bytes",
	"views":   "views",
	"name":    "original_name",
}

// likeEscaper escapes the LIKE/ILIKE metacharacters (with the default escape
// character, backslash) so user input matches literally: a search for "%" or
// "_" used to match every file, and "100%" matched any name starting "100".
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func escapeLike(s string) string { return likeEscaper.Replace(s) }

// listFilesStatementTimeout bounds one ListFiles query. The search matches
// every one of the user's file names by trigram similarity, and the list sits
// behind the dashboard, so a slow one must not hold a pooled connection.
const listFilesStatementTimeout = `SET LOCAL statement_timeout = '10s'`

// searchSimilarityThreshold is the cut-off for the fuzzy half of the file
// search: the % operator matches names whose trigram similarity to the term is
// above pg_trgm.similarity_threshold. 0.2 is what the search has always used
// (the extension's default is 0.3). SET LOCAL keeps it to the transaction.
const searchSimilarityThreshold = `SET LOCAL pg_trgm.similarity_threshold = 0.2`

// ListFiles returns a filtered, sorted page of a user's files. Like the other
// owner views (ListTrash, UserStats) it reads the primary: the dashboard
// refetches right after an upload, delete or restore, and a lagging replica
// showed the old state (a deleted file back in the list, a new one missing).
func (s *Store) ListFiles(ctx context.Context, userID uuid.UUID, q FileQuery) ([]File, error) {
	col := sortColumns[q.Sort]
	if col == "" {
		col = "created_at"
	}
	dir := "ASC"
	if q.Desc {
		dir = "DESC"
	}

	args := []any{userID}
	where := "WHERE user_id = $1 AND deleted_at IS NULL"
	// Every ORDER BY ends in the primary key: the sort columns are not unique
	// (equal sizes, view counts, names, and batch uploads sharing a timestamp),
	// and LIMIT/OFFSET over a non-total order lets rows swap places between
	// pages — "Load more" would repeat some files and never show others.
	orderBy := fmt.Sprintf("%s %s, id %s", col, dir, dir)
	if q.Search != "" {
		// Substring match keeps the original ILIKE behavior; trigram
		// similarity adds typo-tolerant matching and drives the ranking. The
		// similarity filter is written as the % operator (with its threshold
		// set below) rather than similarity(...) > x: only the operator form
		// can use the GIN trigram index, the function call is evaluated for
		// every one of the user's rows.
		args = append(args, q.Search)
		simIdx := len(args)
		args = append(args, "%"+escapeLike(q.Search)+"%")
		likeIdx := len(args)
		where += fmt.Sprintf(" AND (original_name ILIKE $%d OR original_name %% $%d)", likeIdx, simIdx)
		orderBy = fmt.Sprintf("similarity(original_name, $%d) DESC, %s %s, id %s", simIdx, col, dir, dir)
	}
	if q.MimePrefix != "" {
		args = append(args, escapeLike(q.MimePrefix)+"%")
		where += fmt.Sprintf(" AND mime_type LIKE $%d", len(args))
	}
	args = append(args, q.Limit)
	limitIdx := len(args)
	args = append(args, q.Offset)
	offsetIdx := len(args)

	query := fmt.Sprintf("%s %s ORDER BY %s LIMIT $%d OFFSET $%d",
		fileSelect, where, orderBy, limitIdx, offsetIdx)
	// The time bound has to be Postgres' own. A context deadline only makes
	// pgx drop the connection, and a backend busy computing similarity() does
	// not notice that its client is gone, so it would keep running.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, listFilesStatementTimeout); err != nil {
		return nil, err
	}
	if q.Search != "" {
		if _, err := tx.Exec(ctx, searchSimilarityThreshold); err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []File
	for rows.Next() {
		f, err := scanFileRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// DeleteFile soft-deletes a file owned by the user (moves it to trash) and
// returns the row. Blobs are kept until the trash is purged, so the caller must
// NOT delete blobs here. Returns ErrNotFound if absent or already trashed.
func (s *Store) DeleteFile(ctx context.Context, id, userID uuid.UUID) (File, error) {
	row := s.pool.QueryRow(ctx,
		`UPDATE files SET deleted_at = now() WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL RETURNING `+fileColumns,
		id, userID)
	return s.scanFile(row)
}

// DeleteFiles soft-deletes every file among ids that the user owns and that is
// not already in the trash, in one statement, and returns how many it moved.
// Ids that are missing, foreign or already trashed are skipped silently.
func (s *Store) DeleteFiles(ctx context.Context, ids []uuid.UUID, userID uuid.UUID) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE files SET deleted_at = now() WHERE id = ANY($1) AND user_id = $2 AND deleted_at IS NULL`,
		ids, userID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// RestoreFile clears the trash flag on a soft-deleted file owned by the user.
// Returns ErrNotFound if no such trashed file exists.
func (s *Store) RestoreFile(ctx context.Context, id, userID uuid.UUID) (File, error) {
	row := s.pool.QueryRow(ctx,
		`UPDATE files SET deleted_at = NULL WHERE id = $1 AND user_id = $2 AND deleted_at IS NOT NULL RETURNING `+fileColumns,
		id, userID)
	return s.scanFile(row)
}

// HardDeleteFile permanently removes a trashed file owned by the user and
// returns the row so the caller can purge its blobs. Returns ErrNotFound if the
// file is not in the trash.
func (s *Store) HardDeleteFile(ctx context.Context, id, userID uuid.UUID) (File, error) {
	row := s.pool.QueryRow(ctx,
		`DELETE FROM files WHERE id = $1 AND user_id = $2 AND deleted_at IS NOT NULL RETURNING `+fileColumns,
		id, userID)
	return s.scanFile(row)
}

// ListTrash returns a page of a user's soft-deleted files, newest-trashed
// first. It reads the primary (see ListFiles).
func (s *Store) ListTrash(ctx context.Context, userID uuid.UUID, limit, offset int) ([]File, error) {
	rows, err := s.pool.Query(ctx,
		fileSelect+` WHERE user_id = $1 AND deleted_at IS NOT NULL
		 ORDER BY deleted_at DESC, id DESC LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		f, err := scanFileRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// EmptyTrash permanently removes all of a user's trashed files and returns the
// deleted rows for blob cleanup.
func (s *Store) EmptyTrash(ctx context.Context, userID uuid.UUID) ([]File, error) {
	return s.deleteReturning(ctx,
		`DELETE FROM files WHERE user_id = $1 AND deleted_at IS NOT NULL RETURNING `+fileColumns, userID)
}

// PurgeTrashedOlderThan permanently removes trashed files whose deleted_at is
// before cutoff (auto-purge) and returns the rows for blob cleanup.
func (s *Store) PurgeTrashedOlderThan(ctx context.Context, cutoff time.Time) ([]File, error) {
	return s.deleteReturning(ctx,
		`DELETE FROM files WHERE deleted_at IS NOT NULL AND deleted_at < $1 RETURNING `+fileColumns, cutoff)
}

// PurgeFileByID unconditionally hard-deletes a file row by id (no ownership or
// trash check). Used to roll back a reserved row when its blob write fails.
func (s *Store) PurgeFileByID(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM files WHERE id = $1`, id)
	return err
}

// ListAllFiles returns every file for a user (no limit) — used for data export
// and account deletion.
func (s *Store) ListAllFiles(ctx context.Context, userID uuid.UUID) ([]File, error) {
	rows, err := s.pool.Query(ctx, fileSelect+` WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		f, err := scanFileRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ListFilesAfter returns up to limit of the user's non-trashed files, newest
// first by (created_at, id), continuing after the given file (nil: from the
// start). It pages by key rather than offset, so a file uploaded or deleted
// meanwhile cannot shift rows between pages. Reads the primary (see ListFiles).
func (s *Store) ListFilesAfter(ctx context.Context, userID uuid.UUID, after *File, limit int) ([]File, error) {
	return s.listFilesAfter(ctx, userID, after, limit, false)
}

// ListAllFilesAfter is ListFilesAfter including the files in the trash. The
// data export uses it: trashed files are still the user's data, and the export
// must not load every row of a large account at once.
func (s *Store) ListAllFilesAfter(ctx context.Context, userID uuid.UUID, after *File, limit int) ([]File, error) {
	return s.listFilesAfter(ctx, userID, after, limit, true)
}

func (s *Store) listFilesAfter(ctx context.Context, userID uuid.UUID, after *File, limit int, withTrash bool) ([]File, error) {
	var afterAt *time.Time
	var afterID *uuid.UUID
	if after != nil {
		afterAt, afterID = &after.CreatedAt, &after.ID
	}
	rows, err := s.pool.Query(ctx, fileSelect+` WHERE user_id = $1 AND ($5 OR deleted_at IS NULL)
		 AND ($3::timestamptz IS NULL OR (created_at, id) < ($3::timestamptz, $4::uuid))
		 ORDER BY created_at DESC, id DESC LIMIT $2`, userID, limit, afterAt, afterID, withTrash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		f, err := scanFileRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// RenameFile updates a file's display name (original_name) if owned by the user.
// Returns ErrNotFound if no such file exists.
func (s *Store) RenameFile(ctx context.Context, id, userID uuid.UUID, name string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE files SET original_name = $3 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`, id, userID, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UserStats returns aggregate counts for a user, read from the primary (see
// ListFiles).
func (s *Store) UserStats(ctx context.Context, userID uuid.UUID) (Stats, error) {
	var st Stats
	err := s.pool.QueryRow(ctx,
		`SELECT count(*), coalesce(sum(size_bytes),0), coalesce(sum(views),0) FROM files WHERE user_id = $1 AND deleted_at IS NULL`,
		userID).Scan(&st.FileCount, &st.TotalBytes, &st.TotalViews)
	return st, err
}

// ErrUnknownTimeZone is returned by ActivityByDay when Postgres does not know
// the requested time zone name.
var ErrUnknownTimeZone = errors.New("unknown time zone")

// ActivityByDay returns per-day upload counts since the cutoff (for the activity
// heatmap / streak / wrapped). A "day" is a calendar day in the named time zone
// (an IANA name resolved by Postgres), so the buckets line up with the local
// dates the client renders; the server's own session zone plays no role.
func (s *Store) ActivityByDay(ctx context.Context, userID uuid.UUID, since time.Time, tz string) ([]DayCount, error) {
	// Resolve the zone once up front: the bucketing query below only converts
	// the rows it finds, so for a user with no uploads in the window an unknown
	// zone was never evaluated and "succeeded" with an empty result, while the
	// same request with one upload answered ErrUnknownTimeZone.
	if _, err := s.reader().Exec(ctx, `SELECT now() AT TIME ZONE $1::text`, tz); err != nil {
		return nil, activityErr(err)
	}
	rows, err := s.reader().Query(ctx,
		`SELECT (created_at AT TIME ZONE $3)::date AS d, count(*)
		 FROM files WHERE user_id = $1 AND created_at >= $2 AND deleted_at IS NULL
		 GROUP BY d ORDER BY d`, userID, since, tz)
	if err != nil {
		return nil, activityErr(err)
	}
	defer rows.Close()
	var out []DayCount
	for rows.Next() {
		var dc DayCount
		if err := rows.Scan(&dc.Day, &dc.Count); err != nil {
			return nil, activityErr(err)
		}
		out = append(out, dc)
	}
	return out, activityErr(rows.Err())
}

// activityErr maps Postgres' "time zone not recognized" (SQLSTATE 22023,
// invalid_parameter_value) onto ErrUnknownTimeZone so the handler can answer
// 400 instead of 500. pgx may report it from Query or only once rows are read.
func activityErr(err error) error {
	// Matched through the SQLState method (*pgconn.PgError implements it) rather
	// than the concrete type, so the store needs no pgconn import for this.
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) && pgErr.SQLState() == "22023" {
		return ErrUnknownTimeZone
	}
	return err
}

// DeleteExpiredFiles removes files past their expires_at or max_views and
// returns the deleted rows so the caller can purge blobs. Used by the janitor.
func (s *Store) DeleteExpiredFiles(ctx context.Context) ([]File, error) {
	return s.deleteReturning(ctx,
		`DELETE FROM files
		 WHERE (expires_at IS NOT NULL AND expires_at <= now())
		    OR (max_views IS NOT NULL AND views >= max_views)
		 RETURNING `+fileColumns)
}

// DeleteFilesOlderThan removes files created before cutoff (global retention)
// and returns the deleted rows for blob cleanup.
func (s *Store) DeleteFilesOlderThan(ctx context.Context, cutoff time.Time) ([]File, error) {
	return s.deleteReturning(ctx,
		`DELETE FROM files WHERE created_at < $1 RETURNING `+fileColumns, cutoff)
}

func (s *Store) deleteReturning(ctx context.Context, sql string, args ...any) ([]File, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		f, err := scanFileRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

const fileColumns = `id, user_id, slug, original_name, mime_type, size_bytes, storage_key, thumb_key, width, height, duration_ms, views, delete_key, content_hash, dominant_color, has_hls, created_at, deleted_at, encrypted`

const fileSelect = `SELECT ` + fileColumns + ` FROM files`

func (s *Store) scanFile(row pgx.Row) (File, error) {
	f, err := scanFileRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return File{}, ErrNotFound
	}
	return f, err
}

// scanFileRow scans the columns of fileSelect (also used by RETURNING above).
func scanFileRow(row pgx.Row) (File, error) {
	var f File
	err := row.Scan(&f.ID, &f.UserID, &f.Slug, &f.OriginalName, &f.MimeType, &f.SizeBytes,
		&f.StorageKey, &f.ThumbKey, &f.Width, &f.Height, &f.DurationMs, &f.Views, &f.DeleteKey,
		&f.ContentHash, &f.DominantColor, &f.HasHLS, &f.CreatedAt, &f.DeletedAt, &f.Encrypted)
	return f, err
}
