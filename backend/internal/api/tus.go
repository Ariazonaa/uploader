package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"uploader/internal/auth"
	"uploader/internal/db/store"
	"uploader/internal/upload"
)

// Minimal tus.io 1.0.0 server (core + creation + termination extensions) for
// resumable uploads. In-progress uploads are spooled to a ".tus" scratch dir
// inside the storage root (skipped by the orphan janitor); on completion the
// assembled file runs through the normal upload.Service.Save pipeline.
//
// Scratch layout per upload id:
//
//	<id>.json  owner, declared length, filename, upload options (in progress)
//	<id>.bin   bytes received so far; its size is the upload offset (encrypted
//	           for uploads that are stored encrypted, see tuscrypt.go)
//	<id>.done  result of a finished upload, so a client that lost the final
//	           PATCH response can still learn the share URL (HEAD/PATCH)
//
// The janitor removes every scratch file older than 24h.

const tusVersion = "1.0.0"

// maxOpenTusPerUser bounds the unfinished resumable uploads one account may hold
// open. Each one owns a scratch file in the storage root until it completes, is
// terminated (DELETE), evicted as stale, or swept by the janitor after 24h.
const maxOpenTusPerUser = 10

// tusStaleAfter is how long an unfinished upload may sit without receiving a
// byte before a new upload by the same owner may evict it once the owner is at
// maxOpenTusPerUser. It is the release valve for uploads a client abandoned
// without terminating them (closed tab, crashed script).
const tusStaleAfter = time.Hour

// tusFinalizeTimeout bounds how long the final Save may run. It is deliberately
// detached from the request context: a client that disconnects while the
// assembled file is being stored must not abort (and lose) a transfer that has
// already fully arrived.
const tusFinalizeTimeout = uploadDeadline

// tusLockWait is how long a request waits for another request on the same
// upload before it is answered 423 (clients retry that). Waiting without bound
// let one trickling PATCH park every follow-up request on the upload, each with
// its goroutine and connection, for as long as it cared to trickle. A var for
// tests.
var tusLockWait = 10 * time.Second

// keyedMutex hands out one mutex per key. An entry exists only while some
// request holds or waits for it and is dropped on the last unlock, so the map
// is bounded by the number of in-flight requests — never by the number of ids
// ever seen. (A fixed array of striped mutexes would be equally leak-free, but
// one slow client holding a stripe for a 30-minute chunk would then stall every
// unrelated upload that hashes to the same stripe.)
type keyedMutex struct {
	mu sync.Mutex
	m  map[string]*refMutex
}

// refMutex is a lock that can be waited for with a deadline: holding it means
// holding the one token sem has room for.
type refMutex struct {
	sem  chan struct{}
	refs int
}

func newKeyedMutex() *keyedMutex { return &keyedMutex{m: make(map[string]*refMutex)} }

// Lock blocks until key is held and returns the matching unlock func.
func (k *keyedMutex) Lock(key string) func() {
	e := k.acquire(key)
	e.sem <- struct{}{}
	return func() { <-e.sem; k.release(key, e) }
}

// TryLock takes key only if it is free right now.
func (k *keyedMutex) TryLock(key string) (func(), bool) {
	return k.LockWait(context.Background(), key, 0)
}

// LockWait waits for key until it is free, ctx ends or d has passed, and
// reports whether it got it.
func (k *keyedMutex) LockWait(ctx context.Context, key string, d time.Duration) (func(), bool) {
	e := k.acquire(key)
	select {
	case e.sem <- struct{}{}:
		return func() { <-e.sem; k.release(key, e) }, true
	default:
	}
	if d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case e.sem <- struct{}{}:
			return func() { <-e.sem; k.release(key, e) }, true
		case <-ctx.Done():
		case <-t.C:
		}
	}
	k.release(key, e)
	return nil, false
}

func (k *keyedMutex) acquire(key string) *refMutex {
	k.mu.Lock()
	defer k.mu.Unlock()
	e, ok := k.m[key]
	if !ok {
		e = &refMutex{sem: make(chan struct{}, 1)}
		k.m[key] = e
	}
	e.refs++
	return e
}

func (k *keyedMutex) release(key string, e *refMutex) {
	k.mu.Lock()
	defer k.mu.Unlock()
	e.refs--
	if e.refs == 0 {
		delete(k.m, key)
	}
}

// size reports how many keys currently have an entry (tests only).
func (k *keyedMutex) size() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.m)
}

var (
	// tusUploadLocks serializes PATCH/DELETE/finalize per upload id.
	tusUploadLocks = newKeyedMutex()
	// tusUserLocks makes "count open uploads, evict, create" atomic per owner so
	// concurrent creations cannot overshoot maxOpenTusPerUser. Like the rest of
	// the tus state it is per-instance: multi-instance deployments already need
	// sticky routing for tus, since the scratch files live on one node's volume.
	tusUserLocks = newKeyedMutex()
)

// tusSaveHook replaces upload.Service.SaveWith in tests (no Postgres there).
// Always nil in production.
var tusSaveHook func(ctx context.Context, userID uuid.UUID, r io.Reader, filename string, opts upload.SaveOptions) (store.File, error)

type tusInfo struct {
	Length   int64  `json:"length"`
	Filename string `json:"filename"`
	UserID   string `json:"user_id"`
	Encrypt  bool   `json:"encrypt"`
	// The pipeline options, fixed at creation like a direct upload's: the final
	// step may run much later (after a restart, too), with other settings.
	// Records from before they existed lack them; StripEXIF is a pointer so
	// such an upload falls back to the current setting, as it used to.
	StripEXIF    *bool  `json:"strip_exif,omitempty"`
	SlugStrategy string `json:"slug_strategy,omitempty"`
	CustomSlug   string `json:"slug,omitempty"`
	// ScratchKey is the wrapped data key the data file of an encrypted upload is
	// encrypted with (see tuscrypt.go); empty for a plaintext data file.
	ScratchKey string `json:"scratch_key,omitempty"`
}

// tusSaveOptions returns the pipeline options recorded for an upload.
func (s *Server) tusSaveOptions(info tusInfo) upload.SaveOptions {
	set := s.settings.Current()
	opts := upload.SaveOptions{
		StripEXIF:    set.StripEXIFBool(),
		Encrypt:      info.Encrypt,
		SlugStrategy: info.SlugStrategy,
		CustomSlug:   info.CustomSlug,
	}
	if info.StripEXIF != nil {
		opts.StripEXIF = *info.StripEXIF
	}
	if opts.SlugStrategy == "" {
		opts.SlugStrategy = set.SlugStrategyValue()
	}
	return opts
}

// tusDone is the record a finished upload leaves behind.
type tusDone struct {
	Length int64  `json:"length"`
	UserID string `json:"user_id"`
	Slug   string `json:"slug"`
}

func (s *Server) tusDir() string {
	return filepath.Join(s.cfg.StorageFileRoot, ".tus")
}

func tusHeaders(w http.ResponseWriter) {
	w.Header().Set("Tus-Resumable", tusVersion)
}

// tusID returns the upload id from the URL if it is a canonical (lowercase,
// hyphenated) UUID — the only form handleTusCreate hands out. Rejecting every
// other spelling keeps ids usable as file names and lock keys without any
// further escaping.
func tusID(r *http.Request) (string, bool) {
	raw := chi.URLParam(r, "id")
	id, err := uuid.Parse(raw)
	if err != nil || id.String() != raw {
		return "", false
	}
	return raw, true
}

// handleTusOptions advertises capabilities.
func (s *Server) handleTusOptions(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Tus-Resumable", tusVersion)
	h.Set("Tus-Version", tusVersion)
	h.Set("Tus-Extension", "creation,termination")
	h.Set("Tus-Max-Size", strconv.FormatInt(s.maxUploadBytes(), 10))
	w.WriteHeader(http.StatusNoContent)
}

// handleTusCreate creates a new upload (POST), returning its Location.
func (s *Server) handleTusCreate(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	tusHeaders(w)

	length, err := strconv.ParseInt(r.Header.Get("Upload-Length"), 10, 64)
	if err != nil || length < 0 {
		writeErr(w, http.StatusBadRequest, "invalid Upload-Length")
		return
	}
	if length > s.maxUploadBytes() {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}

	filename := tusMetadataValue(r.Header.Get("Upload-Metadata"), "filename")
	if filename == "" {
		filename = "upload"
	}
	filename = upload.CleanFilename(filename)

	// The same per-upload options as a direct upload (?slug, ?strip_exif,
	// ?encrypt, ?slug_strategy), recorded with the upload.
	opts, err := s.uploadOptions(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Refuse up front rather than after the client has sent every byte: without
	// a key the final Save would fail with ErrEncryptionUnavailable anyway.
	if opts.Encrypt && len(s.encKey()) == 0 {
		writeErr(w, http.StatusServiceUnavailable, "encryption is not configured")
		return
	}

	if err := os.MkdirAll(s.tusDir(), 0o750); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create upload")
		return
	}

	// Creating an upload reserves scratch space, so cap how many a single
	// account may have open at once. The owner lock makes count → evict →
	// create one atomic step; without it concurrent POSTs all pass the count.
	unlockUser := tusUserLocks.Lock(u.ID.String())
	defer unlockUser()
	if ok, retryAfter := s.makeTusRoom(u.ID.String()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter/time.Second)))
		writeErr(w, http.StatusTooManyRequests,
			"too many unfinished uploads; finish or abandon them first")
		return
	}

	id := uuid.NewString()
	info := tusInfo{
		Length: length, Filename: filename, UserID: u.ID.String(), Encrypt: opts.Encrypt,
		StripEXIF: &opts.StripEXIF, SlugStrategy: opts.SlugStrategy, CustomSlug: opts.CustomSlug,
	}
	// What is stored encrypted must not sit in the scratch dir in plaintext.
	if opts.Encrypt {
		if _, info.ScratchKey, err = newTusScratchKey(s.encKey(), id); err != nil {
			writeErr(w, http.StatusInternalServerError, "could not create upload")
			return
		}
	}
	// Empty data file.
	if f, err := os.OpenFile(s.tusPath(id, "bin"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create upload")
		return
	} else {
		_ = f.Close()
	}
	if err := writeJSONFile(s.tusPath(id, "json"), info); err != nil {
		_ = os.Remove(s.tusPath(id, "bin"))
		writeErr(w, http.StatusInternalServerError, "could not create upload")
		return
	}

	// An empty upload is complete as soon as it exists, and tus clients send no
	// PATCH for it (there is nothing to send), so it is finalized right here.
	// Nothing is lost by dropping it on failure: the client just creates again.
	if length == 0 {
		done, status, msg := s.finalizeTus(r.Context(), u.ID, id, info)
		if status != 0 {
			s.cleanupTus(id)
			writeErr(w, status, msg)
			return
		}
		s.writeTusDoneHeaders(w, r, done)
	}

	w.Header().Set("Location", s.publicBaseURL(r)+"/api/v1/tus/"+id)
	w.Header().Set("Upload-Offset", "0")
	w.WriteHeader(http.StatusCreated)
}

// handleTusHead reports the current offset so the client can resume. For an
// upload that has already been finalized it reports the full length plus the
// result headers, so a client that lost the final PATCH response recovers the
// share URL instead of seeing a 404.
func (s *Server) handleTusHead(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	tusHeaders(w)
	w.Header().Set("Cache-Control", "no-store")
	id, ok := tusID(r)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	info, off, err := s.tusState(id)
	if err == nil && info.UserID == u.ID.String() && off >= info.Length {
		// Every byte is here but the final step has not succeeded (it failed
		// transiently, or the server stopped during it). Answering offset ==
		// length would make a standard tus client consider the upload done
		// without ever learning its result, so run the final step now; a
		// failure is answered with its status instead.
		unlock, ok := tusUploadLocks.LockWait(r.Context(), id, tusLockWait)
		if !ok {
			w.WriteHeader(http.StatusLocked)
			return
		}
		defer unlock()
		// Re-read under the lock: a PATCH may have finished it meanwhile.
		if info, off, err = s.tusState(id); err == nil && off >= info.Length {
			done, status, _ := s.finalizeTus(r.Context(), u.ID, id, info)
			if status != 0 {
				w.WriteHeader(status)
				return
			}
			s.writeTusDoneHeaders(w, r, done)
			w.WriteHeader(http.StatusOK)
			return
		}
	}
	if err != nil {
		if done, derr := s.readTusDone(id); derr == nil && done.UserID == u.ID.String() {
			s.writeTusDoneHeaders(w, r, done)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if info.UserID != u.ID.String() {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	h := w.Header()
	h.Set("Upload-Offset", strconv.FormatInt(off, 10))
	h.Set("Upload-Length", strconv.FormatInt(info.Length, 10))
	w.WriteHeader(http.StatusOK)
}

// handleTusPatch appends a chunk at the given offset; finalizes when complete.
// A PATCH with an empty body at offset == length retries a finalize that failed
// transiently (or returns the result again if it already succeeded).
func (s *Server) handleTusPatch(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	tusHeaders(w)

	if r.Header.Get("Content-Type") != "application/offset+octet-stream" {
		writeErr(w, http.StatusUnsupportedMediaType, "expected application/offset+octet-stream")
		return
	}
	id, ok := tusID(r)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Only the owner of an upload that exists may take its lock: a malformed,
	// unknown or foreign id is answered without touching any shared state.
	if !s.tusOwnedBy(id, u.ID.String()) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	clientOff, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || clientOff < 0 {
		writeErr(w, http.StatusBadRequest, "invalid Upload-Offset")
		return
	}

	unlock, ok := tusUploadLocks.LockWait(r.Context(), id, tusLockWait)
	if !ok {
		writeErr(w, http.StatusLocked, "upload is busy; retry")
		return
	}
	defer unlock()

	// Re-read under the lock: a concurrent request may have appended, finalized
	// or terminated the upload while this one waited.
	info, off, err := s.tusState(id)
	if err != nil {
		if done, derr := s.readTusDone(id); derr == nil && done.UserID == u.ID.String() && clientOff == done.Length {
			s.writeTusDoneHeaders(w, r, done)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if info.UserID != u.ID.String() {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if clientOff != off {
		w.WriteHeader(http.StatusConflict) // offset mismatch (RFC: 409)
		return
	}

	// Cap the body so a chunk can't exceed the declared remaining length, and give
	// the transfer the longer upload read deadline (the global ReadTimeout is a
	// slow-loris defence, not an upload budget).
	extendUploadDeadline(w)
	remaining := info.Length - off
	r.Body = http.MaxBytesReader(w, r.Body, remaining)

	f, err := os.OpenFile(s.tusPath(id, "bin"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "upload write failed")
		return
	}
	dst, err := s.tusScratchWriter(id, info, off, f)
	if err != nil {
		_ = f.Close()
		if errors.Is(err, upload.ErrEncryptionUnavailable) {
			// The key is gone (restart without it): this upload can never be stored.
			s.cleanupTus(id)
			writeErr(w, http.StatusServiceUnavailable, "encryption is not configured")
			return
		}
		log.Printf("tus: upload %s: %v", id, err)
		writeErr(w, http.StatusInternalServerError, "upload write failed")
		return
	}
	n, copyErr := io.Copy(dst, r.Body)
	closeErr := f.Close()
	if copyErr != nil {
		var maxErr *http.MaxBytesError
		if errors.As(copyErr, &maxErr) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		writeErr(w, http.StatusInternalServerError, "upload write failed")
		return
	}
	// A failed close can mean the chunk never fully reached the disk; the client
	// re-reads the offset (the file's real size) and resends from there.
	if closeErr != nil {
		log.Printf("tus: closing data file of upload %s: %v", id, closeErr)
		writeErr(w, http.StatusInternalServerError, "upload write failed")
		return
	}
	newOff := off + n
	w.Header().Set("Upload-Offset", strconv.FormatInt(newOff, 10))

	if newOff < info.Length {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	done, status, msg := s.finalizeTus(r.Context(), u.ID, id, info)
	if status != 0 {
		writeErr(w, status, msg)
		return
	}
	s.writeTusDoneHeaders(w, r, done)
	w.WriteHeader(http.StatusNoContent)
}

// finalizeTus runs the assembled upload through the normal pipeline and returns
// its result record, or the status and message to answer with when it failed
// (status 0 means success). It must be called with the upload's lock held, or
// before anyone else knows the upload's id.
func (s *Server) finalizeTus(ctx context.Context, userID uuid.UUID, id string, info tusInfo) (tusDone, int, string) {
	f, err := os.Open(s.tusPath(id, "bin"))
	if err != nil {
		return tusDone{}, http.StatusInternalServerError, "finalize failed"
	}
	// The offset bookkeeping says the upload is complete; make sure the data
	// file agrees before publishing it, or a truncated file would be stored.
	// Kept for a retry: HEAD reports the real offset and the client resends.
	if st, err := f.Stat(); err != nil || st.Size() != info.Length {
		_ = f.Close()
		log.Printf("tus: data file of upload %s does not have the declared length %d (stat err %v)", id, info.Length, err)
		return tusDone{}, http.StatusInternalServerError, "finalize failed; retry"
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tusFinalizeTimeout)
	defer cancel()
	save := s.uploads.SaveWith
	if tusSaveHook != nil {
		save = tusSaveHook
	}
	var saved store.File
	src, saveErr := s.tusScratchReader(id, info, f)
	if saveErr == nil {
		saved, saveErr = save(ctx, userID, src, info.Filename, s.tusSaveOptions(info))
	}
	_ = f.Close()

	if saveErr != nil {
		if tusPermanentError(saveErr) {
			// Retrying cannot succeed, so release the scratch space now.
			s.cleanupTus(id)
			switch {
			case errors.Is(saveErr, upload.ErrBlockedType):
				return tusDone{}, http.StatusUnsupportedMediaType, "this file type is not allowed"
			case errors.Is(saveErr, upload.ErrSlugTaken):
				return tusDone{}, http.StatusConflict, "that slug is already taken"
			default:
				return tusDone{}, http.StatusServiceUnavailable, "encryption is not configured"
			}
		}
		// Transient (database hiccup, full temp dir, …): keep the fully received
		// bytes so the client can retry just the final step (an empty PATCH at
		// offset == length, or HEAD) instead of re-sending the whole file.
		log.Printf("tus: finalize save failed for user %s (kept for retry): %v", userID, saveErr)
		return tusDone{}, http.StatusInternalServerError, "finalize failed; retry"
	}

	done := tusDone{Length: info.Length, UserID: info.UserID, Slug: saved.Slug}
	if err := writeJSONFile(s.tusPath(id, "done"), done); err != nil {
		// Not fatal: the file is stored; only a lost-response retry would miss it.
		log.Printf("tus: could not record result for upload %s: %v", id, err)
	}
	_ = os.Remove(s.tusPath(id, "bin"))
	_ = os.Remove(s.tusPath(id, "json"))
	return done, 0, ""
}

// tusPermanentError reports whether a Save error is final, i.e. retrying the
// same bytes can never succeed.
func tusPermanentError(err error) bool {
	return errors.Is(err, upload.ErrBlockedType) || errors.Is(err, upload.ErrEncryptionUnavailable) ||
		errors.Is(err, upload.ErrSlugTaken)
}

// handleTusDelete implements the tus termination extension: the owner abandons
// an upload and its scratch space (and its slot under maxOpenTusPerUser) is
// released immediately rather than after the janitor's 24h sweep.
func (s *Server) handleTusDelete(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	tusHeaders(w)
	id, ok := tusID(r)
	if !ok || !s.tusOwnedBy(id, u.ID.String()) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	unlock, ok := tusUploadLocks.LockWait(r.Context(), id, tusLockWait)
	if !ok {
		writeErr(w, http.StatusLocked, "upload is busy; retry")
		return
	}
	defer unlock()
	if !s.tusOwnedBy(id, u.ID.String()) { // terminated/finished while we waited
		w.WriteHeader(http.StatusNotFound)
		return
	}
	s.cleanupTus(id)
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---

func (s *Server) tusPath(id, ext string) string {
	return filepath.Join(s.tusDir(), id+"."+ext)
}

// writeJSONFile writes v as JSON with 0600: the scratch records carry the
// owner's user id and the original filename, and only this process reads them.
func writeJSONFile(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func (s *Server) readTusInfo(id string) (tusInfo, error) {
	var info tusInfo
	b, err := os.ReadFile(s.tusPath(id, "json"))
	if err != nil {
		return info, err
	}
	err = json.Unmarshal(b, &info)
	return info, err
}

func (s *Server) readTusDone(id string) (tusDone, error) {
	var done tusDone
	b, err := os.ReadFile(s.tusPath(id, "done"))
	if err != nil {
		return done, err
	}
	err = json.Unmarshal(b, &done)
	return done, err
}

// tusOwnedBy reports whether id is an unfinished or finished upload of userID.
// id must already have passed tusID.
func (s *Server) tusOwnedBy(id, userID string) bool {
	if info, err := s.readTusInfo(id); err == nil {
		return info.UserID == userID
	}
	if done, err := s.readTusDone(id); err == nil {
		return done.UserID == userID
	}
	return false
}

func (s *Server) writeTusDoneHeaders(w http.ResponseWriter, r *http.Request, done tusDone) {
	h := w.Header()
	h.Set("Upload-Offset", strconv.FormatInt(done.Length, 10))
	h.Set("Upload-Length", strconv.FormatInt(done.Length, 10))
	// Surface the result URL via custom headers (tus PATCH has no body).
	h.Set("X-Uploader-Slug", done.Slug)
	h.Set("X-Uploader-Url", s.publicBaseURL(r)+"/s/"+done.Slug)
}

// tusState returns the upload info and current offset (data file size) of an
// unfinished upload. id must already have passed tusID.
func (s *Server) tusState(id string) (tusInfo, int64, error) {
	info, err := s.readTusInfo(id)
	if err != nil {
		return info, 0, err
	}
	st, err := os.Stat(s.tusPath(id, "bin"))
	if err != nil {
		return info, 0, err
	}
	return info, st.Size(), nil
}

// cleanupTus removes every scratch file of an upload. Callers hold its lock
// (or know nobody else can, e.g. eviction under TryLock).
func (s *Server) cleanupTus(id string) {
	_ = os.Remove(s.tusPath(id, "bin"))
	_ = os.Remove(s.tusPath(id, "json"))
	_ = os.Remove(s.tusPath(id, "done"))
}

// openTusUpload is one unfinished upload as seen by the per-owner cap.
type openTusUpload struct {
	id           string
	lastActivity time.Time
}

// listTusUploads returns a user's unfinished uploads, oldest activity first.
// The scratch directory only ever holds in-flight uploads (completed ones leave
// just a small .done record, abandoned ones are swept after 24h), so it stays
// small.
func (s *Server) listTusUploads(userID string) []openTusUpload {
	entries, err := os.ReadDir(s.tusDir())
	if err != nil {
		return nil
	}
	var out []openTusUpload
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if _, err := uuid.Parse(id); err != nil {
			continue
		}
		info, err := s.readTusInfo(id)
		if err != nil || info.UserID != userID {
			continue
		}
		out = append(out, openTusUpload{id: id, lastActivity: s.tusLastActivity(id)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].lastActivity.Before(out[j].lastActivity) })
	return out
}

// tusLastActivity is when an upload last made progress: the data file's mtime
// moves with every appended chunk, the info file's is the creation time (which
// covers an upload that never received a byte).
func (s *Server) tusLastActivity(id string) time.Time {
	var last time.Time
	for _, ext := range []string{"json", "bin"} {
		if st, err := os.Stat(s.tusPath(id, ext)); err == nil && st.ModTime().After(last) {
			last = st.ModTime()
		}
	}
	return last
}

// makeTusRoom ensures the user has a free slot under maxOpenTusPerUser,
// evicting their stalest uploads (idle longer than tusStaleAfter) if needed.
// When no slot can be freed it returns false and how long until the oldest
// remaining upload becomes evictable. Callers hold the user's tusUserLocks entry.
func (s *Server) makeTusRoom(userID string) (bool, time.Duration) {
	open := s.listTusUploads(userID)
	excess := len(open) - maxOpenTusPerUser + 1
	if excess <= 0 {
		return true, 0
	}
	var kept []openTusUpload
	for _, up := range open {
		if excess > 0 && s.evictStaleTus(up.id) {
			log.Printf("tus: evicted stale upload %s of user %s to make room", up.id, userID)
			excess--
			continue
		}
		kept = append(kept, up)
	}
	if excess <= 0 {
		return true, 0
	}
	wait := tusStaleAfter
	if len(kept) > 0 {
		wait = tusStaleAfter - time.Since(kept[0].lastActivity)
	}
	if wait < time.Second {
		wait = time.Second
	}
	return false, wait
}

// evictStaleTus removes an upload if nobody is working on it and it has been
// idle for longer than tusStaleAfter. Activity is re-checked under the upload's
// lock, since a chunk may have landed after the directory was listed.
func (s *Server) evictStaleTus(id string) bool {
	unlock, ok := tusUploadLocks.TryLock(id)
	if !ok {
		return false // a request is on it right now, so it is not stale
	}
	defer unlock()
	if time.Since(s.tusLastActivity(id)) < tusStaleAfter {
		return false
	}
	s.cleanupTus(id)
	return true
}

// tusMetadataValue decodes a value from the comma-separated, base64-encoded
// Upload-Metadata header ("key b64value, key2 b64value2").
func tusMetadataValue(header, key string) string {
	for _, pair := range strings.Split(header, ",") {
		parts := strings.Fields(strings.TrimSpace(pair))
		if len(parts) == 2 && parts[0] == key {
			if dec, err := base64.StdEncoding.DecodeString(parts[1]); err == nil {
				return sanitizeFilename(string(dec))
			}
		}
	}
	return ""
}
