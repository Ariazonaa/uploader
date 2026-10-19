//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"uploader/internal/config"
	"uploader/internal/db/store"
	"uploader/internal/settings"
)

func settingsServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	s, st := integrationServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	set, err := settings.New(ctx, st, &config.Config{DefaultAppName: "Uploader"})
	if err != nil {
		t.Fatal(err)
	}
	s.settings = set
	return s, st
}

func patchSettings(t *testing.T, s *Server, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.handlePatchSettings(rec, httptest.NewRequest(http.MethodPatch, "/api/v1/settings", bytes.NewReader(b)))
	return rec
}

// The SPA sends the whole form on every save, legal notice included, so a
// long Impressum must not push the body over the limit.
func TestPatchSettingsAcceptsLongLegalNotice(t *testing.T) {
	s, _ := settingsServer(t)
	notice := strings.Repeat("Impressum ", 4000) // 40 KB
	if rec := patchSettings(t, s, map[string]string{"app_name": "Mine", "legal_notice": notice}); rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s), want 200", rec.Code, rec.Body)
	}
	if got := s.settings.Current().LegalNotice; got != notice {
		t.Fatalf("legal notice not stored (%d bytes)", len(got))
	}
}

// The SPA submits every field on each save; the audit entry must name only the
// keys that changed, and a save that changed nothing records no event.
func TestPatchSettingsAuditsChangedKeysOnly(t *testing.T) {
	s, st := settingsServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	events := func() []store.AuditEvent {
		t.Helper()
		ev, _, err := st.ListAuditEvents(ctx, store.AuditFilter{Event: store.AuditSettingsUpdate, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return ev
	}
	// The form as the settings page holds it: every field of the snapshot.
	form := func() map[string]string {
		t.Helper()
		out := map[string]string{}
		cur, _ := json.Marshal(s.settings.Current())
		if err := json.Unmarshal(cur, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// The first save may still canonicalise freshly seeded values.
	if rec := patchSettings(t, s, form()); rec.Code != http.StatusOK {
		t.Fatalf("baseline save: status %d (%s)", rec.Code, rec.Body)
	}

	before := len(events())
	if rec := patchSettings(t, s, form()); rec.Code != http.StatusOK {
		t.Fatalf("unchanged form: status %d", rec.Code)
	}
	if n := len(events()); n != before {
		t.Fatalf("saving the unchanged form recorded %d event(s)", n-before)
	}
	edited := form()
	edited["app_name"] = "Renamed"
	if rec := patchSettings(t, s, edited); rec.Code != http.StatusOK {
		t.Fatalf("one edit: status %d", rec.Code)
	}
	ev := events()
	if len(ev) != before+1 {
		t.Fatalf("one edit recorded %d event(s), want 1", len(ev)-before)
	}
	var md struct{ Keys []string }
	if err := json.Unmarshal(ev[0].Metadata, &md); err != nil || len(md.Keys) != 1 || md.Keys[0] != "app_name" {
		t.Fatalf("audit metadata = %s (%v), want keys [app_name]", ev[0].Metadata, err)
	}
}

// A public URL without a scheme is refused with a message naming the field,
// and nothing else from that save is applied.
func TestPatchSettingsRejectsInvalidURL(t *testing.T) {
	s, _ := settingsServer(t)
	rec := patchSettings(t, s, map[string]string{"app_name": "Mine", "public_url": "i.example.com"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "public_url") {
		t.Fatalf("status %d (%s), want 400 naming public_url", rec.Code, rec.Body)
	}
	if got := s.settings.Current().AppName; got == "Mine" {
		t.Fatal("a rejected save still changed app_name")
	}
}

// A NUL byte (valid JSON, rejected by Postgres) is dropped instead of failing
// the save after some keys were already written.
func TestPatchSettingsStripsNULBytes(t *testing.T) {
	s, _ := settingsServer(t)
	rec := patchSettings(t, s, map[string]string{"app_name": "Mine", "login_heading": "Hi\x00there"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s), want 200", rec.Code, rec.Body)
	}
	if cur := s.settings.Current(); cur.AppName != "Mine" || cur.LoginHeading != "Hithere" {
		t.Fatalf("app_name/login_heading = %q/%q", cur.AppName, cur.LoginHeading)
	}
}
