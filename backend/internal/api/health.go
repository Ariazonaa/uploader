package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"uploader/internal/db/store"
	"uploader/internal/settings"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReady is a deeper readiness probe: it verifies the database connection
// and that the storage backend is writable. Returns 503 if anything fails.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	res := map[string]any{"db": "ok", "storage": "ok"}
	ready := true

	if err := s.store.Ping(r.Context()); err != nil {
		res["db"] = "error"
		ready = false
	}
	// Write+read+delete a tiny probe blob to confirm storage is usable.
	const probeKey = ".healthcheck"
	if _, err := s.storage.Put(r.Context(), probeKey, bytes.NewReader([]byte("ok"))); err != nil {
		res["storage"] = "error"
		ready = false
	} else {
		_ = s.storage.Delete(r.Context(), probeKey)
	}

	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, res)
}

// handlePublicSettings exposes the non-sensitive settings the SPA needs before
// login (app name, branding).
func (s *Server) handlePublicSettings(w http.ResponseWriter, r *http.Request) {
	set := s.settings.Current()
	// needs_setup drives the first-run wizard: true only while no account exists.
	needsSetup := false
	if n, err := s.store.CountUsers(r.Context()); err == nil && n == 0 {
		needsSetup = true
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"app_name":             set.AppName,
		"max_upload_bytes":     s.maxUploadBytes(),
		"registration_enabled": s.cfg.InviteRegistration(),
		"needs_setup":          needsSetup,
		"app_logo_url":         set.AppLogoURL,
		"login_heading":        set.LoginHeading,
		"login_subtext":        set.LoginSubtext,
		"login_bg_url":         set.LoginBgURL,
		"legal_notice":         set.LegalNotice,
		"push_enabled":         s.cfg.PushEnabled(),
		"vapid_public_key":     s.cfg.VAPIDPublicKey,
	})
}

// handleGetSettings returns the full editable settings (authenticated).
func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.settings.Current())
}

// maxSettingsBody bounds a settings PATCH. The SPA always sends the whole form,
// legal notice included, and a 16 KiB cap made every save fail once an
// Impressum grew past it.
const maxSettingsBody = 256 << 10

// handlePatchSettings updates editable settings (authenticated).
func (s *Server) handlePatchSettings(w http.ResponseWriter, r *http.Request) {
	var patch map[string]string
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSettingsBody)).Decode(&patch); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	changed, err := s.settings.Update(r.Context(), patch)
	if errors.Is(err, settings.ErrInvalid) {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not update settings")
		return
	}
	// Record which settings changed by key name only — never the values, which
	// may be sensitive (URLs, branding, legal text). A save that changed
	// nothing is not an event.
	if len(changed) > 0 {
		s.audit(r, store.AuditSettingsUpdate, "", map[string]any{"keys": changed})
	}
	writeJSON(w, http.StatusOK, s.settings.Current())
}
