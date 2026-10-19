package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"uploader/internal/auth"
	"uploader/internal/config"
	"uploader/internal/db/store"
	"uploader/internal/settings"
	"uploader/internal/upload"
)

// tusHarness wires the tus handlers behind a fake auth layer (the test user id
// comes from a header) so they can be exercised without Postgres.
type tusHarness struct {
	t   *testing.T
	s   *Server
	h   http.Handler
	dir string
}

func newTusHarness(t *testing.T) *tusHarness {
	t.Helper()
	dir := t.TempDir()
	s := &Server{
		cfg:      &config.Config{StorageFileRoot: dir, DirectUploadLimit: 1 << 30},
		settings: settings.NewStatic(settings.Snapshot{PublicURL: "https://up.example"}),
	}
	t.Cleanup(func() { tusSaveHook = nil })
	return &tusHarness{t: t, s: s, h: newTusHarnessRouter(s), dir: dir}
}

// newTusHarnessRouter routes the tus endpoints of s behind the fake auth layer.
func newTusHarnessRouter(s *Server) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := uuid.Parse(r.Header.Get("X-Test-User"))
			if err != nil {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), &store.User{ID: id})))
		})
	})
	r.Options("/api/v1/tus", s.handleTusOptions)
	r.Post("/api/v1/tus", s.handleTusCreate)
	r.Head("/api/v1/tus/{id}", s.handleTusHead)
	r.Patch("/api/v1/tus/{id}", s.handleTusPatch)
	r.Delete("/api/v1/tus/{id}", s.handleTusDelete)
	return r
}

func (th *tusHarness) do(ctx context.Context, method, path string, user uuid.UUID, hdr map[string]string, body string) *httptest.ResponseRecorder {
	th.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("X-Test-User", user.String())
	req.Header.Set("Tus-Resumable", tusVersion)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	th.h.ServeHTTP(rec, req)
	return rec
}

// create opens an upload of the given length and returns its path.
func (th *tusHarness) create(user uuid.UUID, length int) string {
	th.t.Helper()
	return th.createQ(user, length, "")
}

// createQ is create with a query string ("?encrypt=true").
func (th *tusHarness) createQ(user uuid.UUID, length int, query string) string {
	th.t.Helper()
	rec := th.do(context.Background(), http.MethodPost, "/api/v1/tus"+query, user,
		map[string]string{"Upload-Length": strconv.Itoa(length)}, "")
	if rec.Code != http.StatusCreated {
		th.t.Fatalf("create: status %d, body %s", rec.Code, rec.Body)
	}
	loc := rec.Header().Get("Location")
	return strings.TrimPrefix(loc, "https://up.example")
}

func (th *tusHarness) patch(ctx context.Context, path string, user uuid.UUID, off int, body string) *httptest.ResponseRecorder {
	th.t.Helper()
	return th.do(ctx, http.MethodPatch, path, user, map[string]string{
		"Content-Type":  "application/offset+octet-stream",
		"Upload-Offset": strconv.Itoa(off),
	}, body)
}

func (th *tusHarness) scratch() []string {
	entries, _ := os.ReadDir(filepath.Join(th.dir, ".tus"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func savedAs(slug string) func(context.Context, uuid.UUID, io.Reader, string, upload.SaveOptions) (store.File, error) {
	return func(_ context.Context, _ uuid.UUID, r io.Reader, _ string, _ upload.SaveOptions) (store.File, error) {
		_, _ = io.Copy(io.Discard, r)
		return store.File{Slug: slug}, nil
	}
}

func TestKeyedMutexDropsEntries(t *testing.T) {
	k := newKeyedMutex()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			unlock := k.Lock(strconv.Itoa(i % 5))
			unlock()
		}(i)
	}
	wg.Wait()
	if n := k.size(); n != 0 {
		t.Fatalf("keyedMutex kept %d entries after all unlocks", n)
	}

	unlock := k.Lock("a")
	if _, ok := k.TryLock("a"); ok {
		t.Fatal("TryLock succeeded on a held key")
	}
	unlock()
	u2, ok := k.TryLock("a")
	if !ok {
		t.Fatal("TryLock failed on a free key")
	}
	u2()
	if n := k.size(); n != 0 {
		t.Fatalf("keyedMutex kept %d entries after TryLock/unlock", n)
	}
}

// Requests on an upload another request holds wait only briefly (or until the
// client gives up) and are then answered 423, instead of piling up for as long
// as the holder takes.
func TestTusBusyUploadAnswers423(t *testing.T) {
	th := newTusHarness(t)
	user := uuid.New()
	path := th.create(user, 4)
	id := strings.TrimPrefix(path, "/api/v1/tus/")

	unlock := tusUploadLocks.Lock(id)
	old := tusLockWait
	tusLockWait = 20 * time.Millisecond
	t.Cleanup(func() { tusLockWait = old })
	if rec := th.patch(context.Background(), path, user, 0, "da"); rec.Code != http.StatusLocked {
		t.Fatalf("PATCH on a busy upload: status %d, want 423", rec.Code)
	}
	if rec := th.do(context.Background(), http.MethodDelete, path, user, nil, ""); rec.Code != http.StatusLocked {
		t.Fatalf("DELETE on a busy upload: status %d, want 423", rec.Code)
	}
	// A client that hangs up stops waiting at once.
	tusLockWait = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if rec := th.patch(ctx, path, user, 0, "da"); rec.Code != http.StatusLocked || time.Since(start) > 5*time.Second {
		t.Fatalf("PATCH with a cancelled context: status %d after %v", rec.Code, time.Since(start))
	}
	if n := tusUploadLocks.size(); n != 1 {
		t.Fatalf("lock table holds %d entries, want only the holder's", n)
	}
	unlock()

	tusSaveHook = savedAs("free")
	if rec := th.patch(context.Background(), path, user, 0, "data"); rec.Code != http.StatusNoContent {
		t.Fatalf("PATCH once the upload is free: status %d", rec.Code)
	}
}

// A PATCH for a malformed, unknown or foreign id must not leave anything in the
// lock table — that was an unauthenticated-size memory leak per request.
func TestTusPatchUnknownIDsLeaveNoLocks(t *testing.T) {
	th := newTusHarness(t)
	owner, other := uuid.New(), uuid.New()
	path := th.create(owner, 10)

	ids := []string{strings.Repeat("x", 8<<10), "not-a-uuid", uuid.NewString(), strings.ToUpper(uuid.NewString())}
	for i := 0; i < 200; i++ {
		ids = append(ids, uuid.NewString())
	}
	for _, id := range ids {
		if rec := th.patch(context.Background(), "/api/v1/tus/"+id, owner, 0, "x"); rec.Code != http.StatusNotFound {
			t.Fatalf("PATCH unknown id %.20q: status %d, want 404", id, rec.Code)
		}
	}
	if rec := th.patch(context.Background(), path, other, 0, "x"); rec.Code != http.StatusNotFound {
		t.Fatalf("PATCH by non-owner: status %d, want 404", rec.Code)
	}
	if n := tusUploadLocks.size(); n != 0 {
		t.Fatalf("tusUploadLocks holds %d entries after rejected PATCHes", n)
	}
}

func TestTusUploadAndResultRecovery(t *testing.T) {
	th := newTusHarness(t)
	tusSaveHook = savedAs("abc123")
	user := uuid.New()
	path := th.create(user, 6)

	if rec := th.patch(context.Background(), path, user, 0, "hel"); rec.Code != http.StatusNoContent || rec.Header().Get("Upload-Offset") != "3" {
		t.Fatalf("first chunk: status %d offset %q", rec.Code, rec.Header().Get("Upload-Offset"))
	}
	if rec := th.patch(context.Background(), path, user, 0, "hel"); rec.Code != http.StatusConflict {
		t.Fatalf("stale offset: status %d, want 409", rec.Code)
	}
	rec := th.patch(context.Background(), path, user, 3, "lo!")
	if rec.Code != http.StatusNoContent || rec.Header().Get("X-Uploader-Url") != "https://up.example/s/abc123" {
		t.Fatalf("final chunk: status %d url %q", rec.Code, rec.Header().Get("X-Uploader-Url"))
	}
	for _, name := range th.scratch() {
		if !strings.HasSuffix(name, ".done") {
			t.Fatalf("scratch file %s left after a successful upload", name)
		}
	}

	// A client that lost the final response recovers the result via HEAD …
	head := th.do(context.Background(), http.MethodHead, path, user, nil, "")
	if head.Code != http.StatusOK || head.Header().Get("Upload-Offset") != "6" ||
		head.Header().Get("X-Uploader-Url") != "https://up.example/s/abc123" {
		t.Fatalf("HEAD after finish: status %d offset %q url %q", head.Code,
			head.Header().Get("Upload-Offset"), head.Header().Get("X-Uploader-Url"))
	}
	// … or by repeating the final PATCH, which must not store the file again.
	tusSaveHook = func(context.Context, uuid.UUID, io.Reader, string, upload.SaveOptions) (store.File, error) {
		t.Fatal("finalize ran twice")
		return store.File{}, nil
	}
	if rec := th.patch(context.Background(), path, user, 6, ""); rec.Code != http.StatusNoContent ||
		rec.Header().Get("X-Uploader-Slug") != "abc123" {
		t.Fatalf("repeated final PATCH: status %d slug %q", rec.Code, rec.Header().Get("X-Uploader-Slug"))
	}
	// Somebody else's finished upload stays invisible.
	if head := th.do(context.Background(), http.MethodHead, path, uuid.New(), nil, ""); head.Code != http.StatusNotFound {
		t.Fatalf("HEAD by non-owner after finish: status %d, want 404", head.Code)
	}
}

func TestTusTransientFinalizeErrorKeepsUpload(t *testing.T) {
	th := newTusHarness(t)
	user := uuid.New()
	path := th.create(user, 4)

	tusSaveHook = func(context.Context, uuid.UUID, io.Reader, string, upload.SaveOptions) (store.File, error) {
		return store.File{}, errors.New("database is down")
	}
	if rec := th.patch(context.Background(), path, user, 0, "data"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("finalize with transient error: status %d, want 500", rec.Code)
	}
	// HEAD must not answer offset == length without a result: a standard tus
	// client would take the upload as done. It retries the final step instead,
	// and reports that it still fails.
	head := th.do(context.Background(), http.MethodHead, path, user, nil, "")
	if head.Code != http.StatusInternalServerError || head.Header().Get("Upload-Offset") == "4" {
		t.Fatalf("HEAD after transient failure: status %d offset %q, want 500 without an offset",
			head.Code, head.Header().Get("Upload-Offset"))
	}
	if names := th.scratch(); len(names) != 2 {
		t.Fatalf("scratch after transient failures = %v, want the upload kept", names)
	}

	// Retrying just the final step (empty body at offset == length) completes it.
	tusSaveHook = savedAs("retry1")
	rec := th.patch(context.Background(), path, user, 4, "")
	if rec.Code != http.StatusNoContent || rec.Header().Get("X-Uploader-Slug") != "retry1" {
		t.Fatalf("finalize retry: status %d slug %q", rec.Code, rec.Header().Get("X-Uploader-Slug"))
	}
}

// A client that only HEADs after the final PATCH failed (any standard tus
// client resuming) gets the finished upload's result from HEAD.
func TestTusHeadFinalizesCompleteUpload(t *testing.T) {
	th := newTusHarness(t)
	user := uuid.New()
	path := th.create(user, 4)
	tusSaveHook = func(context.Context, uuid.UUID, io.Reader, string, upload.SaveOptions) (store.File, error) {
		return store.File{}, errors.New("database is down")
	}
	th.patch(context.Background(), path, user, 0, "data")

	tusSaveHook = savedAs("viahead")
	head := th.do(context.Background(), http.MethodHead, path, user, nil, "")
	if head.Code != http.StatusOK || head.Header().Get("Upload-Offset") != "4" ||
		head.Header().Get("X-Uploader-Url") != "https://up.example/s/viahead" {
		t.Fatalf("HEAD of a complete upload: status %d offset %q url %q", head.Code,
			head.Header().Get("Upload-Offset"), head.Header().Get("X-Uploader-Url"))
	}
	if names := th.scratch(); len(names) != 1 || !strings.HasSuffix(names[0], ".done") {
		t.Fatalf("scratch after finalizing via HEAD = %v", names)
	}

	// An incomplete upload is just reported, not finalized.
	tusSaveHook = func(context.Context, uuid.UUID, io.Reader, string, upload.SaveOptions) (store.File, error) {
		t.Fatal("HEAD finalized an incomplete upload")
		return store.File{}, nil
	}
	partial := th.create(user, 10)
	th.patch(context.Background(), partial, user, 0, "abc")
	if head := th.do(context.Background(), http.MethodHead, partial, user, nil, ""); head.Code != http.StatusOK ||
		head.Header().Get("Upload-Offset") != "3" {
		t.Fatalf("HEAD of a partial upload: status %d offset %q", head.Code, head.Header().Get("Upload-Offset"))
	}
	if head := th.do(context.Background(), http.MethodHead, partial, uuid.New(), nil, ""); head.Code != http.StatusNotFound {
		t.Fatalf("HEAD by non-owner: status %d, want 404", head.Code)
	}
}

// tus clients send no PATCH for an empty file, so creating one finalizes it.
func TestTusEmptyUploadFinalizesOnCreate(t *testing.T) {
	th := newTusHarness(t)
	user := uuid.New()
	tusSaveHook = savedAs("empty")
	rec := th.do(context.Background(), http.MethodPost, "/api/v1/tus", user, map[string]string{"Upload-Length": "0"}, "")
	if rec.Code != http.StatusCreated || rec.Header().Get("X-Uploader-Url") != "https://up.example/s/empty" {
		t.Fatalf("create empty upload: status %d url %q", rec.Code, rec.Header().Get("X-Uploader-Url"))
	}
	path := strings.TrimPrefix(rec.Header().Get("Location"), "https://up.example")
	if head := th.do(context.Background(), http.MethodHead, path, user, nil, ""); head.Header().Get("X-Uploader-Slug") != "empty" {
		t.Fatalf("HEAD after empty upload: status %d slug %q", head.Code, head.Header().Get("X-Uploader-Slug"))
	}

	// A failure is reported by the creation itself, leaving nothing behind.
	th2 := newTusHarness(t)
	tusSaveHook = func(context.Context, uuid.UUID, io.Reader, string, upload.SaveOptions) (store.File, error) {
		return store.File{}, upload.ErrBlockedType
	}
	if rec := th2.do(context.Background(), http.MethodPost, "/api/v1/tus", user, map[string]string{"Upload-Length": "0"}, ""); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("create blocked empty upload: status %d, want 415", rec.Code)
	}
	if names := th2.scratch(); len(names) != 0 {
		t.Fatalf("scratch left by a failed empty upload: %v", names)
	}
}

func TestTusPermanentFinalizeErrorDiscardsUpload(t *testing.T) {
	th := newTusHarness(t)
	user := uuid.New()
	path := th.create(user, 4)

	tusSaveHook = func(context.Context, uuid.UUID, io.Reader, string, upload.SaveOptions) (store.File, error) {
		return store.File{}, upload.ErrBlockedType
	}
	if rec := th.patch(context.Background(), path, user, 0, "data"); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("blocked type: status %d, want 415", rec.Code)
	}
	if names := th.scratch(); len(names) != 0 {
		t.Fatalf("scratch files left after a permanent failure: %v", names)
	}
}

// The final Save must not inherit the request context: a client that drops the
// connection after sending the last byte would otherwise abort the store.
func TestTusFinalizeIgnoresClientDisconnect(t *testing.T) {
	th := newTusHarness(t)
	user := uuid.New()
	path := th.create(user, 4)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var saveCtxErr error
	saved := false
	tusSaveHook = func(ctx context.Context, _ uuid.UUID, r io.Reader, _ string, _ upload.SaveOptions) (store.File, error) {
		saved = true
		saveCtxErr = ctx.Err()
		_, _ = io.Copy(io.Discard, r)
		return store.File{Slug: "kept"}, nil
	}
	rec := th.patch(ctx, path, user, 0, "data")
	if !saved {
		t.Fatal("the final PATCH never reached Save")
	}
	if saveCtxErr != nil {
		t.Fatalf("Save ran with a cancelled context: %v", saveCtxErr)
	}
	if rec.Code != http.StatusNoContent || rec.Header().Get("X-Uploader-Url") != "https://up.example/s/kept" {
		t.Fatalf("final PATCH: status %d url %q, want 204 with the saved file's URL", rec.Code, rec.Header().Get("X-Uploader-Url"))
	}
}

func TestTusTermination(t *testing.T) {
	th := newTusHarness(t)
	owner := uuid.New()
	path := th.create(owner, 10)

	if rec := th.do(context.Background(), http.MethodDelete, path, uuid.New(), nil, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE by non-owner: status %d, want 404", rec.Code)
	}
	if rec := th.do(context.Background(), http.MethodDelete, path, owner, nil, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE by owner: status %d, want 204", rec.Code)
	}
	if names := th.scratch(); len(names) != 0 {
		t.Fatalf("scratch files left after termination: %v", names)
	}
	if rec := th.do(context.Background(), http.MethodDelete, path, owner, nil, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("second DELETE: status %d, want 404", rec.Code)
	}
	opts := th.do(context.Background(), http.MethodOptions, "/api/v1/tus", owner, nil, "")
	if !strings.Contains(opts.Header().Get("Tus-Extension"), "termination") {
		t.Fatalf("Tus-Extension %q does not advertise termination", opts.Header().Get("Tus-Extension"))
	}
}

func TestTusCapEvictsStaleUpload(t *testing.T) {
	th := newTusHarness(t)
	user := uuid.New()
	var paths []string
	for i := 0; i < maxOpenTusPerUser; i++ {
		paths = append(paths, th.create(user, 10))
	}
	// Another account is unaffected by this user's cap.
	th.create(uuid.New(), 10)

	rec := th.do(context.Background(), http.MethodPost, "/api/v1/tus", user, map[string]string{"Upload-Length": "10"}, "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("create over the cap: status %d, want 429", rec.Code)
	}
	if ra, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || ra < 1 {
		t.Fatalf("429 Retry-After = %q, want a positive number of seconds", rec.Header().Get("Retry-After"))
	}

	// Age one upload past tusStaleAfter: the next create evicts it.
	stale := strings.TrimPrefix(paths[3], "/api/v1/tus/")
	old := time.Now().Add(-2 * tusStaleAfter)
	for _, ext := range []string{"json", "bin"} {
		if err := os.Chtimes(filepath.Join(th.dir, ".tus", stale+"."+ext), old, old); err != nil {
			t.Fatal(err)
		}
	}
	th.create(user, 10)
	if _, err := os.Stat(filepath.Join(th.dir, ".tus", stale+".json")); !os.IsNotExist(err) {
		t.Fatalf("stale upload was not evicted (stat err %v)", err)
	}
	if n := len(th.s.listTusUploads(user.String())); n != maxOpenTusPerUser {
		t.Fatalf("user has %d open uploads, want %d", n, maxOpenTusPerUser)
	}
}

func TestTusCapHoldsUnderConcurrentCreates(t *testing.T) {
	th := newTusHarness(t)
	user := uuid.New()
	var wg sync.WaitGroup
	var mu sync.Mutex
	created := 0
	for i := 0; i < 3*maxOpenTusPerUser; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := th.do(context.Background(), http.MethodPost, "/api/v1/tus", user, map[string]string{"Upload-Length": "1"}, "")
			if rec.Code == http.StatusCreated {
				mu.Lock()
				created++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if created != maxOpenTusPerUser {
		t.Fatalf("%d concurrent creates succeeded, want exactly %d", created, maxOpenTusPerUser)
	}
}

// The per-upload options of a direct upload work for tus too, and are taken
// from the creating request: the final step runs later, maybe under other
// settings.
func TestTusRecordsUploadOptionsAtCreation(t *testing.T) {
	th := newTusHarness(t)
	th.s.settings = settings.NewStatic(settings.Snapshot{PublicURL: "https://up.example", StripEXIF: "true", SlugStrategy: "gfycat"})
	user := uuid.New()
	var got []upload.SaveOptions
	tusSaveHook = func(_ context.Context, _ uuid.UUID, r io.Reader, _ string, opts upload.SaveOptions) (store.File, error) {
		_, _ = io.Copy(io.Discard, r)
		got = append(got, opts)
		return store.File{Slug: "x"}, nil
	}

	explicit := th.createQ(user, 4, "?strip_exif=false&slug_strategy=date&slug=my-link")
	defaults := th.create(user, 4)
	// An upload created before options were recorded uses the current settings.
	legacy := uuid.NewString()
	if err := os.WriteFile(filepath.Join(th.dir, ".tus", legacy+".bin"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(th.dir, ".tus", legacy+".json"),
		[]byte(`{"length":4,"filename":"old","user_id":"`+user.String()+`","encrypt":false}`), 0o600); err != nil {
		t.Fatal(err)
	}

	th.s.settings = settings.NewStatic(settings.Snapshot{PublicURL: "https://up.example", StripEXIF: "false", SlugStrategy: "random"})
	for _, path := range []string{explicit, defaults} {
		if rec := th.patch(context.Background(), path, user, 0, "data"); rec.Code != http.StatusNoContent {
			t.Fatalf("PATCH %s: status %d", path, rec.Code)
		}
	}
	th.s.settings = settings.NewStatic(settings.Snapshot{PublicURL: "https://up.example", SlugStrategy: "date"})
	if rec := th.patch(context.Background(), "/api/v1/tus/"+legacy, user, 4, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("finalize of a legacy upload: status %d", rec.Code)
	}
	want := []upload.SaveOptions{
		{StripEXIF: false, SlugStrategy: "date", CustomSlug: "my-link"},
		{StripEXIF: true, SlugStrategy: "gfycat"},
		{StripEXIF: true, SlugStrategy: "date"},
	}
	if len(got) != len(want) {
		t.Fatalf("finalized %d uploads, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("upload %d finalized with %+v, want %+v", i, got[i], want[i])
		}
	}

	if rec := th.do(context.Background(), http.MethodPost, "/api/v1/tus?slug=a", user,
		map[string]string{"Upload-Length": "4"}, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("create with an invalid slug: status %d, want 400", rec.Code)
	}
}

// A custom slug that turns out to be taken is final: 409, and the upload goes.
func TestTusTakenSlugDiscardsUpload(t *testing.T) {
	th := newTusHarness(t)
	user := uuid.New()
	path := th.createQ(user, 4, "?slug=taken")
	tusSaveHook = func(context.Context, uuid.UUID, io.Reader, string, upload.SaveOptions) (store.File, error) {
		return store.File{}, upload.ErrSlugTaken
	}
	if rec := th.patch(context.Background(), path, user, 0, "data"); rec.Code != http.StatusConflict {
		t.Fatalf("taken slug: status %d, want 409", rec.Code)
	}
	if names := th.scratch(); len(names) != 0 {
		t.Fatalf("scratch files left after a taken slug: %v", names)
	}
}

func TestTusCreateRefusesEncryptionWithoutKey(t *testing.T) {
	th := newTusHarness(t)
	rec := th.do(context.Background(), http.MethodPost, "/api/v1/tus?encrypt=true", uuid.New(),
		map[string]string{"Upload-Length": "10"}, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("encrypted create without key: status %d, want 503", rec.Code)
	}
	if names := th.scratch(); len(names) != 0 {
		t.Fatalf("scratch files created for a refused upload: %v", names)
	}
}
