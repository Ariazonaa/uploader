// Package settings exposes the runtime-editable configuration (app name, public
// URL, OpenGraph embed template). Defaults are hardcoded built-ins seeded into
// the database on first run; they are not env-configurable, and subsequent edits
// go through the admin API. The current snapshot is cached in an atomic pointer
// for lock-free reads.
package settings

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"uploader/internal/config"
	"uploader/internal/db/store"
)

// Keys for the settings table.
const (
	KeyAppName          = "app_name"
	KeyPublicURL        = "public_url"
	KeyEmbedSiteName    = "embed_site_name"
	KeyEmbedDescription = "embed_description"
	KeyEmbedColor       = "embed_color"
	KeyEmbedTitle       = "embed_title"
	KeyEmbedAuthorName  = "embed_author_name"
	KeyEmbedAuthorURL   = "embed_author_url"
	KeyAppLogoURL       = "app_logo_url"
	KeyLoginHeading     = "login_heading"
	KeyLoginSubtext     = "login_subtext"
	KeyLoginBgURL       = "login_bg_url"
	KeyHidePoweredBy    = "hide_powered_by"
	KeyLegalNotice      = "legal_notice"
	KeyBlockedMime      = "blocked_mime"
	KeyRetentionDays    = "retention_days"
	// Upload/behavior policy, previously env-only, now editable in the dashboard.
	KeyMaxUploadMiB       = "max_upload_mib"
	KeyStripEXIF          = "strip_exif"
	KeyTrashRetentionDays = "trash_retention_days"
	KeyEncryptDefault     = "encrypt_default"
	KeySlugStrategy       = "slug_strategy"
	KeyAuditRetentionDays = "audit_retention_days"
)

// Snapshot is an immutable view of the current settings.
type Snapshot struct {
	AppName          string `json:"app_name"`
	PublicURL        string `json:"public_url"`
	EmbedSiteName    string `json:"embed_site_name"`
	EmbedDescription string `json:"embed_description"`
	EmbedColor       string `json:"embed_color"`
	EmbedTitle       string `json:"embed_title"`
	EmbedAuthorName  string `json:"embed_author_name"`
	EmbedAuthorURL   string `json:"embed_author_url"`
	AppLogoURL       string `json:"app_logo_url"`
	LoginHeading     string `json:"login_heading"`
	LoginSubtext     string `json:"login_subtext"`
	LoginBgURL       string `json:"login_bg_url"`
	HidePoweredBy    string `json:"hide_powered_by"`
	LegalNotice      string `json:"legal_notice"`
	BlockedMime      string `json:"blocked_mime"`
	// RetentionDays is stored as a string like every other setting; use
	// RetentionDaysInt for the parsed value (0 = keep files forever).
	RetentionDays string `json:"retention_days"`

	// MaxUploadMiB is the per-file upload cap in MiB (0 = fall back to the
	// built-in/env default). Use MaxUploadBytes for the resolved byte value.
	MaxUploadMiB string `json:"max_upload_mib"`
	// StripEXIF is "true"/"false": strip image metadata by default on upload.
	StripEXIF string `json:"strip_exif"`
	// TrashRetentionDays is how long soft-deleted files stay recoverable before
	// the janitor purges them (0 = keep in trash forever).
	TrashRetentionDays string `json:"trash_retention_days"`
	// EncryptDefault is "true"/"false": store uploads AES-256-GCM encrypted at
	// rest by default (per-upload override via ?encrypt=true/false).
	EncryptDefault string `json:"encrypt_default"`
	// SlugStrategy names how auto-generated share slugs are built: "random"
	// (default), "gfycat" or "date". Use SlugStrategyValue for the canonical
	// value (garbage falls back to "random"). Per-upload override: ?slug_strategy=.
	SlugStrategy string `json:"slug_strategy"`
	// AuditRetentionDays is how long audit-log entries are kept before the
	// janitor deletes them (0 = keep forever).
	AuditRetentionDays string `json:"audit_retention_days"`
}

// Upper bounds for the numeric settings, enforced both when a value is written
// (Update) and when it is read (the accessors), so a row stored before the
// bound existed is covered too. Without them a huge value — an admin typing
// 999999999 to mean "forever" — overflowed downstream: the janitor's retention
// cutoff fell outside what pgx can encode as a timestamp and wrapped into the
// future, so every file counted as expired and was purged; and MiB<<20 wrapped
// the upload cap.
const (
	retentionDaysLimit = 36500   // ~100 years; also caps the trash and audit windows
	uploadMiBLimit     = 1 << 20 // 1 TiB
)

// RetentionDaysInt returns the global retention window in days. A missing,
// blank, negative or non-numeric value means "disabled" (0), so a garbled
// setting can never trigger unexpected deletions; anything above
// retentionDaysLimit is clamped to it.
func (s *Snapshot) RetentionDaysInt() int {
	return boundedInt(s.RetentionDays, retentionDaysLimit)
}

// MaxUploadBytes resolves the per-file upload cap in bytes. A missing, zero or
// invalid setting yields 0, letting the caller fall back to its own default;
// anything above uploadMiBLimit is clamped to it.
func (s *Snapshot) MaxUploadBytes() int64 {
	if m := boundedInt(s.MaxUploadMiB, uploadMiBLimit); m > 0 {
		return int64(m) << 20
	}
	return 0
}

// StripEXIFBool reports whether image metadata should be stripped by default.
// Anything but an explicit "false" keeps stripping on (safer default).
func (s *Snapshot) StripEXIFBool() bool {
	b, err := strconv.ParseBool(strings.TrimSpace(s.StripEXIF))
	if err != nil {
		return true
	}
	return b
}

// TrashRetentionDaysInt returns the trash auto-purge window in days (0 =
// never auto-purge; garbage is treated as 0 so it can't delete unexpectedly;
// anything above retentionDaysLimit is clamped to it).
func (s *Snapshot) TrashRetentionDaysInt() int {
	return boundedInt(s.TrashRetentionDays, retentionDaysLimit)
}

// AuditRetentionDaysInt returns the audit-log retention window in days (0 =
// keep forever; garbage is treated as 0 so it can't delete unexpectedly;
// anything above retentionDaysLimit is clamped to it).
func (s *Snapshot) AuditRetentionDaysInt() int {
	return boundedInt(s.AuditRetentionDays, retentionDaysLimit)
}

// EncryptDefaultBool reports whether uploads should be encrypted at rest by
// default. Anything but an explicit "true" is off (safe default: no surprise
// encryption if the setting is garbled).
func (s *Snapshot) EncryptDefaultBool() bool {
	b, err := strconv.ParseBool(strings.TrimSpace(s.EncryptDefault))
	if err != nil {
		return false
	}
	return b
}

// SlugStrategyValue returns the canonical auto-slug strategy. Only the exact
// stored values "gfycat" and "date" are honored; anything else (blank, garbage,
// legacy) falls back to "random", so slug generation always has a valid strategy.
func (s *Snapshot) SlugStrategyValue() string {
	switch strings.TrimSpace(s.SlugStrategy) {
	case "gfycat":
		return "gfycat"
	case "date":
		return "date"
	default:
		return "random"
	}
}

// boundedInt parses a stored setting into an int in [0, limit]. Blank,
// negative or non-numeric input returns 0; anything above limit — including a
// digit string too long for an int — returns limit.
func boundedInt(v string, limit int) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	switch {
	case errors.Is(err, strconv.ErrRange) && n > 0:
		return limit
	case err != nil || n < 0:
		return 0
	case n > limit:
		return limit
	}
	return n
}

// RefreshInterval is how often every instance re-reads the settings table.
// Update refreshes the instance that handled the PATCH immediately; the others
// (horizontal scaling, one shared database) pick the change up within this
// window instead of running on stale settings until they restart.
const RefreshInterval = 30 * time.Second

// refreshTimeout bounds a single background re-read so a hung database cannot
// pin the refresh goroutine (and s.mu, which Update also needs) indefinitely.
const refreshTimeout = 10 * time.Second

// Service loads, caches and updates runtime settings.
type Service struct {
	// fetch reads every stored setting; New wires it to store.GetAllSettings.
	// save writes a batch of settings atomically (store.SetSettings). Both are
	// fields so the refresh loop and Update can be tested without Postgres.
	fetch func(context.Context) (map[string]string, error)
	save  func(context.Context, map[string]string) error
	mu    sync.Mutex // serializes writes and reloads (see refreshLoop)
	cur   atomic.Pointer[Snapshot]
}

// New seeds defaults (without overwriting existing values), loads the current
// snapshot into the cache and starts a background goroutine that re-reads the
// settings every RefreshInterval, so edits made through another instance show
// up here too. That goroutine lives exactly as long as ctx: cancel it (serve
// passes its signal-bound root context) to stop the refresh.
func New(ctx context.Context, st *store.Store, cfg *config.Config) (*Service, error) {
	s := &Service{fetch: st.GetAllSettings, save: st.SetSettings}

	// Every runtime setting is seeded exactly once from a hardcoded built-in
	// default (SetSettingIfAbsent) so an existing dashboard value is never
	// overwritten. These settings are intentionally NOT env-configurable — after
	// the first boot they are editable only in the admin dashboard.
	seeds := []struct{ key, val string }{
		{KeyAppName, cfg.DefaultAppName},
		{KeyPublicURL, cfg.DefaultPublicURL},
		{KeyEmbedSiteName, cfg.DefaultEmbedSiteName},
		{KeyEmbedDescription, cfg.DefaultEmbedDescription},
		{KeyEmbedColor, cfg.DefaultEmbedColor},
		{KeyEmbedTitle, cfg.DefaultEmbedTitle},
		{KeyEmbedAuthorName, cfg.DefaultEmbedAuthorName},
		{KeyEmbedAuthorURL, cfg.DefaultEmbedAuthorURL},
		{KeyAppLogoURL, cfg.DefaultAppLogoURL},
		{KeyLoginHeading, cfg.DefaultLoginHeading},
		{KeyLoginSubtext, cfg.DefaultLoginSubtext},
		{KeyLoginBgURL, cfg.DefaultLoginBgURL},
		{KeyHidePoweredBy, cfg.DefaultHidePoweredBy},
		{KeyLegalNotice, cfg.DefaultLegalNotice},
		{KeyBlockedMime, cfg.DefaultBlockedMime},
		{KeyRetentionDays, strconv.Itoa(cfg.RetentionDays)},
		{KeyMaxUploadMiB, strconv.FormatInt(cfg.DirectUploadLimit>>20, 10)},
		{KeyStripEXIF, strconv.FormatBool(cfg.StripEXIF)},
		{KeyTrashRetentionDays, strconv.Itoa(cfg.TrashRetentionDays)},
		{KeyEncryptDefault, strconv.FormatBool(cfg.EncryptDefault)},
		{KeySlugStrategy, cfg.DefaultSlugStrategy},
		{KeyAuditRetentionDays, strconv.Itoa(cfg.AuditRetentionDays)},
	}
	for _, sd := range seeds {
		if err := st.SetSettingIfAbsent(ctx, sd.key, sd.val); err != nil {
			return nil, err
		}
	}
	if err := s.reload(ctx); err != nil {
		return nil, err
	}
	go s.refreshLoop(ctx, RefreshInterval)
	return s, nil
}

// refreshLoop reloads the snapshot every interval until ctx is cancelled. A
// failed reload keeps the previous snapshot (stale beats empty) and is retried
// on the next tick.
func (s *Service) refreshLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.refresh(ctx); err != nil && ctx.Err() == nil {
				log.Printf("settings: periodic reload: %v", err)
			}
		}
	}
}

// refresh re-reads the settings under s.mu. Holding the write lock matters: a
// reload that read the table just before a concurrent Update committed would
// otherwise store its older snapshot after Update's fresh one and undo the
// edit locally until the next tick.
func (s *Service) refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reload(ctx)
}

// NewStatic returns a Service pinned to a fixed snapshot and backed by no
// database. It exists so tests can exercise settings-dependent code paths
// without a Postgres instance. Update/reload would nil-panic on it, so
// production code must always go through New. It runs no refresh loop.
func NewStatic(snap Snapshot) *Service {
	s := &Service{}
	s.cur.Store(&snap)
	return s
}

func (s *Service) reload(ctx context.Context) error {
	m, err := s.fetch(ctx)
	if err != nil {
		return err
	}
	// hide_powered_by is made canonical on read as well: rows saved before
	// Update normalised it may hold "1" or "TRUE", which consumers comparing
	// with "true" (and the dashboard checkbox) would read as off.
	s.cur.Store(&Snapshot{
		AppName:            m[KeyAppName],
		PublicURL:          m[KeyPublicURL],
		EmbedSiteName:      m[KeyEmbedSiteName],
		EmbedDescription:   m[KeyEmbedDescription],
		EmbedColor:         m[KeyEmbedColor],
		EmbedTitle:         m[KeyEmbedTitle],
		EmbedAuthorName:    m[KeyEmbedAuthorName],
		EmbedAuthorURL:     m[KeyEmbedAuthorURL],
		AppLogoURL:         m[KeyAppLogoURL],
		LoginHeading:       m[KeyLoginHeading],
		LoginSubtext:       m[KeyLoginSubtext],
		LoginBgURL:         m[KeyLoginBgURL],
		HidePoweredBy:      normalize(KeyHidePoweredBy, m[KeyHidePoweredBy]),
		LegalNotice:        m[KeyLegalNotice],
		BlockedMime:        m[KeyBlockedMime],
		RetentionDays:      m[KeyRetentionDays],
		MaxUploadMiB:       m[KeyMaxUploadMiB],
		StripEXIF:          m[KeyStripEXIF],
		TrashRetentionDays: m[KeyTrashRetentionDays],
		EncryptDefault:     m[KeyEncryptDefault],
		SlugStrategy:       m[KeySlugStrategy],
		AuditRetentionDays: m[KeyAuditRetentionDays],
	})
	return nil
}

// Current returns the cached settings snapshot.
func (s *Service) Current() *Snapshot {
	return s.cur.Load()
}

// Update writes the provided keys in one transaction and refreshes the cache.
// Only known keys are accepted; unknown keys are ignored. It returns the keys
// whose stored value actually changed, sorted: the settings page submits the
// whole form on every save, so the submitted keys say nothing about what the
// admin edited. Unchanged values are not rewritten.
func (s *Service) Update(ctx context.Context, patch map[string]string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored, err := s.fetch(ctx)
	if err != nil {
		return nil, err
	}

	allowed := map[string]bool{
		KeyAppName: true, KeyPublicURL: true, KeyEmbedSiteName: true,
		KeyEmbedDescription: true, KeyEmbedColor: true, KeyEmbedTitle: true,
		KeyEmbedAuthorName: true, KeyEmbedAuthorURL: true,
		KeyAppLogoURL: true, KeyLoginHeading: true, KeyLoginSubtext: true, KeyLoginBgURL: true,
		KeyHidePoweredBy: true, KeyLegalNotice: true, KeyBlockedMime: true,
		KeyRetentionDays: true, KeyMaxUploadMiB: true, KeyStripEXIF: true,
		KeyTrashRetentionDays: true, KeyEncryptDefault: true, KeySlugStrategy: true,
		KeyAuditRetentionDays: true,
	}
	write := make(map[string]string, len(patch))
	// Sorted, so that of several invalid values the same one is reported.
	for _, k := range slices.Sorted(maps.Keys(patch)) {
		if !allowed[k] {
			continue
		}
		v := normalize(k, patch[k])
		if err := validate(k, v); err != nil {
			return nil, err
		}
		if old, ok := stored[k]; ok && old == v {
			continue
		}
		write[k] = v
	}
	if len(write) > 0 {
		if err := s.save(ctx, write); err != nil {
			return nil, err
		}
	}
	if err := s.reload(ctx); err != nil {
		return nil, err
	}
	return slices.Sorted(maps.Keys(write)), nil
}

// normalize brings the typed settings into a canonical stored form so
// consumers (janitor, upload handler) never have to defend against garbage
// input. Free-text settings pass through unchanged apart from NUL bytes and
// invalid UTF-8, which a Postgres TEXT column rejects: one such value used to
// fail the whole save.
func normalize(k, v string) string {
	v = strings.ReplaceAll(strings.ToValidUTF8(v, "\uFFFD"), "\x00", "")
	switch k {
	case KeyRetentionDays, KeyTrashRetentionDays, KeyAuditRetentionDays:
		return strconv.Itoa(boundedInt(v, retentionDaysLimit))
	case KeyMaxUploadMiB:
		return strconv.Itoa(boundedInt(v, uploadMiBLimit))
	case KeyStripEXIF:
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			b = true // malformed → keep the privacy-safer default (strip on)
		}
		return strconv.FormatBool(b) // canonical "true"/"false"
	case KeyEncryptDefault, KeyHidePoweredBy:
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			b = false // malformed input keeps encryption off / the badge shown
		}
		return strconv.FormatBool(b) // canonical bool string
	case KeyPublicURL, KeyEmbedAuthorURL, KeyAppLogoURL, KeyLoginBgURL:
		return strings.TrimSpace(v)
	case KeySlugStrategy:
		v = strings.ToLower(strings.TrimSpace(v))
		if v != "gfycat" && v != "date" {
			v = "random" // unknown/blank → the default random strategy
		}
		return v
	}
	return v
}

// ErrInvalid marks a value Update refuses to store. The error text names the
// key and what is expected, and is meant to be shown to the admin.
var ErrInvalid = errors.New("invalid setting")

// embedVariable matches a {placeholder} the embed templates expand per file.
var embedVariable = regexp.MustCompile(`\{[^{}]*\}`)

// validate rejects URL settings that would break what is built from them. A
// public_url without a scheme was stored as typed and turned every generated
// share/raw link into a relative path. Empty always means "not set".
func validate(k, v string) error {
	if v == "" {
		return nil
	}
	switch k {
	case KeyPublicURL:
		if u, err := url.Parse(v); err != nil || !absHTTP(u) || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return fmt.Errorf("%w: %s must be an absolute http(s) URL without query or fragment, such as https://i.example.com, or empty", ErrInvalid, k)
		}
	case KeyEmbedAuthorURL:
		// May contain embed variables ({uploader}, ...), even in the host.
		if u, err := url.Parse(embedVariable.ReplaceAllString(v, "x")); err != nil || !absHTTP(u) {
			return fmt.Errorf("%w: %s must be an absolute http(s) URL or empty", ErrInvalid, k)
		}
	case KeyAppLogoURL, KeyLoginBgURL:
		// The SPA also resolves a path against its own origin, e.g. an image
		// uploaded to this instance (/raw/...).
		u, err := url.Parse(v)
		rootPath := strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "//")
		if err != nil || !(absHTTP(u) || rootPath) {
			return fmt.Errorf("%w: %s must be an absolute http(s) URL, a path starting with /, or empty", ErrInvalid, k)
		}
	}
	return nil
}

func absHTTP(u *url.URL) bool {
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
