// Package web embeds the built single-page frontend and serves it with SPA
// fallback (unknown non-API paths return index.html so client routing works).
//
// The dist/ directory holds a placeholder during local backend development; the
// Docker build replaces it with the real Vite output before compiling.
package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var distFS embed.FS

// immutableCache is sent for Vite's build output under assets/: every name
// there carries a content hash, so a changed file always gets a new URL.
const immutableCache = "public, max-age=31536000, immutable"

// Handler returns an http.Handler serving the embedded SPA.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	return handler(sub)
}

func handler(sub fs.FS) http.Handler {
	etags := hashFiles(sub)
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqPath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		// Only regular files are in etags, so a directory (GET /assets/) falls
		// through to the SPA shell instead of getting FileServer's listing.
		if tag, ok := etags[reqPath]; ok && reqPath != "index.html" {
			// embed.FS reports a zero ModTime, so without an ETag nothing could
			// be revalidated and every request re-sent the full file.
			w.Header().Set("ETag", tag)
			if strings.HasPrefix(reqPath, "assets/") {
				w.Header().Set("Cache-Control", immutableCache)
			} else {
				// Unhashed public files (sw.js, ...) keep their URL across
				// builds and must be revalidated.
				w.Header().Set("Cache-Control", "no-cache")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		serveIndex(w, r, sub, etags["index.html"])
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, sub fs.FS, etag string) {
	b, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "frontend not built", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Always revalidate the app shell so a rebuilt frontend (new asset hashes)
	// is picked up immediately instead of a stale cached index.html. The ETag
	// turns that revalidation into a 304 when nothing changed.
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	w.Header().Set("ETag", etag)
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(b))
}

// hashFiles returns a strong ETag (content hash) for every regular file in
// fsys, keyed by its slash-separated path.
func hashFiles(fsys fs.FS) map[string]string {
	tags := map[string]string{}
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(b)
		tags[p] = `"` + hex.EncodeToString(sum[:16]) + `"`
		return nil
	})
	return tags
}
