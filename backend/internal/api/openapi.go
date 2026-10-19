package api

import (
	_ "embed"
	"net/http"
)

// openapiSpec is the hand-authored OpenAPI 3.0.3 description of the JSON API,
// embedded so it ships inside the single binary (no external file at runtime).
//
//go:embed openapi.yaml
var openapiSpec []byte

// handleOpenAPISpec serves the embedded OpenAPI document as YAML. It is
// registered inside the authenticated route group, so it returns 401 when logged
// out but is readable by any logged-in user (session cookie or API token) — which
// is exactly what the in-app Swagger UI page fetches.
func (s *Server) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	// Small, versioned document; allow revalidation so a rebuilt binary's updated
	// spec is picked up without serving a stale copy.
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(openapiSpec)
}
