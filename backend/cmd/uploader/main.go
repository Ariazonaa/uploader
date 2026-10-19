// Command uploader is the single binary for the image/file uploader: it serves
// the API+SPA and provides operational subcommands (migrate, user, healthcheck).
package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"uploader/internal/api"
	"uploader/internal/auth"
	"uploader/internal/blobcrypt"
	"uploader/internal/config"
	"uploader/internal/db"
	"uploader/internal/db/store"
	"uploader/internal/jobs"
	"uploader/internal/settings"
	"uploader/internal/storage"
	"uploader/internal/upload"
	"uploader/internal/web"
)

const version = "uploader dev"

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	var err error
	switch cmd {
	case "serve":
		err = runServe()
	case "migrate":
		err = runMigrate(os.Args[2:])
	case "user":
		err = runUser(os.Args[2:])
	case "seed":
		err = runSeed()
	case "vapid":
		err = runVAPID()
	case "healthcheck":
		err = runHealthcheck()
	case "version", "-v", "--version":
		fmt.Println(version)
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		err = fmt.Errorf("unknown command %q", cmd)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`uploader — image/file uploader

Usage:
  uploader serve                 Run the HTTP server (default)
  uploader migrate [up|down|status|reset]
  uploader user create --username U --email E --password P
  uploader user disable-2fa --username U
  uploader seed                  Seed a dev DB with a demo admin + sample files
  uploader vapid                 Generate a VAPID keypair for Web Push
  uploader healthcheck           Probe the local /ready endpoint (DB + storage)
  uploader version`)
}

func runServe() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// If started as root (so a bind-mounted data dir can be fixed up), make the
	// storage writable and drop to the unprivileged runtime user.
	if err := dropPrivileges(cfg.StorageFileRoot); err != nil {
		return fmt.Errorf("drop privileges: %w", err)
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Apply pending migrations automatically on boot.
	if err := db.Migrate(rootCtx, cfg.DatabaseURL, "up"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	poolOpts := db.PoolOptions{
		MaxConns:        poolSize(cfg.DBMaxConns),
		MinConns:        poolSize(cfg.DBMinConns),
		MaxConnLifetime: cfg.DBMaxConnLifetime,
		MaxConnIdleTime: cfg.DBMaxConnIdleTime,
	}
	pool, err := db.ConnectWith(rootCtx, cfg.DatabaseURL, poolOpts)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Optional read-replica: read-heavy public queries (share/raw/list) hit the
	// replica; all writes still go to the primary. Enables horizontal read scaling.
	st := store.New(pool)
	if cfg.DatabaseReplicaURL != "" {
		replica, err := db.ConnectWith(rootCtx, cfg.DatabaseReplicaURL, poolOpts)
		if err != nil {
			return fmt.Errorf("connect read replica: %w", err)
		}
		defer replica.Close()
		st = store.NewWithReplica(pool, replica)
		fmt.Println("uploader: using read replica for read-heavy queries")
	}

	set, err := settings.New(rootCtx, st, cfg)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	blobs, err := storage.NewFileStorage(cfg.StorageFileRoot)
	if err != nil {
		return err
	}
	sessions := auth.NewSessionManager(st, cfg.SessionHashKey, cfg.SessionBlockKey, cfg.SessionTTL, cfg.SecureCookies)
	authn := auth.NewAuthenticator(sessions, st)
	uploads := upload.NewService(st, blobs, cfg.SlugLength)

	// At-rest blob encryption key: an explicit UPLOADER_ENCRYPTION_KEY wins;
	// otherwise it is derived from the session block key. The same key is shared
	// with the API server so it can decrypt encrypted blobs when serving them.
	encKey := cfg.EncryptionKey
	if len(encKey) == 0 {
		encKey = blobcrypt.DeriveKey(cfg.SessionBlockKey)
		fmt.Println("uploader: warning — UPLOADER_ENCRYPTION_KEY is unset; the at-rest " +
			"blob key is derived from UPLOADER_SESSION_BLOCK_KEY. Rotating the session " +
			"block key will make every encrypted blob AND every stored 2FA secret " +
			"unreadable. Set an explicit key before enabling encryption in production.")
	}
	uploads.SetEncryptionKey(encKey)

	// Background worker pool for best-effort media post-processing (thumbnails,
	// dimensions) so uploads don't block on CPU-heavy work. Sized by GOMAXPROCS,
	// not NumCPU: since Go 1.25 GOMAXPROCS follows the container's CPU limit
	// (compose `cpus:`), while NumCPU reports every core of the host — on a big
	// host that meant dozens of concurrent image decodes inside a 2 GiB memory
	// limit. Their combined memory is additionally capped by media's decode
	// budget.
	mediaJobs := jobs.New("media", runtime.GOMAXPROCS(0), 256)
	defer mediaJobs.Shutdown()
	uploads.SetJobQueue(mediaJobs)
	// HLS transcodes get their own single worker: one libx264 encode already
	// uses every core it is given, a second one only adds memory pressure, and
	// a long transcode must not hold up the thumbnails queued behind it.
	hlsJobs := jobs.New("hls", 1, 64)
	defer hlsJobs.Shutdown()
	uploads.SetHLSQueue(hlsJobs)
	// MIME blocklist is read live from runtime settings on every upload.
	uploads.SetBlocklist(func() string { return set.Current().BlockedMime })
	uploads.SetRetention(func() int { return set.Current().RetentionDaysInt() })

	srv := api.NewServer(cfg, st, blobs, sessions, authn, uploads, set, web.Handler())
	srv.SetEncryptionKey(encKey)
	// View analytics are counted in memory and written in batches every few
	// seconds instead of two writes per public request.
	srv.EnableViewBatching()
	go srv.RunViewStats(rootCtx)

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           withWriteDeadline(srv.Routes(), writeIdleTimeout),
		ReadHeaderTimeout: 10 * time.Second,
		// A request body must arrive within this window; the upload handlers push
		// the deadline out per request (see api.extendUploadDeadline). Without any
		// read timeout, trickling a body one byte at a time held a connection —
		// and its handler — open indefinitely, which MaxBytesReader does not stop
		// because it bounds size, not time.
		ReadTimeout: 30 * time.Second,
		// No fixed WriteTimeout: it would cap the whole response and cut off
		// large downloads. withWriteDeadline bounds each write instead.
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	// Periodically purge expired sessions, old audit events and
	// expired/over-retention files.
	go dbJanitor(rootCtx, st, func() int { return set.Current().AuditRetentionDaysInt() })
	// Retention + trash-retention are read live from runtime settings on every
	// sweep so a change in the admin dashboard takes effect without a restart.
	janitorDone := make(chan struct{})
	go func() {
		defer close(janitorDone)
		fileJanitor(rootCtx, st, blobs,
			func() int { return set.Current().RetentionDaysInt() },
			func() int { return set.Current().TrashRetentionDaysInt() },
			filepath.Join(cfg.StorageFileRoot, ".tus"))
	}()

	// ListenAndServe returns ErrServerClosed the moment Shutdown *starts*, not
	// when in-flight requests have finished. Returning right then would run the
	// deferred teardown (job queues, DB pools) underneath uploads that are still
	// being written. shutdownDone closes only once Shutdown has drained them (or
	// given up after the grace period).
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-rootCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		// The requests are drained now; write the views they recorded before
		// the deferred teardown closes the database pool. Own deadline: a slow
		// drain may have used up the shared grace period.
		flushCtx, cancelFlush := context.WithTimeout(context.Background(), 10*time.Second)
		srv.FlushViews(flushCtx)
		cancelFlush()
		// A janitor pass in progress gets the rest of the same grace period. A
		// purge cut off between deleting rows and deleting their blobs left
		// those blobs for a later orphan scan to find, at best.
		select {
		case <-janitorDone:
		case <-shutdownCtx.Done():
		}
	}()

	fmt.Printf("uploader listening on %s\n", cfg.HTTPAddr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-shutdownDone
	return nil
}

// poolSize narrows an env-provided connection count to the int32 pgxpool wants.
// A plain conversion would wrap a nonsense value (UPLOADER_DB_MAX_CONNS being
// 2^31) into a negative one, which pgxpool then rejects at boot or, worse,
// silently treats as "use the default". Out-of-range input clamps to 0, which
// PoolOptions.withDefaults reads as "unset".
func poolSize(n int) int32 {
	if n <= 0 || n > math.MaxInt32 {
		return 0
	}
	return int32(n)
}

// dbJanitor does the hourly row housekeeping: expired sessions, view stats
// left behind for purged files, and audit events older than the
// audit_retention_days setting (read live, so a dashboard edit applies on the
// next run; 0 keeps them forever).
func dbJanitor(ctx context.Context, st *store.Store, auditRetentionDays func() int) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = st.DeleteExpiredSessions(ctx)
			_, _ = st.DeleteOrphanViewStats(ctx)
			if days := auditRetentionDays(); days > 0 {
				_, _ = st.DeleteAuditEventsOlderThan(ctx, time.Now().AddDate(0, 0, -days))
			}
		}
	}
}

// tusStaleAfter is how long a resumable upload may sit without receiving data
// before its scratch files are swept.
const tusStaleAfter = 24 * time.Hour

// sweepStaleTus removes abandoned resumable uploads from the tus scratch dir.
//
// An upload is two files, <id>.json (metadata, written once at creation) and
// <id>.bin (the data, appended to by every PATCH). Staleness is judged per
// upload by its most recent activity — the newest mtime of its files — not per
// file: judging the .json alone deleted it 24h after creation even while the
// .bin was still receiving data, which orphaned the data and failed the
// upload.
func sweepStaleTus(tusDir string, now time.Time) {
	entries, err := os.ReadDir(tusDir)
	if err != nil {
		return
	}
	uploadID := func(name string) string { return strings.TrimSuffix(name, filepath.Ext(name)) }

	lastActive := make(map[string]time.Time, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		id := uploadID(e.Name())
		if t, ok := lastActive[id]; !ok || info.ModTime().After(t) {
			lastActive[id] = info.ModTime()
		}
	}
	cutoff := now.Add(-tusStaleAfter)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if t, ok := lastActive[uploadID(e.Name())]; ok && t.Before(cutoff) {
			_ = os.Remove(filepath.Join(tusDir, e.Name()))
		}
	}
}

// staleTempAfter is how old an uploader temp file in os.TempDir() must be
// before the janitor treats it as left behind by a crash or kill. Every live
// user of such a file (spooling, EXIF strip, thumbnailing, a transcode bounded
// by media's 30-minute timeout) touches it far more recently than this.
const staleTempAfter = 6 * time.Hour

// sweepStaleTemp removes this app's own leftover temp files and HLS scratch
// directories from dir (os.TempDir()). In the container /tmp is a persistent
// volume, so whatever an OOM kill or SIGKILL leaves behind would otherwise
// accumulate forever. Only names this binary creates ("uploader-*", see
// upload.Service and the media package) are touched.
func sweepStaleTemp(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := now.Add(-staleTempAfter)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "uploader-") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		path := filepath.Join(dir, name)
		switch {
		case e.IsDir() && strings.HasPrefix(name, "uploader-hls-"):
			_ = os.RemoveAll(path)
		case e.Type().IsRegular():
			_ = os.Remove(path)
		}
	}
}

// stagingSweeper is implemented by storage backends that stage writes inside
// their own tree (storage.FileStorage).
type stagingSweeper interface {
	SweepStaging(cutoff time.Time) int
}

// fileJanitor purges expired/over-retention files (+ their blobs), auto-purges
// the trash after the retention window, and sweeps abandoned tus uploads,
// staging files and leftover temp files every 15 minutes. The orphan-blob scan
// runs on boot and then every orphanScanEvery.
func fileJanitor(ctx context.Context, st *store.Store, blobs storage.Storage, retentionDays, trashRetentionDays func() int, tusDir string) {
	purge := func() {
		// Not cancelled by shutdown: rows and their blobs go together, and
		// runServe waits for a running pass. A DELETE cancelled after it
		// committed would also lose the list of blobs to remove.
		ctx := context.WithoutCancel(ctx)
		deleted, _ := st.DeleteExpiredFiles(ctx)
		// Global retention: permanently delete files older than the configured
		// window (from storage + DB). Read live so dashboard edits apply at once.
		if days := retentionDays(); days > 0 {
			cutoff := time.Now().AddDate(0, 0, -days)
			if old, err2 := st.DeleteFilesOlderThan(ctx, cutoff); err2 == nil {
				deleted = append(deleted, old...)
			}
		}
		// Auto-purge soft-deleted files once they've been in the trash longer than
		// the trash-retention window (0 = keep in trash forever).
		if days := trashRetentionDays(); days > 0 {
			cutoff := time.Now().AddDate(0, 0, -days)
			if old, err2 := st.PurgeTrashedOlderThan(ctx, cutoff); err2 == nil {
				deleted = append(deleted, old...)
			}
		}
		for _, f := range deleted {
			upload.PurgeBlobs(ctx, st, blobs, f)
		}
	}

	var lastOrphanScan time.Time
	sweep := func() {
		purge()
		if ctx.Err() != nil {
			return
		}
		if now := time.Now(); now.Sub(lastOrphanScan) >= orphanScanEvery {
			lastOrphanScan = now
			scanOrphans(ctx, st, blobs, now)
		}
		sweepStaleTus(tusDir, time.Now())
		sweepStaleTemp(os.TempDir(), time.Now())
		// Same age as the /tmp leftovers: a live Put touches its staging file
		// with every write.
		if s, ok := blobs.(stagingSweeper); ok {
			s.SweepStaging(time.Now().Add(-staleTempAfter))
		}
	}
	sweep() // run once on boot
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}

func runMigrate(args []string) error {
	direction := "up"
	if len(args) > 0 {
		direction = args[0]
	}
	dbURL := os.Getenv("UPLOADER_DATABASE_URL")
	if dbURL == "" {
		return errors.New("UPLOADER_DATABASE_URL is required")
	}
	ctx := context.Background()
	if err := db.Migrate(ctx, dbURL, direction); err != nil {
		return err
	}
	fmt.Printf("migrate %s: done\n", direction)
	return nil
}

func runHealthcheck() error {
	addr := os.Getenv("UPLOADER_HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	host := addr
	if len(addr) > 0 && addr[0] == ':' {
		host = "127.0.0.1" + addr
	}
	// /ready, not /health: /health answers whenever the process is up, so a
	// container whose database was gone or whose data dir was not writable
	// still reported healthy. /ready is rate-limited per client IP; a 429 still
	// proves the server is answering, so it falls back to the liveness probe
	// rather than letting a flood against that bucket mark the container
	// unhealthy.
	base := "http://" + host + "/api/v1"
	status, err := probeStatus(base + "/ready")
	if err == nil && status == http.StatusTooManyRequests {
		status, err = probeStatus(base + "/health")
	}
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("unhealthy: status %d", status)
	}
	return nil
}

// probeStatus GETs url and returns the response status. The timeout stays
// below the image's 5 s HEALTHCHECK timeout so a hung probe reports an error
// instead of being killed.
func probeStatus(url string) (int, error) {
	client := &http.Client{Timeout: 4 * time.Second}
	// #nosec G704 -- url is built from UPLOADER_HTTP_ADDR (operator env, and
	// ":8080" by default), never from request data. This is the container
	// healthcheck probing its own listener.
	resp, err := client.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}
