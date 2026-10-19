//go:build integration

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"uploader/internal/auth"
	"uploader/internal/blobcrypt"
	"uploader/internal/db/store"
	"uploader/internal/settings"
	"uploader/internal/storage"
	"uploader/internal/upload"
)

// serveEnv is a Server with a real database and blob store behind the public
// routes.
type serveEnv struct {
	t     *testing.T
	ctx   context.Context
	s     *Server
	st    *store.Store
	blobs *storage.FileStorage
	h     http.Handler
	owner store.User
}

func newServeEnv(t *testing.T) *serveEnv {
	t.Helper()
	s, st := integrationServer(t)
	blobs, err := storage.NewFileStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.storage = blobs
	s.settings = settings.NewStatic(settings.Snapshot{})
	s.SetEncryptionKey(blobcrypt.DeriveKey(bytes.Repeat([]byte{7}, 32)))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	owner := store.User{ID: uuid.New(), Username: "owner", Email: "owner@example.com", PasswordHash: "x"}
	if err := st.CreateUser(ctx, owner); err != nil {
		t.Fatal(err)
	}
	return &serveEnv{t: t, ctx: ctx, s: s, st: st, blobs: blobs, h: s.Routes(), owner: owner}
}

// file stores content as a blob (sealed when encrypt is set) plus its row.
func (e *serveEnv) file(slug, mime string, content []byte, encrypt bool) store.File {
	e.t.Helper()
	f := store.File{
		ID: uuid.New(), UserID: e.owner.ID, Slug: slug, OriginalName: slug + ".bin", MimeType: mime,
		SizeBytes: int64(len(content)), StorageKey: upload.StorageKey(slug, ".bin"), Encrypted: encrypt,
	}
	var src io.Reader = bytes.NewReader(content)
	if encrypt {
		enc, err := blobcrypt.NewEncryptingReader(src, e.s.encKey())
		if err != nil {
			e.t.Fatal(err)
		}
		src = enc
	} else {
		sum := sha256.Sum256(content)
		hash := hex.EncodeToString(sum[:])
		f.ContentHash = &hash
	}
	if _, err := e.blobs.Put(e.ctx, f.StorageKey, src); err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.CreateFile(e.ctx, f); err != nil {
		e.t.Fatal(err)
	}
	return f
}

func (e *serveEnv) do(method, target string, header map[string]string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *serveEnv) views(slug string) int64 {
	e.t.Helper()
	f, err := e.st.GetFileBySlugPrimary(e.ctx, slug)
	if err != nil {
		e.t.Fatal(err)
	}
	return f.Views
}

// Only a GET that starts sending the file counts as a view: not the further
// range requests of a player, and not a revalidation answered with 304.
func TestRawCountsOnlyViewsFromTheStart(t *testing.T) {
	e := newServeEnv(t)
	content := bytes.Repeat([]byte("0123456789"), 1000)
	f := e.file("counted", "video/mp4", content, false)
	enc := e.file("counted-enc", "video/mp4", content, true)

	for _, c := range []struct {
		name   string
		header map[string]string
		status int
		counts bool
	}{
		{"full", nil, http.StatusOK, true},
		{"range from 0", map[string]string{"Range": "bytes=0-1"}, http.StatusPartialContent, true},
		{"range from an offset", map[string]string{"Range": "bytes=5000-5999"}, http.StatusPartialContent, false},
		{"suffix range", map[string]string{"Range": "bytes=-100"}, http.StatusPartialContent, false},
		{"unsatisfiable range", map[string]string{"Range": "bytes=999999-"}, http.StatusRequestedRangeNotSatisfiable, false},
	} {
		for _, file := range []store.File{f, enc} {
			before := e.views(file.Slug)
			rec := e.do(http.MethodGet, "/raw/"+file.Slug+".mp4", c.header)
			if rec.Code != c.status {
				t.Fatalf("%s (%s): status %d, want %d", c.name, file.Slug, rec.Code, c.status)
			}
			want := int64(0)
			if c.counts {
				want = 1
			}
			if counted := e.views(file.Slug) - before; counted != want {
				t.Errorf("%s (%s): counted %d views, want %d", c.name, file.Slug, counted, want)
			}
		}
	}

	etag := e.do(http.MethodGet, "/raw/"+f.Slug, nil).Header().Get("ETag")
	before := e.views(f.Slug)
	if rec := e.do(http.MethodGet, "/raw/"+f.Slug, map[string]string{"If-None-Match": etag}); rec.Code != http.StatusNotModified {
		t.Fatalf("revalidation: status %d, want 304", rec.Code)
	}
	if e.views(f.Slug) != before {
		t.Error("a 304 revalidation counted as a view")
	}
}

// The public routes answer HEAD like GET, without a body and without
// recording a view.
func TestPublicRoutesAnswerHead(t *testing.T) {
	e := newServeEnv(t)
	content := bytes.Repeat([]byte("v"), 4096)
	f := e.file("probed", "video/mp4", content, false)

	for _, c := range []struct {
		path   string
		status int
	}{
		{"/raw/" + f.Slug + ".mp4", http.StatusOK},
		{"/s/" + f.Slug, http.StatusOK},
		{"/p/" + f.Slug, http.StatusOK},
		{"/oembed?slug=" + f.Slug, http.StatusOK},
		{"/thumb/" + f.Slug, http.StatusNotFound}, // no thumbnail generated
		{"/hls/" + f.Slug + "/index.m3u8", http.StatusNotFound},
	} {
		get := e.do(http.MethodGet, c.path, nil)
		head := e.do(http.MethodHead, c.path, nil)
		if get.Code != c.status || head.Code != c.status {
			t.Errorf("%s: GET %d, HEAD %d; want %d for both", c.path, get.Code, head.Code, c.status)
		}
		if head.Header().Get("Content-Type") != get.Header().Get("Content-Type") {
			t.Errorf("%s: HEAD Content-Type %q, GET %q", c.path, head.Header().Get("Content-Type"), get.Header().Get("Content-Type"))
		}
	}
	if rec := e.do(http.MethodHead, "/raw/"+f.Slug, nil); rec.Header().Get("Content-Length") != "4096" || rec.Body.Len() != 0 {
		t.Errorf("HEAD /raw: Content-Length %q, %d body bytes; want 4096 and none", rec.Header().Get("Content-Length"), rec.Body.Len())
	}

	// Only the GETs above count: GET /raw bumps the view counter, and GET /raw
	// and GET /s each add one hit to the referrer/UA breakdown.
	if v := e.views(f.Slug); v != 1 {
		t.Errorf("views = %d, want 1 (HEAD must not count)", v)
	}
	agents, err := e.st.TopUACategories(e.ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var hits int64
	for _, a := range agents {
		hits += a.Count
	}
	if hits != 2 {
		t.Errorf("view breakdown holds %d hits, want 2 (GET /raw and GET /s)", hits)
	}
}

// Segment URIs in the playlist carry the file's id, so a slug reused after a
// purge never resolves to segments cached for the earlier file.
func TestHLSPlaylistPinsSegmentsToTheFile(t *testing.T) {
	e := newServeEnv(t)
	f := e.file("streamed", "video/mp4", []byte("mp4"), false)
	if err := e.st.SetHasHLS(e.ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	prefix := upload.HLSPrefix(f.Slug, f.StorageKey)
	playlist := "#EXTM3U\n#EXTINF:6.0,\nseg000.ts\n#EXT-X-ENDLIST\n"
	for name, body := range map[string]string{"index.m3u8": playlist, "seg000.ts": "segment"} {
		if _, err := e.blobs.Put(e.ctx, prefix+"/"+name, bytes.NewReader([]byte(body))); err != nil {
			t.Fatal(err)
		}
	}
	base := "/hls/" + f.Slug + "/"

	rec := e.do(http.MethodGet, base+"index.m3u8", nil)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte("\nseg000.ts?v="+f.ID.String()+"\n")) {
		t.Fatalf("playlist: status %d, body %q; want seg000.ts?v=%s", rec.Code, rec.Body, f.ID)
	}
	if rec.Header().Get("Last-Modified") != "" || rec.Header().Get("ETag") == "" {
		t.Fatalf("playlist validators: Last-Modified %q, ETag %q; want only an ETag",
			rec.Header().Get("Last-Modified"), rec.Header().Get("ETag"))
	}
	// A copy cached before the rewrite is revalidated by date only; it must
	// get the rewritten playlist, not a 304.
	if rec := e.do(http.MethodGet, base+"index.m3u8", map[string]string{
		"If-Modified-Since": time.Now().Add(time.Hour).UTC().Format(http.TimeFormat),
	}); rec.Code != http.StatusOK {
		t.Fatalf("playlist revalidated by date: status %d, want 200", rec.Code)
	}
	if rec := e.do(http.MethodGet, base+"index.m3u8", map[string]string{"If-None-Match": rec.Header().Get("ETag")}); rec.Code != http.StatusNotModified {
		t.Fatalf("playlist revalidated by ETag: status %d, want 304", rec.Code)
	}

	rec = e.do(http.MethodGet, base+"seg000.ts?v="+f.ID.String(), nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("versioned segment: status %d, Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if rec := e.do(http.MethodGet, base+"seg000.ts?v="+uuid.NewString(), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("segment of another file: status %d, want 404", rec.Code)
	}
	rec = e.do(http.MethodGet, base+"seg000.ts", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("unversioned segment: status %d, Cache-Control %q; want 200, no-cache", rec.Code, rec.Header().Get("Cache-Control"))
	}
}

func TestPlayerIsFrameableUnderTheGlobalPolicy(t *testing.T) {
	e := newServeEnv(t)
	f := e.file("framed", "video/mp4", []byte("mp4"), false)
	rec := e.do(http.MethodGet, "/p/"+f.Slug, nil)
	csp := rec.Header().Get("Content-Security-Policy")
	if rec.Code != http.StatusOK || rec.Header().Get("X-Frame-Options") != "" {
		t.Fatalf("status %d, X-Frame-Options %q", rec.Code, rec.Header().Get("X-Frame-Options"))
	}
	for _, want := range []string{"frame-ancestors https:", "object-src 'none'", "base-uri 'self'", "script-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("player CSP %q lacks %q", csp, want)
		}
	}
}

// authed calls a handler as the env's owner.
func (e *serveEnv) authed(h http.HandlerFunc, target string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req = req.WithContext(auth.WithUser(req.Context(), &e.owner))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// The exports are per-user data written outside writeJSON; no cache may keep
// a copy.
func TestExportsAreNotStored(t *testing.T) {
	e := newServeEnv(t)
	e.file("exported", "image/png", []byte("png"), false)
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"stats csv":   e.authed(e.s.handleStatsExport, "/api/v1/stats/export"),
		"stats json":  e.authed(e.s.handleStatsExport, "/api/v1/stats/export?format=json"),
		"auth export": e.authed(e.s.handleExport, "/api/v1/auth/export"),
	} {
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: status %d, Cache-Control %q; want 200, no-store", name, rec.Code, rec.Header().Get("Cache-Control"))
		}
	}
}

func TestStatsCSVDefusesFormulaNames(t *testing.T) {
	e := newServeEnv(t)
	f := e.file("formula", "image/png", []byte("png"), false)
	if err := e.st.RenameFile(e.ctx, f.ID, e.owner.ID, `=HYPERLINK("https://evil.example","x")`); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(e.authed(e.s.handleStatsExport, "/api/v1/stats/export").Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1][1] != `'=HYPERLINK("https://evil.example","x")` {
		t.Fatalf("CSV rows = %q; want the name prefixed with an apostrophe", rows)
	}
}

// A zone the browser knows but Postgres does not yields UTC days, not a 400
// that leaves the heatmap empty.
func TestActivityFallsBackToUTCForUnknownZone(t *testing.T) {
	e := newServeEnv(t)
	e.file("today", "image/png", []byte("png"), false)
	for _, tz := range []string{"Mars/Olympus_Mons", "UTC"} {
		rec := e.authed(e.s.handleActivity, "/api/v1/stats/activity?tz="+tz)
		var days []struct {
			Day   string `json:"day"`
			Count int    `json:"count"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &days); rec.Code != http.StatusOK || err != nil {
			t.Fatalf("tz=%s: status %d, %v", tz, rec.Code, err)
		}
		if len(days) != 1 || days[0].Day != time.Now().UTC().Format("2006-01-02") || days[0].Count != 1 {
			t.Fatalf("tz=%s: %v; want today's upload in its UTC day", tz, days)
		}
	}
}

// A huge search term is cut to maxSearchRunes and still matches on its
// prefix. Uncut, a 100 KB term against 300 files took about 3 s.
func TestListFilesCapsTheSearchTerm(t *testing.T) {
	e := newServeEnv(t)
	for i := 0; i < 300; i++ {
		f := store.File{ID: uuid.New(), UserID: e.owner.ID, Slug: fmt.Sprintf("s%04d", i),
			OriginalName: fmt.Sprintf("holiday photo %d.png", i), MimeType: "image/png", StorageKey: fmt.Sprintf("k/%d", i)}
		if err := e.st.CreateFile(e.ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	f := e.file("long", "image/png", []byte("png"), false)
	prefix := strings.Repeat("a", maxSearchRunes)
	if err := e.st.RenameFile(e.ctx, f.ID, e.owner.ID, prefix+".png"); err != nil {
		t.Fatal(err)
	}
	// Varied text (many distinct trigrams) from a fixed linear congruential
	// sequence.
	tail := make([]byte, 100<<10)
	for i, x := 0, uint32(1); i < len(tail); i++ {
		x = x*1103515245 + 12345
		tail[i] = "bcdefghijklmnopqrstuvwxyz0123456789"[(x>>16)%35]
	}

	start := time.Now()
	rec := e.authed(e.s.handleListFiles, "/api/v1/files?q="+prefix+string(tail))
	elapsed := time.Since(start)
	var files []fileView
	if err := json.Unmarshal(rec.Body.Bytes(), &files); rec.Code != http.StatusOK || err != nil {
		t.Fatalf("status %d, %v", rec.Code, err)
	}
	if len(files) != 1 || files[0].ID != f.ID {
		t.Fatalf("listed %d files, want only the one whose name starts with the term", len(files))
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("a 100 KB search term took %v", elapsed)
	}
}

// A purge runs after its row has freed the slug. Blobs that a new file with
// the same slug has taken over in the meantime must survive it.
func TestPurgeSkipsKeysOfAReclaimedSlug(t *testing.T) {
	e := newServeEnv(t)
	exists := func(key string) bool {
		_, err := e.blobs.Stat(e.ctx, key)
		return err == nil
	}
	put := func(key, body string) {
		t.Helper()
		if _, err := e.blobs.Put(e.ctx, key, strings.NewReader(body)); err != nil {
			t.Fatal(err)
		}
	}
	purge := func(f store.File) {
		t.Helper()
		if _, err := e.st.DeleteFile(e.ctx, f.ID, e.owner.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.st.HardDeleteFile(e.ctx, f.ID, e.owner.ID); err != nil {
			t.Fatal(err)
		}
	}
	withMedia := func(f store.File) store.File {
		t.Helper()
		thumb := upload.ThumbKey(f.Slug)
		put(thumb, "thumb")
		put(upload.HLSPrefix(f.Slug, f.StorageKey)+"/index.m3u8", "#EXTM3U")
		if err := e.st.UpdateFileMedia(e.ctx, f.ID, &thumb, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
		f.ThumbKey = &thumb
		return f
	}

	// Nobody took the slug: everything goes.
	alone := withMedia(e.file("alone", "video/mp4", []byte("old"), false))
	purge(alone)
	e.s.purgeFileBlobs(e.ctx, alone)
	if exists(alone.StorageKey) || exists(*alone.ThumbKey) || exists(upload.HLSPrefix(alone.Slug, alone.StorageKey)+"/index.m3u8") {
		t.Fatal("purge of an unclaimed slug left blobs behind")
	}

	// A new upload claimed the slug between the row delete and the blob purge.
	old := withMedia(e.file("custom", "video/mp4", []byte("old"), false))
	purge(old)
	reclaimed := withMedia(e.file("custom", "video/mp4", []byte("new"), false))
	e.s.purgeFileBlobs(e.ctx, old)
	for _, key := range []string{reclaimed.StorageKey, *reclaimed.ThumbKey, upload.HLSPrefix(reclaimed.Slug, reclaimed.StorageKey) + "/index.m3u8"} {
		if !exists(key) {
			t.Errorf("purging the old file deleted %s of the new one", key)
		}
	}
	rec := e.do(http.MethodGet, "/raw/custom", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "new" {
		t.Fatalf("new file after the old purge: status %d, body %q", rec.Code, rec.Body)
	}

	// Same slug, other extension: the old blob is not the new file's, so it goes.
	other := e.file("ext", "image/png", []byte("old"), false)
	purge(other)
	f := store.File{ID: uuid.New(), UserID: e.owner.ID, Slug: "ext", OriginalName: "ext.gif", MimeType: "image/gif",
		SizeBytes: 3, StorageKey: upload.StorageKey("ext", ".gif")}
	if err := e.st.CreateFile(e.ctx, f); err != nil {
		t.Fatal(err)
	}
	e.s.purgeFileBlobs(e.ctx, other)
	if exists(other.StorageKey) {
		t.Error("old blob under another extension was kept")
	}
}

func TestListTrashPages(t *testing.T) {
	e := newServeEnv(t)
	for _, slug := range []string{"t1", "t2", "t3"} {
		f := e.file(slug, "image/png", []byte(slug), false)
		if _, err := e.st.DeleteFile(e.ctx, f.ID, e.owner.ID); err != nil {
			t.Fatal(err)
		}
	}
	page := func(query string) []fileView {
		t.Helper()
		rec := e.authed(e.s.handleListTrash, "/api/v1/files/trash"+query)
		var files []fileView
		if err := json.Unmarshal(rec.Body.Bytes(), &files); rec.Code != http.StatusOK || err != nil {
			t.Fatalf("%s: status %d, %v", query, rec.Code, err)
		}
		return files
	}
	if n := len(page("")); n != 3 {
		t.Fatalf("default page: %d files, want 3", n)
	}
	first, rest := page("?limit=2"), page("?limit=2&offset=2")
	if len(first) != 2 || len(rest) != 1 || first[0].Slug != "t3" || rest[0].Slug != "t1" {
		t.Fatalf("pages %v / %v; want t3,t2 then t1", first, rest)
	}
}

// The streamed stats export covers every live file across several pages and
// writes the same JSON as encoding the whole list at once.
func TestStatsExportStreamsAllPages(t *testing.T) {
	e := newServeEnv(t)
	if body := e.authed(e.s.handleStatsExport, "/api/v1/stats/export?format=json").Body.String(); body != "[]\n" {
		t.Fatalf("empty JSON export = %q, want []", body)
	}
	const n = statsExportPage + 2
	var trashed uuid.UUID
	for i := 0; i < n; i++ {
		f := store.File{ID: uuid.New(), UserID: e.owner.ID, Slug: fmt.Sprintf("x%04d", i),
			OriginalName: fmt.Sprintf("file %d.png", i), MimeType: "image/png", StorageKey: fmt.Sprintf("k/%d", i)}
		if err := e.st.CreateFile(e.ctx, f); err != nil {
			t.Fatal(err)
		}
		trashed = f.ID
	}
	if _, err := e.st.DeleteFile(e.ctx, trashed, e.owner.ID); err != nil {
		t.Fatal(err)
	}

	rec := e.authed(e.s.handleStatsExport, "/api/v1/stats/export?format=json")
	var views []fileView
	if err := json.Unmarshal(rec.Body.Bytes(), &views); err != nil {
		t.Fatalf("JSON export does not parse: %v", err)
	}
	seen := map[uuid.UUID]bool{}
	for _, v := range views {
		if seen[v.ID] || v.ID == trashed {
			t.Fatalf("%s exported twice or from the trash", v.Slug)
		}
		seen[v.ID] = true
	}
	if len(views) != n-1 {
		t.Fatalf("JSON export has %d files, want %d", len(views), n-1)
	}
	var whole bytes.Buffer
	enc := json.NewEncoder(&whole)
	enc.SetIndent("", "  ")
	if err := enc.Encode(views); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rec.Body.Bytes(), whole.Bytes()) {
		t.Fatal("streamed JSON differs from encoding the list in one go")
	}

	rows, err := csv.NewReader(e.authed(e.s.handleStatsExport, "/api/v1/stats/export").Body).ReadAll()
	if err != nil || len(rows) != n { // header + n-1 files
		t.Fatalf("CSV export: %d rows, %v; want %d", len(rows), err, n)
	}
}

// An encrypted blob that does not decrypt is an uncacheable error page, not a
// 200 with a week of caching and a body that breaks off.
func TestRawRefusesUndecryptableBlobUpFront(t *testing.T) {
	e := newServeEnv(t)
	plain := bytes.Repeat([]byte("secret "), 20000)
	f := e.file("sealed", "application/octet-stream", plain, true)

	rec := e.do(http.MethodGet, "/raw/"+f.Slug, nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), plain) {
		t.Fatalf("right key: status %d, %d bytes", rec.Code, rec.Body.Len())
	}

	e.s.SetEncryptionKey(blobcrypt.DeriveKey(bytes.Repeat([]byte{9}, 32)))
	rec = e.do(http.MethodGet, "/raw/"+f.Slug, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("wrong key: status %d, want 500", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("wrong key: Cache-Control %q, want no-store", cc)
	}
	if et := rec.Header().Get("ETag"); et != "" {
		t.Fatalf("wrong key: ETag %q set on the error page", et)
	}
}
