package settings

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestRetentionDaysInt(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},       // unset → disabled
		{"0", 0},      // explicit off
		{"30", 30},    // normal value
		{"  15 ", 15}, // surrounding whitespace tolerated
		{"-5", 0},     // negatives can never trigger deletions
		{"abc", 0},    // garbage → disabled, not a panic
		{"3.5", 0},    // non-integer → disabled
		{"365", 365},  // large window
		{"00007", 7},  // leading zeros
		// Huge values clamp instead of overflowing the janitor's cutoff (which
		// would wrap into the future and purge every file).
		{"36500", retentionDaysLimit},
		{"36501", retentionDaysLimit},
		{"999999999", retentionDaysLimit},
		{"99999999999999999999999", retentionDaysLimit}, // beyond int64
		{"-99999999999999999999999", 0},
	}
	for _, c := range cases {
		s := &Snapshot{RetentionDays: c.in}
		if got := s.RetentionDaysInt(); got != c.want {
			t.Errorf("RetentionDaysInt(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestMaxUploadBytes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0},              // unset → caller falls back to its default
		{"0", 0},             // 0 → fall back
		{"100", 100 << 20},   // 100 MiB
		{"1024", 1024 << 20}, // 1 GiB
		{"-5", 0},            // negative → fall back
		{"abc", 0},           // garbage → fall back
		{" 50 ", 50 << 20},   // trimmed
		// Huge values clamp instead of wrapping MiB<<20 negative.
		{"1048576", 1 << 40},
		{"1048577", 1 << 40},
		{"9223372036854775807", 1 << 40},
		{"99999999999999999999999", 1 << 40},
	}
	for _, c := range cases {
		s := &Snapshot{MaxUploadMiB: c.in}
		if got := s.MaxUploadBytes(); got != c.want {
			t.Errorf("MaxUploadBytes(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestStripEXIFBool(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"true", true},
		{"false", false},
		{"1", true},
		{"0", false},
		{"", true},     // blank → safer default (strip on)
		{"nope", true}, // garbage → safer default
	}
	for _, c := range cases {
		s := &Snapshot{StripEXIF: c.in}
		if got := s.StripEXIFBool(); got != c.want {
			t.Errorf("StripEXIFBool(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestTrashRetentionDaysInt(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"30", 30},
		{"-1", 0},
		{"x", 0},
		{"36501", retentionDaysLimit},
		{"99999999999999999999999", retentionDaysLimit},
	}
	for _, c := range cases {
		s := &Snapshot{TrashRetentionDays: c.in}
		if got := s.TrashRetentionDaysInt(); got != c.want {
			t.Errorf("TrashRetentionDaysInt(%q) = %d, want %d", c.in, got, c.want)
		}
		a := &Snapshot{AuditRetentionDays: c.in}
		if got := a.AuditRetentionDaysInt(); got != c.want {
			t.Errorf("AuditRetentionDaysInt(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestNormalizeClampsNumericSettings: Update must store the clamped value, not
// just rely on the accessors, so the dashboard shows what is actually in effect.
func TestNormalizeClampsNumericSettings(t *testing.T) {
	cases := []struct {
		key, in, want string
	}{
		{KeyRetentionDays, "30", "30"},
		{KeyRetentionDays, " 999999999 ", "36500"},
		{KeyRetentionDays, "99999999999999999999999", "36500"},
		{KeyRetentionDays, "-5", "0"},
		{KeyRetentionDays, "forever", "0"},
		{KeyTrashRetentionDays, "36501", "36500"},
		{KeyTrashRetentionDays, "7", "7"},
		{KeyAuditRetentionDays, "365", "365"},
		{KeyAuditRetentionDays, "99999999999999999999999", "36500"},
		{KeyAuditRetentionDays, "-1", "0"},
		{KeyMaxUploadMiB, "100", "100"},
		{KeyMaxUploadMiB, "9223372036854775807", "1048576"},
		{KeyMaxUploadMiB, "-1", "0"},
	}
	for _, c := range cases {
		if got := normalize(c.key, c.in); got != c.want {
			t.Errorf("normalize(%s, %q) = %q, want %q", c.key, c.in, got, c.want)
		}
	}
}

func TestNormalizeHidePoweredBy(t *testing.T) {
	for in, want := range map[string]string{
		"true": "true", "TRUE": "true", "1": "true", " t ": "true",
		"false": "false", "0": "false", "": "false", "yes": "false",
	} {
		if got := normalize(KeyHidePoweredBy, in); got != want {
			t.Errorf("normalize(hide_powered_by, %q) = %q, want %q", in, got, want)
		}
	}
	// Legacy rows are read canonically too.
	m := newMemService(t, map[string]string{KeyHidePoweredBy: "1"})
	if got := m.Current().HidePoweredBy; got != "true" {
		t.Errorf("HidePoweredBy stored as \"1\" reads %q, want \"true\"", got)
	}
}

func TestValidateURLSettings(t *testing.T) {
	cases := []struct {
		key, val string
		ok       bool
	}{
		{KeyPublicURL, "", true},
		{KeyPublicURL, "https://i.example.com", true},
		{KeyPublicURL, "http://localhost:8080/", true},
		{KeyPublicURL, "https://example.com/up", true},
		{KeyPublicURL, "i.example.com", false}, // no scheme: every link turned relative
		{KeyPublicURL, "//i.example.com", false},
		{KeyPublicURL, "ftp://i.example.com", false},
		{KeyPublicURL, "javascript:alert(1)", false},
		{KeyPublicURL, "https://", false},
		{KeyPublicURL, "https://i.example.com/?x=1", false},
		{KeyPublicURL, "https://i.example.com/#top", false},
		{KeyEmbedAuthorURL, "https://example.com/u/{uploader}", true},
		{KeyEmbedAuthorURL, "https://{uploader}.example.com", true},
		{KeyEmbedAuthorURL, "{uploader}", false},
		{KeyEmbedAuthorURL, "example.com", false},
		{KeyAppLogoURL, "https://cdn.example.com/logo.png", true},
		{KeyAppLogoURL, "/raw/abc123.png", true},
		{KeyAppLogoURL, "logo.png", false},
		{KeyAppLogoURL, "//cdn.example.com/logo.png", false},
		{KeyLoginBgURL, "data:image/png;base64,AAAA", false},
		{KeyLoginBgURL, "/bg.jpg", true},
		{KeyAppName, "anything at all", true}, // not a URL setting
	}
	for _, c := range cases {
		err := validate(c.key, normalize(c.key, c.val))
		if (err == nil) != c.ok {
			t.Errorf("validate(%s, %q) = %v, want ok=%v", c.key, c.val, err, c.ok)
		}
		if err != nil && !errors.Is(err, ErrInvalid) {
			t.Errorf("validate(%s, %q) error %v does not wrap ErrInvalid", c.key, c.val, err)
		}
	}
}

// An invalid value rejects the whole save; nothing is written.
func TestUpdateRejectsInvalidURL(t *testing.T) {
	m := newMemService(t, map[string]string{KeyAppName: "old"})
	_, err := m.Update(context.Background(), map[string]string{KeyAppName: "new", KeyPublicURL: "i.example.com"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Update = %v, want ErrInvalid", err)
	}
	if m.saves != 0 || m.Current().AppName != "old" {
		t.Fatalf("invalid save still wrote (%d writes, app_name %q)", m.saves, m.Current().AppName)
	}
	// Surrounding whitespace is trimmed, not rejected.
	if _, err := m.Update(context.Background(), map[string]string{KeyPublicURL: " https://i.example.com "}); err != nil {
		t.Fatal(err)
	}
	if got := m.Current().PublicURL; got != "https://i.example.com" {
		t.Fatalf("PublicURL = %q", got)
	}
}

// TestRetentionCutoffStaysInPast guards the janitor's arithmetic: whatever is
// stored, the cutoff derived from the clamped value must lie in the past and
// within the range Postgres can store, so it can never select every file.
func TestRetentionCutoffStaysInPast(t *testing.T) {
	now := time.Now()
	for _, in := range []string{"999999999", "9223372036854775807", "99999999999999999999999"} {
		s := &Snapshot{RetentionDays: in, TrashRetentionDays: in, AuditRetentionDays: in}
		for _, days := range []int{s.RetentionDaysInt(), s.TrashRetentionDaysInt(), s.AuditRetentionDaysInt()} {
			cutoff := now.AddDate(0, 0, -days)
			if !cutoff.Before(now) || cutoff.Year() < 1900 {
				t.Errorf("%q: cutoff %v not a sane past timestamp", in, cutoff)
			}
		}
	}
}

// TestRefreshLoopPicksUpExternalEdits covers the multi-instance case: another
// instance writes the settings table, and this one must converge on the new
// value without an Update of its own — and stop polling once ctx is cancelled.
func TestRefreshLoopPicksUpExternalEdits(t *testing.T) {
	var mu sync.Mutex
	stored := map[string]string{KeyAppName: "before"}
	calls := 0
	s := &Service{fetch: func(context.Context) (map[string]string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		out := make(map[string]string, len(stored))
		for k, v := range stored {
			out[k] = v
		}
		return out, nil
	}}
	if err := s.reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := s.Current().AppName; got != "before" {
		t.Fatalf("initial AppName = %q, want %q", got, "before")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.refreshLoop(ctx, 5*time.Millisecond)
		close(done)
	}()

	// Simulate an edit made through a different app instance.
	mu.Lock()
	stored[KeyAppName] = "after"
	mu.Unlock()

	deadline := time.Now().Add(2 * time.Second)
	for s.Current().AppName != "after" {
		if time.Now().After(deadline) {
			t.Fatalf("snapshot never refreshed: AppName = %q", s.Current().AppName)
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("refreshLoop did not stop after ctx was cancelled")
	}
	mu.Lock()
	after := calls
	mu.Unlock()
	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if calls != after {
		t.Fatalf("fetch called %d more times after the loop stopped", calls-after)
	}
}

// memService is a Service over an in-memory settings table. saves counts the
// batch writes; failSave makes the next one fail without storing anything.
type memService struct {
	*Service
	stored   map[string]string
	saves    int
	failSave bool
}

func newMemService(t *testing.T, initial map[string]string) *memService {
	t.Helper()
	m := &memService{stored: map[string]string{}}
	for k, v := range initial {
		m.stored[k] = v
	}
	m.Service = &Service{
		fetch: func(context.Context) (map[string]string, error) {
			out := make(map[string]string, len(m.stored))
			for k, v := range m.stored {
				out[k] = v
			}
			return out, nil
		},
		save: func(_ context.Context, kv map[string]string) error {
			m.saves++
			if m.failSave {
				return errors.New("write failed")
			}
			for k, v := range kv {
				m.stored[k] = v
			}
			return nil
		},
	}
	if err := m.reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	return m
}

// Update writes the whole patch as one batch, normalised, and drops unknown
// keys and the bytes Postgres would reject.
func TestUpdateWritesOneCleanBatch(t *testing.T) {
	m := newMemService(t, map[string]string{KeyAppName: "old"})
	_, err := m.Update(context.Background(), map[string]string{
		KeyAppName:       "new\x00name",
		KeyLegalNotice:   "bad \xff byte",
		KeyRetentionDays: "999999999",
		"unknown_key":    "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.saves != 1 {
		t.Fatalf("%d batch writes, want 1", m.saves)
	}
	if _, ok := m.stored["unknown_key"]; ok {
		t.Error("unknown key was stored")
	}
	cur := m.Current()
	if cur.AppName != "newname" || cur.LegalNotice != "bad \uFFFD byte" || cur.RetentionDays != "36500" {
		t.Errorf("snapshot = %q / %q / %q", cur.AppName, cur.LegalNotice, cur.RetentionDays)
	}
}

// A failed write leaves the snapshot as it was.
func TestUpdateFailureKeepsSnapshot(t *testing.T) {
	m := newMemService(t, map[string]string{KeyAppName: "old"})
	m.failSave = true
	if _, err := m.Update(context.Background(), map[string]string{KeyAppName: "new"}); err == nil {
		t.Fatal("Update: want error, got nil")
	}
	if got := m.Current().AppName; got != "old" {
		t.Fatalf("AppName after failed update = %q, want %q", got, "old")
	}
}

// Update reports (and writes) only the keys whose stored value changes, judged
// after normalisation, so the audit log names what was actually edited.
func TestUpdateReportsOnlyChangedKeys(t *testing.T) {
	m := newMemService(t, map[string]string{
		KeyAppName: "Uploader", KeyRetentionDays: "30", KeyStripEXIF: "true", KeyLegalNotice: "x",
	})
	form := map[string]string{
		KeyAppName:       "Uploader",
		KeyRetentionDays: " 30 ", // same value once normalised
		KeyStripEXIF:     "1",    // ditto
		KeyLegalNotice:   "y",
		KeyLoginHeading:  "Welcome", // not stored yet
		"unknown_key":    "z",
	}
	changed, err := m.Update(context.Background(), form)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{KeyLegalNotice, KeyLoginHeading}; !slices.Equal(changed, want) {
		t.Fatalf("changed = %v, want %v", changed, want)
	}

	// Saving the same form again changes nothing and writes nothing.
	saves := m.saves
	changed, err = m.Update(context.Background(), form)
	if err != nil || len(changed) != 0 || m.saves != saves {
		t.Fatalf("resubmitted form: changed = %v, err = %v, %d new writes", changed, err, m.saves-saves)
	}
}

// TestRefreshKeepsSnapshotOnError: a failing reload must not blank the cache.
func TestRefreshKeepsSnapshotOnError(t *testing.T) {
	fail := false
	s := &Service{fetch: func(context.Context) (map[string]string, error) {
		if fail {
			return nil, errors.New("db down")
		}
		return map[string]string{KeyAppName: "kept"}, nil
	}}
	if err := s.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	fail = true
	if err := s.refresh(context.Background()); err == nil {
		t.Fatal("refresh: want error, got nil")
	}
	if got := s.Current().AppName; got != "kept" {
		t.Fatalf("AppName after failed refresh = %q, want %q", got, "kept")
	}
}
