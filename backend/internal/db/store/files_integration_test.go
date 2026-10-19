//go:build integration

package store_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"uploader/internal/db/store"
)

// Every sort key over rows that tie on that key must still page through each
// file exactly once (ORDER BY ends in the primary key).
func TestListFilesPaginationWithTies(t *testing.T) {
	e := newEnv(t)
	u := e.user("pager", false)
	other := e.user("other", false)
	const n = 11
	for i := 0; i < n; i++ {
		e.file(u.ID, fmt.Sprintf("tie%02d", i))
	}
	e.file(other.ID, "foreign")
	trashed := e.file(u.ID, "trashed")
	if _, err := e.st.DeleteFile(e.ctx, trashed.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	// Identical sort keys on every column the list can sort by.
	e.exec(`UPDATE files SET created_at = '2026-01-01T12:00:00Z', size_bytes = 42, views = 7,
		original_name = 'same name.png' WHERE user_id = $1`, u.ID)

	for _, sort := range []string{"created", "size", "views", "name", "", "bogus"} {
		for _, desc := range []bool{false, true} {
			for _, pageSize := range []int{1, 3, 4} {
				for _, search := range []string{"", "same"} {
					name := fmt.Sprintf("sort=%s/desc=%v/page=%d/search=%q", sort, desc, pageSize, search)
					seen := map[uuid.UUID]int{}
					for offset := 0; offset < n+pageSize; offset += pageSize {
						page, err := e.st.ListFiles(e.ctx, u.ID, store.FileQuery{Sort: sort, Desc: desc, Limit: pageSize, Offset: offset, Search: search})
						if err != nil {
							t.Fatalf("%s: %v", name, err)
						}
						for _, f := range page {
							seen[f.ID]++
							if f.UserID != u.ID || f.DeletedAt != nil {
								t.Fatalf("%s: listed foreign or trashed file %s", name, f.Slug)
							}
						}
					}
					if len(seen) != n {
						t.Errorf("%s: saw %d distinct files, want %d", name, len(seen), n)
					}
					for id, c := range seen {
						if c != 1 {
							t.Errorf("%s: file %s listed %d times", name, id, c)
						}
					}
				}
			}
		}
	}
	// Order must actually follow the direction on the tie-breaker.
	asc, _ := e.st.ListFiles(e.ctx, u.ID, store.FileQuery{Sort: "size", Limit: n})
	desc, _ := e.st.ListFiles(e.ctx, u.ID, store.FileQuery{Sort: "size", Desc: true, Limit: n})
	for i := range asc {
		if asc[i].ID != desc[n-1-i].ID {
			t.Fatal("descending order is not the reverse of ascending order")
		}
	}
}

func TestListFilesFilters(t *testing.T) {
	e := newEnv(t)
	u := e.user("filter", false)
	img := e.file(u.ID, "img")
	vid := e.file(u.ID, "vid")
	e.exec(`UPDATE files SET mime_type = 'video/mp4', original_name = 'holiday clip.mp4' WHERE id = $1`, vid.ID)
	e.exec(`UPDATE files SET original_name = 'screenshot.png' WHERE id = $1`, img.ID)

	only := func(q store.FileQuery, want uuid.UUID) {
		t.Helper()
		q.Limit = 10
		got, err := e.st.ListFiles(e.ctx, u.ID, q)
		if err != nil {
			t.Fatalf("%+v: %v", q, err)
		}
		if len(got) != 1 || got[0].ID != want {
			t.Fatalf("%+v returned %d file(s), want only %s", q, len(got), want)
		}
	}
	only(store.FileQuery{MimePrefix: "video/"}, vid.ID)
	only(store.FileQuery{Search: "HOLIDAY"}, vid.ID)   // ILIKE substring
	only(store.FileQuery{Search: "screnshot"}, img.ID) // trigram typo tolerance
	only(store.FileQuery{Search: "clip", MimePrefix: "video/"}, vid.ID)
	// LIKE metacharacters in the search and type filter match literally.
	for _, q := range []store.FileQuery{{Search: "%"}, {Search: "_"}, {Search: `\`}, {MimePrefix: "%"}, {MimePrefix: "_"}} {
		q.Limit = 10
		if got, err := e.st.ListFiles(e.ctx, u.ID, q); err != nil || len(got) != 0 {
			t.Fatalf("%+v matched %d file(s) (err %v), want none", q, len(got), err)
		}
	}
	pct := e.file(u.ID, "pct")
	e.exec(`UPDATE files SET original_name = '100% done_final.txt' WHERE id = $1`, pct.ID)
	e.exec(`UPDATE files SET original_name = '100abcdefghijklmnopdonexqrstuvwxyz.png' WHERE id = $1`, img.ID)
	only(store.FileQuery{Search: "100%"}, pct.ID)
	only(store.FileQuery{Search: "done_"}, pct.ID)
}

func TestSoftDeleteVisibility(t *testing.T) {
	e := newEnv(t)
	u := e.user("owner", false)
	intruder := e.user("intruder", false)
	f := e.file(u.ID, "soft")
	hash := "abc123"
	key := "delkey"
	e.exec(`UPDATE files SET content_hash = $2, delete_key = $3 WHERE id = $1`, f.ID, hash, key)

	if _, err := e.st.GetFileBySlugPrimary(e.ctx, f.Slug); err != nil {
		t.Fatalf("GetFileBySlugPrimary before delete: %v", err)
	}
	if _, err := e.st.DeleteFile(e.ctx, f.ID, intruder.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete by non-owner = %v, want ErrNotFound", err)
	}
	trashed, err := e.st.DeleteFile(e.ctx, f.ID, u.ID)
	if err != nil || trashed.DeletedAt == nil {
		t.Fatalf("soft delete = %+v, %v", trashed, err)
	}
	if _, err := e.st.DeleteFile(e.ctx, f.ID, u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}

	for name, get := range map[string]func() (store.File, error){
		"GetFileBySlug":        func() (store.File, error) { return e.st.GetFileBySlug(e.ctx, f.Slug) },
		"GetFileBySlugPrimary": func() (store.File, error) { return e.st.GetFileBySlugPrimary(e.ctx, f.Slug) },
		"GetFileByID":          func() (store.File, error) { return e.st.GetFileByID(e.ctx, f.ID, u.ID) },
		"GetFileByHash":        func() (store.File, error) { return e.st.GetFileByHash(e.ctx, u.ID, hash) },
		"GetFileByDeleteKey":   func() (store.File, error) { return e.st.GetFileByDeleteKey(e.ctx, key) },
	} {
		if _, err := get(); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s returned a trashed file (err %v)", name, err)
		}
	}
	if err := e.st.RenameFile(e.ctx, f.ID, u.ID, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RenameFile on a trashed file = %v, want ErrNotFound", err)
	}
	if st, _ := e.st.UserStats(e.ctx, u.ID); st.FileCount != 0 || st.TotalBytes != 0 {
		t.Errorf("UserStats counts trashed file: %+v", st)
	}
	if list, _ := e.st.ListFiles(e.ctx, u.ID, store.FileQuery{Limit: 10}); len(list) != 0 {
		t.Errorf("ListFiles shows trashed file")
	}
	if trash, _ := e.st.ListTrash(e.ctx, u.ID, 10, 0); len(trash) != 1 || trash[0].ID != f.ID {
		t.Errorf("ListTrash = %v", trash)
	}
	// Still referenced for the orphan scan, still existing for jobs.
	keys, _ := e.st.ReferencedKeys(e.ctx, []string{f.StorageKey})
	if _, ok := keys[f.StorageKey]; !ok {
		t.Error("ReferencedKeys omits a trashed file's blob")
	}
	if ok, err := e.st.FileExists(e.ctx, f.ID); err != nil || !ok {
		t.Errorf("FileExists(trashed) = %v, %v; want true", ok, err)
	}

	// Restore makes it visible again, including on the primary read.
	if _, err := e.st.RestoreFile(e.ctx, f.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := e.st.GetFileBySlugPrimary(e.ctx, f.Slug); err != nil || got.ID != f.ID || got.DeletedAt != nil {
		t.Fatalf("GetFileBySlugPrimary after restore = %+v, %v", got, err)
	}
	if _, err := e.st.RestoreFile(e.ctx, f.ID, u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("restoring a live file = %v, want ErrNotFound", err)
	}
	// HardDeleteFile only removes trashed rows.
	if _, err := e.st.HardDeleteFile(e.ctx, f.ID, u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("hard delete of a live file = %v, want ErrNotFound", err)
	}
	if _, err := e.st.DeleteFile(e.ctx, f.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	emptied, err := e.st.EmptyTrash(e.ctx, u.ID)
	if err != nil || len(emptied) != 1 {
		t.Fatalf("EmptyTrash = %v, %v", emptied, err)
	}
	if ok, err := e.st.FileExists(e.ctx, f.ID); err != nil || ok {
		t.Fatalf("FileExists(purged) = %v, %v; want false", ok, err)
	}
	if ok, err := e.st.FileExists(e.ctx, uuid.New()); err != nil || ok {
		t.Fatalf("FileExists(random) = %v, %v; want false", ok, err)
	}
}

// Full column round trip through CreateFile/UpdateFileMedia/... and the
// shared fileColumns/scanFileRow pair.
func TestFileColumnsRoundTrip(t *testing.T) {
	e := newEnv(t)
	u := e.user("rt", false)
	hash, dk := "h", "dk"
	f := store.File{ID: uuid.New(), UserID: u.ID, Slug: "rt", OriginalName: "a b.png", MimeType: "image/png",
		SizeBytes: 12345, StorageKey: "r/rt", DeleteKey: &dk, ContentHash: &hash, Encrypted: true}
	if err := e.st.CreateFile(e.ctx, f); err != nil {
		t.Fatal(err)
	}
	thumb, w, h, d := "t/rt", 640, 480, int64(1500)
	if err := e.st.UpdateFileMedia(e.ctx, f.ID, &thumb, &w, &h, &d); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetDominantColor(e.ctx, f.ID, "#112233"); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetHasHLS(e.ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.st.RecordViews(e.ctx, []store.ViewDelta{{Slug: f.Slug, Views: 1}}); err != nil {
		t.Fatal(err)
	}
	got, err := e.st.GetFileBySlugPrimary(e.ctx, f.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != f.ID || got.UserID != u.ID || got.OriginalName != f.OriginalName || got.SizeBytes != f.SizeBytes ||
		got.StorageKey != f.StorageKey || got.ThumbKey == nil || *got.ThumbKey != thumb || got.Width == nil || *got.Width != w ||
		got.Height == nil || *got.Height != h || got.DurationMs == nil || *got.DurationMs != d || got.Views != 1 ||
		got.DeleteKey == nil || *got.DeleteKey != dk || got.ContentHash == nil || *got.ContentHash != hash ||
		got.DominantColor == nil || *got.DominantColor != "#112233" || !got.HasHLS || !got.Encrypted || got.DeletedAt != nil {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	keys, _ := e.st.ReferencedKeys(e.ctx, []string{thumb, f.StorageKey, "t/unknown"})
	if _, ok := keys[thumb]; !ok || len(keys) != 2 {
		t.Errorf("ReferencedKeys = %v, want the thumb and storage key only", keys)
	}
	if byHash, err := e.st.GetFileByHash(e.ctx, u.ID, hash); err != nil || byHash.ID != f.ID {
		t.Errorf("GetFileByHash = %v, %v", byHash.ID, err)
	}
	// A duplicate slug is a unique violation, never an overwrite.
	dup := f
	dup.ID = uuid.New()
	if err := e.st.CreateFile(e.ctx, dup); sqlState(err) != "23505" {
		t.Fatalf("duplicate slug = %v, want 23505", err)
	}
}

func TestActivityByDayTimeZone(t *testing.T) {
	e := newEnv(t)
	u := e.user("tz", false)
	late := e.file(u.ID, "late")
	noon := e.file(u.ID, "noon")
	trashed := e.file(u.ID, "trashed")
	// 23:30 UTC on 10 March is 00:30 on 11 March in Berlin (CET, UTC+1).
	e.exec(`UPDATE files SET created_at = '2026-03-10T23:30:00Z' WHERE id = $1`, late.ID)
	e.exec(`UPDATE files SET created_at = '2026-03-10T12:00:00Z' WHERE id IN ($1, $2)`, noon.ID, trashed.ID)
	if _, err := e.st.DeleteFile(e.ctx, trashed.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	days := func(tz string) map[string]int64 {
		t.Helper()
		rows, err := e.st.ActivityByDay(e.ctx, u.ID, since, tz)
		if err != nil {
			t.Fatalf("ActivityByDay(%s): %v", tz, err)
		}
		out := map[string]int64{}
		for _, r := range rows {
			out[r.Day.Format("2006-01-02")] = r.Count
		}
		return out
	}
	if got := days("UTC"); len(got) != 1 || got["2026-03-10"] != 2 {
		t.Errorf("UTC buckets = %v, want 2026-03-10:2", got)
	}
	if got := days("Europe/Berlin"); len(got) != 2 || got["2026-03-10"] != 1 || got["2026-03-11"] != 1 {
		t.Errorf("Europe/Berlin buckets = %v, want 2026-03-10:1 2026-03-11:1", got)
	}
	// The session zone must play no role.
	e.exec(`ALTER DATABASE ` + currentDB(e) + ` SET timezone = 'Pacific/Kiritimati'`)
	e.pool.Reset()
	if got := days("UTC"); got["2026-03-10"] != 2 {
		t.Errorf("UTC buckets depend on the session zone: %v", got)
	}

	if _, err := e.st.ActivityByDay(e.ctx, u.ID, since, "Mars/Olympus_Mons"); !errors.Is(err, store.ErrUnknownTimeZone) {
		t.Fatalf("unknown zone with matching rows = %v, want ErrUnknownTimeZone", err)
	}
	// Same answer when the user has no uploads in the window: the zone is
	// validated even if no row is ever converted.
	empty := e.user("empty", false)
	if _, err := e.st.ActivityByDay(e.ctx, empty.ID, since, "Mars/Olympus_Mons"); !errors.Is(err, store.ErrUnknownTimeZone) {
		t.Fatalf("unknown zone without rows = %v, want ErrUnknownTimeZone", err)
	}
	if rows, err := e.st.ActivityByDay(e.ctx, empty.ID, since, "Europe/Berlin"); err != nil || len(rows) != 0 {
		t.Fatalf("valid zone without rows = %v, %v", rows, err)
	}
}

func currentDB(e *env) string {
	var name string
	if err := e.pool.QueryRow(e.ctx, `SELECT quote_ident(current_database())`).Scan(&name); err != nil {
		e.t.Fatal(err)
	}
	return name
}

// Postgres itself rejects NUL bytes and invalid UTF-8 in TEXT, and the \u0000
// escape in JSONB; the API cleans audit fields first (api.cleanText, covered
// by the api package's integration test). Here: cleaned text round-trips, raw
// text fails the INSERT (documents why the cleaning exists).
func TestAuditInsertEncoding(t *testing.T) {
	e := newEnv(t)
	u := e.user("auditor", false)

	clean := store.AuditEvent{
		UserID: &u.ID, ActorLabel: "müller �", Event: store.AuditLoginFailure,
		Target: "tärget", IP: "::1", UserAgent: "Mozilla/5.0 ☃",
		Metadata: json.RawMessage(`{"name":"ok é"}`),
	}
	if err := e.st.InsertAuditEvent(e.ctx, clean); err != nil {
		t.Fatalf("clean insert: %v", err)
	}
	if err := e.st.InsertAuditEvent(e.ctx, store.AuditEvent{Event: store.AuditLogout}); err != nil {
		t.Fatalf("insert with empty metadata: %v", err)
	}

	for name, ev := range map[string]store.AuditEvent{
		"nul in text":       {Event: store.AuditLoginFailure, ActorLabel: "a\x00b"},
		"invalid utf8":      {Event: store.AuditLoginFailure, UserAgent: "bad \xff\xfe"},
		"nul escape jsonb":  {Event: store.AuditLoginFailure, Metadata: json.RawMessage(`{"k":"a\u0000b"}`)},
		"invalid json meta": {Event: store.AuditLoginFailure, Metadata: json.RawMessage(`{`)},
	} {
		if err := e.st.InsertAuditEvent(e.ctx, ev); err == nil {
			t.Errorf("%s: raw insert unexpectedly succeeded", name)
		} else {
			t.Logf("%s: rejected by Postgres as expected (SQLSTATE %s)", name, sqlState(err))
		}
	}

	events, total, err := e.st.ListAuditEvents(e.ctx, store.AuditFilter{Limit: 10})
	if err != nil || total != 2 || len(events) != 2 {
		t.Fatalf("ListAuditEvents = %d/%d, %v; want 2", len(events), total, err)
	}
	filtered, total, err := e.st.ListAuditEvents(e.ctx, store.AuditFilter{Event: store.AuditLoginFailure, UserID: &u.ID, Limit: 10})
	if err != nil || total != 1 || len(filtered) != 1 {
		t.Fatalf("filtered = %d/%d, %v", len(filtered), total, err)
	}
	got := filtered[0]
	if got.ActorLabel != clean.ActorLabel || got.Target != clean.Target || got.UserAgent != clean.UserAgent || got.IP != "::1" {
		t.Fatalf("round trip = %+v", got)
	}
	var md map[string]string
	if err := json.Unmarshal(got.Metadata, &md); err != nil || md["name"] != "ok é" {
		t.Fatalf("metadata = %s, %v", got.Metadata, err)
	}
	page, total, _ := e.st.ListAuditEvents(e.ctx, store.AuditFilter{Limit: 1, Offset: 1})
	if total != 2 || len(page) != 1 {
		t.Fatalf("paged = %d/%d", len(page), total)
	}
	// ON DELETE SET NULL keeps the audit trail when the user goes.
	if err := e.st.DeleteUserUnlessLastAdmin(e.ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE user_id IS NULL AND actor_label = $1`, clean.ActorLabel); n != 1 {
		t.Fatal("audit row lost or still linked after user deletion")
	}
}

// Settings upserts: SetSettingIfAbsent never clobbers an edit.
func TestSettingsSeedDoesNotClobber(t *testing.T) {
	e := newEnv(t)
	if err := e.st.SetSettingIfAbsent(e.ctx, "k", "default"); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetSetting(e.ctx, "k", "edited"); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetSettingIfAbsent(e.ctx, "k", "default"); err != nil {
		t.Fatal(err)
	}
	all, err := e.st.GetAllSettings(e.ctx)
	if err != nil || all["k"] != "edited" {
		t.Fatalf("settings = %v, %v", all, err)
	}
}
