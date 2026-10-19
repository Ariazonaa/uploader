package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"uploader/internal/config"
)

// Unknown API paths and methods must answer in JSON, not fall through to the
// SPA (which answered 200 with index.html for anything under /api/v1).
func TestAPIUnknownRoutesAreJSON(t *testing.T) {
	spa := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html>"))
	})
	h := (&Server{cfg: &config.Config{}, spa: spa}).Routes()

	cases := []struct {
		method, path string
		status       int
		allow        string
	}{
		{http.MethodGet, "/api/v1/does-not-exist", http.StatusNotFound, ""},
		{http.MethodPost, "/api/v1/does-not-exist", http.StatusNotFound, ""},
		{http.MethodGet, "/api/v1/auth/login", http.StatusMethodNotAllowed, "POST"},
		{http.MethodDelete, "/api/v1/health", http.StatusMethodNotAllowed, "GET"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.status {
			t.Errorf("%s %s: status %d, want %d", c.method, c.path, rec.Code, c.status)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("%s %s: Content-Type %q, want JSON", c.method, c.path, ct)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] == "" {
			t.Errorf("%s %s: body %q is not a JSON error", c.method, c.path, rec.Body)
		}
		if c.allow != "" && !strings.Contains(strings.Join(rec.Header().Values("Allow"), ","), c.allow) {
			t.Errorf("%s %s: Allow %v, want it to contain %s", c.method, c.path, rec.Header().Values("Allow"), c.allow)
		}
	}

	// Outside the API, unknown paths still reach the SPA (client-side routes).
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/files/anything", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<!doctype html>") {
		t.Fatalf("SPA fallback: status %d body %q", rec.Code, rec.Body)
	}
}

// State-changing browser requests from another origin are refused before any
// handler runs; same-origin and non-browser clients (no Origin, no
// Sec-Fetch-Site, e.g. ShareX or curl) are not affected.
func TestCrossOriginRequestsAreRefused(t *testing.T) {
	h := (&Server{cfg: &config.Config{}}).Routes()
	cases := []struct {
		name, method, path string
		headers            map[string]string
		status             int
		contentType        string
	}{
		{"cross-site fetch", http.MethodPost, "/api/v1/nope", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden, "application/json"},
		{"same-site sibling", http.MethodPost, "/api/v1/nope", map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden, "application/json"},
		{"foreign Origin", http.MethodPost, "/api/v1/nope", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden, "application/json"},
		{"cross-site form on /d", http.MethodPost, "/d/somekey", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden, "text/html"},
		{"same origin", http.MethodPost, "/api/v1/nope", map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusNotFound, "application/json"},
		{"matching Origin", http.MethodPost, "/api/v1/nope", map[string]string{"Origin": "http://example.com"}, http.StatusNotFound, "application/json"},
		{"no browser headers", http.MethodPost, "/api/v1/nope", nil, http.StatusNotFound, "application/json"},
		{"cross-site GET", http.MethodGet, "/api/v1/nope", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusNotFound, "application/json"},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, "http://example.com"+c.path, nil)
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != c.status {
			t.Errorf("%s: status %d, want %d", c.name, rec.Code, c.status)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, c.contentType) {
			t.Errorf("%s: Content-Type %q, want %s", c.name, ct, c.contentType)
		}
	}
}
