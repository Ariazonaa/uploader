package upload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"uploader/internal/blobcrypt"
	"uploader/internal/db/store"
	"uploader/internal/jobs"
	"uploader/internal/media"
	"uploader/internal/storage"
)

// ErrBlockedType is returned when an upload's sniffed MIME type matches the
// operator's blocklist.
var ErrBlockedType = errors.New("file type is not allowed")

// ErrSlugTaken is returned when a user-requested custom slug is already in use.
var ErrSlugTaken = errors.New("slug already taken")

// ErrEncryptionUnavailable is returned when an encrypted upload is requested but
// no encryption key is configured on the service.
var ErrEncryptionUnavailable = errors.New("encryption is not configured")

// ErrUnsanitizable is returned when EXIF stripping was requested for a JPEG
// whose metadata could not be removed (neither losslessly nor by re-encoding).
// The upload is refused rather than stored with its metadata. It wraps
// ErrBlockedType so the handlers answer 415 without knowing about it.
var ErrUnsanitizable = fmt.Errorf("%w: image metadata could not be removed", ErrBlockedType)

// Service runs the upload pipeline: sniff, optional EXIF strip, store, persist
// and post-process (thumbnail/dimensions/HLS).
type Service struct {
	store   *store.Store
	storage storage.Storage
	slugLen int

	// queue, when set, runs media post-processing (thumbnail/dimensions/HLS) in
	// the background instead of inline on the request path. nil = inline.
	queue *jobs.Queue
	// hlsQueue, when set, runs HLS transcodes separately from queue, so a
	// long transcode does not occupy a thumbnail worker and the number of
	// concurrent transcodes is the queue's worker count. nil = HLS runs as part
	// of the queue job (or inline).
	hlsQueue *jobs.Queue
	// blocklist returns the current comma/space/newline-separated MIME blocklist
	// (read from runtime settings). nil = nothing blocked.
	blocklist func() string
	// retentionDays returns the current global retention window (0 = keep
	// forever), read from runtime settings. nil = no retention.
	retentionDays func() int
	// encKey is the 32-byte AES-256-GCM key for at-rest blob encryption. Empty
	// until SetEncryptionKey is called; encrypted uploads then fail closed.
	encKey []byte
}

// NewService wires the upload service.
func NewService(st *store.Store, blobs storage.Storage, slugLen int) *Service {
	return &Service{store: st, storage: blobs, slugLen: slugLen}
}

// SetJobQueue enables background media post-processing via the given queue.
func (s *Service) SetJobQueue(q *jobs.Queue) { s.queue = q }

// SetHLSQueue routes HLS transcodes to their own queue (see Service.hlsQueue).
func (s *Service) SetHLSQueue(q *jobs.Queue) { s.hlsQueue = q }

// SetBlocklist installs a callback returning the current MIME blocklist string.
func (s *Service) SetBlocklist(fn func() string) { s.blocklist = fn }

// SetRetention installs a callback returning the current global retention
// window in days, which deduplication must respect.
func (s *Service) SetRetention(fn func() int) { s.retentionDays = fn }

// SetEncryptionKey installs the at-rest encryption key (32 bytes). Without it,
// requests to encrypt an upload return ErrEncryptionUnavailable.
func (s *Service) SetEncryptionKey(key []byte) { s.encKey = key }

// SaveOptions carries the per-upload knobs for Save/SaveWith.
type SaveOptions struct {
	// StripEXIF removes JPEG metadata before storing (privacy).
	StripEXIF bool
	// CustomSlug, when non-empty, requests a specific (pre-validated) slug; a
	// collision returns ErrSlugTaken instead of retrying with a random slug.
	CustomSlug string
	// Encrypt stores the blob AES-256-GCM encrypted at rest. Encrypted uploads
	// skip dedup and all media post-processing (thumbnail/HLS/dominant color).
	Encrypt bool
	// SlugStrategy names how an auto-generated slug is built (random/gfycat/date).
	// Blank or unknown falls back to random. Ignored when CustomSlug is set.
	SlugStrategy string
}

// maxSlugAttempts bounds retries on the (astronomically rare) slug collision.
const maxSlugAttempts = 5

// postCommitTimeout bounds each database step SaveWith takes once the row is
// inserted. Those steps run detached from the client's request context.
const postCommitTimeout = time.Minute

// generateSlug is GenerateSlug; tests replace it to force slug collisions.
var generateSlug = GenerateSlug

// Save stores an uploaded file with a random slug. r is expected to already be
// size-limited by the caller (http.MaxBytesReader). stripEXIF lets the caller
// override the default per upload (ShareX uploads keep the default).
func (s *Service) Save(ctx context.Context, userID uuid.UUID, r io.Reader, filename string, stripEXIF bool) (store.File, error) {
	return s.SaveWith(ctx, userID, r, filename, SaveOptions{StripEXIF: stripEXIF})
}

// SaveWith runs the full upload pipeline with the given options. filename is
// the client's and is stored through CleanFilename. A non-empty
// opts.CustomSlug must already be validated (see ValidCustomSlug); a collision
// returns ErrSlugTaken instead of retrying with a random slug. opts.Encrypt
// stores the blob encrypted at rest (skipping dedup and media post-processing).
func (s *Service) SaveWith(ctx context.Context, userID uuid.UUID, r io.Reader, filename string, opts SaveOptions) (store.File, error) {
	// Fail closed before touching the body if encryption was asked for but the
	// key is not configured.
	if opts.Encrypt && len(s.encKey) == 0 {
		return store.File{}, ErrEncryptionUnavailable
	}
	filename = CleanFilename(filename)

	// Sniff the content type from the first bytes rather than trusting the client.
	sniff := make([]byte, 512)
	n, err := io.ReadFull(r, sniff)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return store.File{}, err
	}
	sniff = sniff[:n]
	mime := http.DetectContentType(sniff)
	body := io.MultiReader(bytes.NewReader(sniff), r)

	// Reject blocked MIME types up front (sniffed, never trusting the client).
	if s.blocklist != nil && blockedContent(s.blocklist(), mime, sniff) {
		return store.File{}, ErrBlockedType
	}

	ext := SanitizeExt(filename)
	if ext == "" {
		ext = extFromMime(mime)
	}

	slug, storageKey, deduped, err := s.reserveSlug(ctx, userID, filename, mime, ext, body, opts)
	if err != nil {
		return store.File{}, err
	}

	// Row and blob are committed. A client hanging up now must not turn the
	// stored file into an error, nor skip its inline post-processing.
	ctx = context.WithoutCancel(ctx)
	f, err := s.readBack(ctx, slug)
	if err != nil {
		return store.File{}, err
	}
	// Identical content already existed for this user → return it as-is; the blob
	// and its thumbnail/metadata are already there.
	if deduped {
		return f, nil
	}

	// Encrypted blobs are opaque on disk: thumbnail/dimension/dominant-color/HLS
	// post-processing would read the plaintext-derived bytes and write cleartext
	// artifacts next to the ciphertext, defeating the encryption. Skip it.
	if opts.Encrypt {
		return f, nil
	}

	// Post-processing (thumbnail/dimensions/dominant color/HLS) is best-effort
	// and never fails the upload. With a job queue it runs in the background so
	// the response returns immediately; otherwise it runs inline.
	if s.queue != nil {
		fileID, key, slugCopy, mimeCopy := f.ID, storageKey, slug, mime
		s.queue.Submit(func(bg context.Context) {
			s.processMedia(bg, fileID, key, slugCopy, mimeCopy)
		})
		return f, nil
	}

	s.processMedia(ctx, f.ID, storageKey, slug, mime)
	// Re-load so the returned record reflects the inline post-processing.
	if updated, err := s.readBack(ctx, slug); err == nil {
		f = updated
	}
	return f, nil
}

// readBack loads a row SaveWith just wrote (or deduplicated against) from the
// primary: it was written moments ago, and a lagging replica would report it
// missing — failing an upload whose row and blob are already committed.
func (s *Service) readBack(ctx context.Context, slug string) (store.File, error) {
	ctx, cancel := context.WithTimeout(ctx, postCommitTimeout)
	defer cancel()
	return s.store.GetFileBySlugPrimary(ctx, slug)
}

// rollback removes a reserved row whose blob could not be written. It runs on
// a context detached from the client: when the client's hang-up is what failed
// the write, a rollback on the same context would fail too and leave a row
// pointing at a blob that does not exist.
func (s *Service) rollback(ctx context.Context, id uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), postCommitTimeout)
	defer cancel()
	_ = s.store.PurgeFileByID(ctx, id)
}

// processMedia runs the best-effort post-processing for a stored file: thumbnail
// + dimensions + dominant color, plus HLS transcoding for videos (handed to the
// HLS queue when there is one). It is called either inline or from the job
// queue and never returns an error.
func (s *Service) processMedia(ctx context.Context, fileID uuid.UUID, storageKey, slug, mime string) {
	thumbKey := ThumbKey(slug)
	res := media.Process(ctx, s.storage, storageKey, thumbKey, mime)
	if res.ThumbKey != nil || res.Width != nil || res.Height != nil || res.DurationMs != nil {
		_ = s.store.UpdateFileMedia(ctx, fileID, res.ThumbKey, res.Width, res.Height, res.DurationMs)
	}
	if res.DominantColor != nil {
		_ = s.store.SetDominantColor(ctx, fileID, *res.DominantColor)
	}
	if strings.HasPrefix(mime, "video/") && media.HLSAvailable() {
		if s.hlsQueue != nil {
			s.hlsQueue.Submit(func(bg context.Context) {
				s.processHLS(bg, fileID, storageKey, slug, res.Video)
			})
			return
		}
		s.processHLS(ctx, fileID, storageKey, slug, res.Video)
	}
}

// processHLS transcodes a video into its HLS rendition and flags the row.
//
// The file can be purged while the transcode runs (minutes, for a long
// video). The purge deletes the row and then the hls/ prefix; segments this job
// publishes after that would be orphaned until the orphan scan catches them
// (an hour later at the earliest, and never if the slug is reused). So the row
// is checked before the work and again after publishing, and the rendition is
// removed whenever the row is gone — or the transcode failed part-way through
// publishing.
//
// probed is what the thumbnail step's ffprobe run reported (nil: not probed),
// so the video is not probed a second time.
func (s *Service) processHLS(ctx context.Context, fileID uuid.UUID, storageKey, slug string, probed *media.VideoInfo) {
	if exists, err := s.store.FileExists(ctx, fileID); err == nil && !exists {
		return
	}
	prefix := HLSPrefix(slug, storageKey)
	ok, err := media.TranscodeHLS(ctx, s.storage, storageKey, prefix, probed)
	if err != nil {
		// Use a fresh context: ctx may be the very thing that ended the transcode
		// (shutdown), and the partial segments must still go.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		_ = s.storage.DeletePrefix(cleanupCtx, prefix)
		return
	}
	if !ok {
		return
	}
	_ = s.store.SetHasHLS(ctx, fileID)
	if exists, err := s.store.FileExists(ctx, fileID); err == nil && !exists {
		_ = s.storage.DeletePrefix(ctx, prefix)
	}
}

// reserveSlug stores the blob and inserts the DB row, retrying on slug
// collision. It returns the chosen slug, storage key and whether the upload was
// deduplicated against an identical existing file of the same user.
func (s *Service) reserveSlug(ctx context.Context, userID uuid.UUID, filename, mime, ext string, body io.Reader, opts SaveOptions) (string, string, bool, error) {
	stripEXIF := opts.StripEXIF
	customSlug := opts.CustomSlug
	// Spool the upload to a temp file rather than holding it all in memory; a
	// slug-collision retry re-reads from disk. EXIF stripping streams from this
	// file into a second one, so no upload is ever held on the heap whole.
	tmp, err := os.CreateTemp("", "uploader-*")
	if err != nil {
		return "", "", false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	// The content hash for deduplication is taken while spooling rather than
	// by reading the spooled file back.
	hasher := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(tmp, hasher), body)
	closeErr := tmp.Close()
	if copyErr != nil {
		return "", "", false, copyErr
	}
	if closeErr != nil {
		return "", "", false, closeErr
	}

	if media.IsJPEG(mime) && stripEXIF {
		// Fails closed: a JPEG whose metadata cannot be removed is refused, never
		// stored as uploaded (that would publish its GPS position).
		clean, err := media.SanitizeJPEG(ctx, tmpName)
		if err != nil {
			if errors.Is(err, media.ErrJPEGUnsanitizable) {
				return "", "", false, fmt.Errorf("%w (%v)", ErrUnsanitizable, err)
			}
			return "", "", false, err
		}
		defer os.Remove(clean)
		_ = os.Remove(tmpName) // the unstripped original is not needed any more
		tmpName = clean
		hasher = nil // the stored content is the stripped file, hashed below
	}

	// Hash the final (post-strip) content for deduplication. A custom slug is an
	// explicit request for a distinct named link, so it bypasses dedup. Encrypted
	// uploads skip hashing+dedup entirely: the plaintext hash would leak content
	// equality across encrypted blobs, and the stored ciphertext differs per
	// upload anyway, so content_hash stays NULL.
	var contentHash *string
	if !opts.Encrypt {
		var hash string
		if hasher != nil {
			hash = hex.EncodeToString(hasher.Sum(nil))
		} else if hash, err = hashFile(tmpName); err != nil {
			return "", "", false, err
		}
		contentHash = &hash
		if customSlug == "" {
			if existing, ok := s.dedupTarget(ctx, userID, hash); ok {
				return existing.Slug, existing.StorageKey, true, nil
			}
		}
	}

	// A secret per-file deletion key (stable across slug-collision retries),
	// surfaced as the ShareX DeletionURL.
	deleteKey, err := RandomSlug(24)
	if err != nil {
		return "", "", false, err
	}

	info, err := os.Stat(tmpName)
	if err != nil {
		return "", "", false, err
	}
	size := info.Size()

	// A custom slug gets a single attempt: a collision is a user-facing error
	// (ErrSlugTaken), not something to paper over with a random slug.
	attempts := maxSlugAttempts
	if customSlug != "" {
		attempts = 1
	}

	// Insert the DB row first, then write the blob. Reserving the slug via the
	// unique constraint before touching storage means a collision is detected
	// without ever writing (and possibly overwriting an existing blob at the same
	// storage key — which a custom slug colliding with a trashed file could do).
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		slug := customSlug
		if slug == "" {
			slug, err = generateSlug(opts.SlugStrategy, s.slugLen)
			if err != nil {
				return "", "", false, err
			}
		}
		storageKey := StorageKey(slug, ext)

		id := uuid.New()
		err = s.store.CreateFile(ctx, store.File{
			ID:           id,
			UserID:       userID,
			Slug:         slug,
			OriginalName: filename,
			MimeType:     mime,
			// size_bytes is always the PLAINTEXT length (from the temp file), even
			// for encrypted blobs whose on-disk size is header+tags larger.
			SizeBytes:   size,
			StorageKey:  storageKey,
			DeleteKey:   &deleteKey,
			ContentHash: contentHash,
			Encrypted:   opts.Encrypt,
		})
		if err != nil {
			if isUniqueViolation(err) {
				if customSlug != "" {
					return "", "", false, ErrSlugTaken
				}
				lastErr = err
				continue
			}
			return "", "", false, err
		}

		// Row reserved → write the blob. If that fails, roll the row back so we
		// don't leave a DB record pointing at a missing blob. When encrypting, the
		// temp-file reader is wrapped so only ciphertext ever reaches storage.
		// The body is spooled already, so the write no longer depends on the
		// client and is not cut short by its hang-up.
		blob, err := os.Open(tmpName)
		if err != nil {
			s.rollback(ctx, id)
			return "", "", false, err
		}
		var src io.Reader = blob
		if opts.Encrypt {
			enc, encErr := blobcrypt.NewEncryptingReader(blob, s.encKey)
			if encErr != nil {
				_ = blob.Close()
				s.rollback(ctx, id)
				return "", "", false, encErr
			}
			src = enc
		}
		_, putErr := s.storage.Put(context.WithoutCancel(ctx), storageKey, src)
		_ = blob.Close()
		if putErr != nil {
			s.rollback(ctx, id)
			return "", "", false, putErr
		}
		return slug, storageKey, false, nil
	}
	return "", "", false, errors.Join(errors.New("could not allocate unique slug"), lastErr)
}

// dedupTarget returns the user's existing copy of an upload's content, if it is
// safe to hand out instead of storing the upload: the newest non-trashed copy,
// and only if its blob is in place and it is not about to be deleted.
func (s *Service) dedupTarget(ctx context.Context, userID uuid.UUID, hash string) (store.File, bool) {
	existing, err := s.store.GetFileByHash(ctx, userID, hash)
	if err != nil {
		return store.File{}, false
	}
	// Rows are inserted before their blob is written, so the copy may belong to
	// a concurrent upload that is still writing it, or about to roll back.
	if st, err := s.storage.Stat(ctx, existing.StorageKey); err != nil || st.Size != existing.SizeBytes {
		return store.File{}, false
	}
	// Global retention deletes by created_at, so a re-upload would inherit the
	// copy's remaining lifetime, which may be minutes. Reuse a copy only while
	// at least half of the window is still ahead of it.
	if s.retentionDays != nil {
		if days := s.retentionDays(); days > 0 && time.Since(existing.CreatedAt) > time.Duration(days)*12*time.Hour {
			return store.File{}, false
		}
	}
	return existing, true
}

// hashFile returns the hex SHA-256 of a file's contents (streamed).
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// extFromMime maps a sniffed MIME type to a sensible extension.
func extFromMime(mime string) string {
	switch mime {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "application/pdf":
		return ".pdf"
	case "text/plain; charset=utf-8":
		return ".txt"
	default:
		return ""
	}
}
