package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	neturl "net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"uploader/internal/blobcrypt"
	"uploader/internal/db/store"
	"uploader/internal/upload"
)

// errorPageTmpl renders a friendly HTML error page with the matching http.cat
// image instead of the stock plain-text "404 page not found".
var errorPageTmpl = template.Must(template.New("err").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Code}} · {{.Title}}</title>
<style>
:root{color-scheme:dark}
body{margin:0;min-height:100vh;display:flex;flex-direction:column;align-items:center;justify-content:center;
gap:1.25rem;background:#000;color:#e7e7ea;font-family:system-ui,-apple-system,"Segoe UI",sans-serif;
text-align:center;padding:1.5rem}
img{max-width:min(90vw,460px);width:100%;border-radius:16px}
h1{margin:0;font-size:1.5rem}p{margin:.25rem 0;color:#9b9ba6}
a{color:#9aa4ff;text-decoration:none}a:hover{text-decoration:underline}
</style></head>
<body>
<img src="https://http.cat/{{.Code}}" alt="{{.Code}} {{.Title}}" loading="lazy">
<div><h1>{{.Code}} · {{.Title}}</h1><p>{{.Message}}</p></div>
<a href="/">← Back home</a>
</body></html>`))

// writeErrorPage renders the friendly error page at the given status.
func writeErrorPage(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = errorPageTmpl.Execute(w, map[string]any{
		"Code":    status,
		"Title":   http.StatusText(status),
		"Message": message,
	})
}

// slugFromParam strips an optional file extension from a raw-URL slug. Raw URLs
// may end in the real extension (e.g. /raw/aB3kdq.mp4) so chat clients
// auto-embed the media; slugs themselves never contain '.', so any dot marks the
// start of the extension.
func slugFromParam(param string) string {
	if i := strings.LastIndexByte(param, '.'); i > 0 {
		return param[:i]
	}
	return param
}

// handleRaw serves the original blob with Range support and defensive headers.
func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	f, err := s.store.GetFileBySlug(r.Context(), slugFromParam(chi.URLParam(r, "slug")))
	if err != nil {
		writeErrorPage(w, http.StatusNotFound, "This file does not exist or was removed.")
		return
	}

	rc, err := s.storage.Open(r.Context(), f.StorageKey)
	if err != nil {
		writeErrorPage(w, http.StatusNotFound, "This file does not exist or was removed.")
		return
	}
	defer rc.Close()
	stat, err := s.storage.Stat(r.Context(), f.StorageKey)
	if err != nil {
		writeErrorPage(w, http.StatusNotFound, "This file does not exist or was removed.")
		return
	}

	// content is the plaintext stream ServeContent serves. For encrypted blobs we
	// wrap the on-disk ciphertext in a seekable decrypting reader so Range/206
	// requests still work — against PLAINTEXT offsets, sized by f.SizeBytes.
	var content io.ReadSeeker = rc
	if f.Encrypted {
		dr, derr := blobcrypt.NewReader(rc, s.encKey(), f.SizeBytes)
		if derr == nil {
			derr = verifyFirstChunk(dr)
		}
		if derr != nil {
			log.Printf("raw: decrypt %s: %v", f.Slug, derr)
			writeErrorPage(w, http.StatusInternalServerError, "This file could not be decrypted.")
			return
		}
		content = dr
		singleRangeOnly(r)
	}

	// Never let the browser sniff or inline-render potentially active content.
	w.Header().Set("Content-Type", f.MimeType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	allowCrossOriginEmbed(w)
	// A week of caching with revalidation: long enough to be CDN/Discord friendly,
	// short enough that a purged-then-reused slug self-heals.
	if f.ContentHash != nil && *f.ContentHash != "" {
		// Strong ETag derived from the content hash → conditional requests
		// (If-None-Match) get a cheap 304; http.ServeContent does the comparison.
		// It is an HMAC, not the bare hash — see Server.etagFor.
		w.Header().Set("ETag", `"`+s.etagFor(*f.ContentHash)+`"`)
		w.Header().Set("Cache-Control", "public, max-age=604800, must-revalidate")
	} else {
		// Encrypted blobs carry no content hash; fall back to a weak validator
		// (size+modtime) so conditional requests can still 304, like handleThumb.
		w.Header().Set("ETag", fmt.Sprintf(`W/"r-%x-%x"`, stat.Size, stat.ModTime.Unix()))
		w.Header().Set("Cache-Control", "public, max-age=604800, must-revalidate")
	}
	disposition := "inline"
	if isDangerousInline(f.MimeType) {
		disposition = "attachment"
	}
	w.Header().Set("Content-Disposition", disposition+"; filename=\""+sanitizeFilename(f.OriginalName)+"\"")

	// A view is a GET that starts sending the file: a 200, or a 206 for a range
	// from byte 0. Players fetch the rest in further ranges and revalidate
	// with conditional requests; counting those (206 from an offset, 304) made
	// one playback of a video worth dozens of views.
	out := w
	if r.Method == http.MethodGet {
		fromStart := rangeFromStart(r.Header.Get("Range"))
		ref, ua := refererHost(r), uaCategory(r)
		out = &statusHook{ResponseWriter: w, onStatus: func(code int) {
			if code == http.StatusOK || (code == http.StatusPartialContent && fromStart) {
				s.submitView(f.Slug, ref, ua, true)
			}
		}}
	}
	http.ServeContent(out, r, f.OriginalName, stat.ModTime, content)
}

// rangeFromStart reports whether a Range header's first range begins at byte 0.
func rangeFromStart(h string) bool {
	spec, ok := strings.CutPrefix(h, "bytes=")
	if !ok {
		return false
	}
	first, _, _ := strings.Cut(spec, ",")
	return strings.HasPrefix(strings.TrimSpace(first), "0-")
}

// statusHook calls onStatus once, with the status the wrapped handler commits
// to. ReadFrom is passed through so http.ServeContent keeps the underlying
// writer's sendfile path.
type statusHook struct {
	http.ResponseWriter
	onStatus func(code int)
	fired    bool
}

func (h *statusHook) WriteHeader(code int) {
	if !h.fired {
		h.fired = true
		h.onStatus(code)
	}
	h.ResponseWriter.WriteHeader(code)
}

func (h *statusHook) Write(p []byte) (int, error) {
	if !h.fired {
		h.WriteHeader(http.StatusOK)
	}
	return h.ResponseWriter.Write(p)
}

func (h *statusHook) ReadFrom(src io.Reader) (int64, error) {
	if !h.fired {
		h.WriteHeader(http.StatusOK)
	}
	if rf, ok := h.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(h.ResponseWriter, src)
}

func (h *statusHook) Unwrap() http.ResponseWriter { return h.ResponseWriter }

// verifyFirstChunk authenticates the first chunk of an encrypted blob before
// anything is sent. blobcrypt.NewReader only parses the header, so a wrong key
// or a damaged blob used to fail on the first Read inside ServeContent, after
// the 200 and a week of cache headers had gone out: the client got a cut-off
// body that a cache may keep. Reading one byte decrypts chunk 0, which the
// reader keeps cached, and the seek rewinds for ServeContent. Later chunks are
// still checked only as they are read, but every chunk uses the same key, so
// the likely failure (a changed ENCRYPTION_KEY) is caught here.
func verifyFirstChunk(r io.ReadSeeker) error {
	var b [1]byte
	if _, err := r.Read(b[:]); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	_, err := r.Seek(0, io.SeekStart)
	return err
}

// singleRangeOnly drops a Range header that asks for more than one range, so
// the response is the whole file instead of multipart/byteranges (RFC 9110
// lets a server ignore Range).
//
// It is applied to encrypted blobs. Every range of a multipart response seeks
// the decrypting reader, and a seek into another chunk authenticates and
// decrypts a whole 64 KiB chunk, so a request listing many one-byte ranges in
// alternating chunks cost a full chunk decryption per range: a 1 MiB header
// took 2.8 s of CPU and 17.7 GiB of allocations, anonymously, over /raw.
// Players and download managers ask for one range at a time.
func singleRangeOnly(r *http.Request) {
	if strings.Contains(r.Header.Get("Range"), ",") {
		r.Header.Del("Range")
	}
}

// allowCrossOriginEmbed relaxes the global Cross-Origin-Resource-Policy for the
// public blob routes. The default "same-origin" is right for the API and the
// SPA, but these routes exist precisely so other sites can embed the media —
// with it, a cross-origin <img>/<video> pointing at /raw or /thumb is blocked by
// the browser. Framing and script execution stay locked down by the other
// headers (X-Frame-Options, nosniff, attachment for active types).
func allowCrossOriginEmbed(w http.ResponseWriter) {
	w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
}

// hlsName allows only the playlist and segment files we generate.
var hlsName = regexp.MustCompile(`^(index\.m3u8|seg\d{3,}\.ts)$`)

// handleHLS serves a file's HLS playlist + segments (when transcoded).
func (s *Server) handleHLS(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	name := chi.URLParam(r, "file")
	if !hlsName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	f, err := s.store.GetFileBySlug(r.Context(), slug)
	if err != nil || !f.HasHLS {
		http.NotFound(w, r)
		return
	}
	version := f.ID.String()
	isPlaylist := strings.HasSuffix(name, ".m3u8")
	v := r.URL.Query().Get("v")
	if !isPlaylist && v != "" && v != version {
		// A segment of an earlier file under this slug.
		http.NotFound(w, r)
		return
	}
	key := upload.HLSPrefix(f.Slug, f.StorageKey) + "/" + name
	rc, err := s.storage.Open(r.Context(), key)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer rc.Close()
	stat, err := s.storage.Stat(r.Context(), key)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	allowCrossOriginEmbed(w)
	if isPlaylist {
		servePlaylist(w, r, rc, stat.Size, stat.ModTime, version)
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	if v == version {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		// Unversioned: only a playlist from before segment URIs carried the
		// version asks for this, so it gets no long-lived cache entry.
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, name, stat.ModTime, rc)
}

// maxPlaylistBytes bounds the playlist read into memory. Renditions are capped
// at about 48 minutes of 6-second segments, a playlist of roughly 20 KB.
const maxPlaylistBytes = 1 << 20

// servePlaylist serves an HLS playlist with every segment URI pinned to the
// file's version (its id). Segments are cached for a year as immutable, but
// slugs are reused once a file is purged: under bare segment names, the next
// file's playlist made browsers and CDNs play cached segments of the old one.
//
// The response carries an ETag derived from the version and no Last-Modified.
// Playlists cached before this rewrite only hold a Last-Modified, and the
// stored playlist's mtime has not changed, so If-Modified-Since would keep
// handing them a 304 and their unversioned segment URIs.
func servePlaylist(w http.ResponseWriter, r *http.Request, rc io.Reader, size int64, modTime time.Time, version string) {
	body, err := io.ReadAll(io.LimitReader(rc, maxPlaylistBytes+1))
	if err != nil || len(body) > maxPlaylistBytes {
		http.Error(w, "playlist unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", fmt.Sprintf(`W/"h-%s-%x-%x"`, version, size, modTime.Unix()))
	http.ServeContent(w, r, "index.m3u8", time.Time{}, bytes.NewReader(versionSegmentURIs(body, version)))
}

// versionSegmentURIs appends v=version to each URI line of an HLS playlist:
// every line that is neither blank nor a tag/comment (starting with '#').
func versionSegmentURIs(playlist []byte, version string) []byte {
	var out bytes.Buffer
	for _, line := range bytes.SplitAfter(playlist, []byte("\n")) {
		uri := bytes.TrimRight(line, "\r\n")
		if len(bytes.TrimSpace(uri)) == 0 || uri[0] == '#' {
			out.Write(line)
			continue
		}
		sep := "?"
		if bytes.IndexByte(uri, '?') >= 0 {
			sep = "&"
		}
		out.Write(uri)
		out.WriteString(sep + "v=" + neturl.QueryEscape(version))
		out.Write(line[len(uri):])
	}
	return out.Bytes()
}

// handleThumb serves the generated JPEG thumbnail, if any.
func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	f, err := s.store.GetFileBySlug(r.Context(), chi.URLParam(r, "slug"))
	if err != nil || f.ThumbKey == nil {
		http.NotFound(w, r)
		return
	}
	rc, err := s.storage.Open(r.Context(), *f.ThumbKey)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer rc.Close()
	stat, err := s.storage.Stat(r.Context(), *f.ThumbKey)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	allowCrossOriginEmbed(w)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	// Weak ETag from size+modtime → conditional requests get a 304.
	w.Header().Set("ETag", fmt.Sprintf(`W/"t-%x-%x"`, stat.Size, stat.ModTime.Unix()))
	http.ServeContent(w, r, f.Slug+".jpg", stat.ModTime, rc)
}

// playerPageTmpl is a minimal, frameable media player page used as the
// twitter:player target so Twitter/X (and clients that honor it) can embed an
// inline video/audio player. It is intentionally tiny and self-contained.
var playerPageTmpl = template.Must(template.New("player").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Name}}</title>
<style>html,body{margin:0;height:100%;background:#000}
video,audio{width:100%;height:100%;object-fit:contain;display:block}</style>
</head><body>
{{if .IsVideo}}<video controls autoplay playsinline {{if .Poster}}poster="{{.Poster}}"{{end}}>
{{if .HLSURL}}<source src="{{.HLSURL}}" type="application/vnd.apple.mpegurl">{{end}}
<source src="{{.RawURL}}" type="{{.MimeType}}"></video>
{{else}}<audio controls autoplay src="{{.RawURL}}"></audio>{{end}}
</body></html>`))

// handlePlayer serves the frameable media player (twitter:player target).
func (s *Server) handlePlayer(w http.ResponseWriter, r *http.Request) {
	f, err := s.store.GetFileBySlug(r.Context(), slugFromParam(chi.URLParam(r, "slug")))
	if err != nil {
		writeErrorPage(w, http.StatusNotFound, "This file does not exist or was removed.")
		return
	}
	isVideo := strings.HasPrefix(f.MimeType, "video/")
	isAudio := strings.HasPrefix(f.MimeType, "audio/")
	if !isVideo && !isAudio {
		writeErrorPage(w, http.StatusNotFound, "This file has no inline player.")
		return
	}
	base := s.publicBaseURL(r)
	data := map[string]any{
		"Name":     f.OriginalName,
		"MimeType": f.MimeType,
		"RawURL":   rawURLFor(base, f),
		"IsVideo":  isVideo,
	}
	if f.ThumbKey != nil {
		data["Poster"] = base + "/thumb/" + f.Slug
	}
	if f.HasHLS {
		data["HLSURL"] = base + "/hls/" + f.Slug + "/index.m3u8"
	}
	// This page is meant to be framed by external clients (twitter:player), so
	// relax the global anti-framing headers for this response only. Only
	// frame-ancestors changes: replacing the whole policy here once dropped
	// base-uri and object-src 'none'.
	w.Header().Del("X-Frame-Options")
	w.Header().Set("Content-Security-Policy",
		withFrameAncestors(w.Header().Get("Content-Security-Policy"), "https:"))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = playerPageTmpl.Execute(w, data)
}

// withFrameAncestors returns the CSP with its frame-ancestors directive set to
// sources and every other directive left as it is.
func withFrameAncestors(csp, sources string) string {
	var out []string
	for _, d := range strings.Split(csp, ";") {
		d = strings.TrimSpace(d)
		name, _, _ := strings.Cut(d, " ")
		if d == "" || strings.EqualFold(name, "frame-ancestors") {
			continue
		}
		out = append(out, d)
	}
	return strings.Join(append(out, "frame-ancestors "+sources), "; ")
}

var deleteConfirmTmpl = template.Must(template.New("del").Parse(`<!doctype html>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Delete file</title>
<style>body{font-family:system-ui;background:#000;color:#e7e7ea;display:flex;min-height:100vh;
align-items:center;justify-content:center;text-align:center}
button{font:inherit;background:#dc2626;color:#fff;border:0;border-radius:.6rem;padding:.6rem 1.2rem;cursor:pointer}
.hint{color:#9b9ba6;font-size:.9rem}</style>
<div>
<p>Delete <strong>{{.Name}}</strong>?</p>
<p class="hint">It moves to its owner's trash and can be restored from there until it is purged.</p>
<form method="POST" action="{{.Action}}"><button type="submit">Delete file</button></form>
</div>`))

// handleDeleteConfirm (GET /d/{key}) shows a confirmation page. The actual delete
// happens via POST, so a link prefetch / <img> / crawler cannot delete the file.
func (s *Server) handleDeleteConfirm(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	f, err := s.store.GetFileByDeleteKey(r.Context(), key)
	if err != nil {
		writeErrorPage(w, http.StatusNotFound, "This deletion link is invalid or the file is already gone.")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = deleteConfirmTmpl.Execute(w, map[string]string{
		"Name":   f.OriginalName,
		"Action": "/d/" + key,
	})
}

// handleDeleteByKey (POST /d/{key}) deletes a file via its secret delete key
// (the ShareX DeletionURL target after confirmation).
func (s *Server) handleDeleteByKey(w http.ResponseWriter, r *http.Request) {
	f, err := s.store.GetFileByDeleteKey(r.Context(), chi.URLParam(r, "key"))
	if err != nil {
		writeErrorPage(w, http.StatusNotFound, "This deletion link is invalid or the file is already gone.")
		return
	}
	// Soft-delete: the file moves to the owner's trash (recoverable) rather than
	// being purged immediately, so an accidental deletion-link click is reversible.
	if _, err := s.store.DeleteFile(r.Context(), f.ID, f.UserID); err != nil {
		writeErrorPage(w, http.StatusInternalServerError, "Could not delete the file. Please try again.")
		return
	}
	// Anyone holding the key may do this, so there is no signed-in actor; the
	// owner is noted so the event can be matched to the account.
	s.auditAnon(r, store.AuditFileDeleteByKey, "delete link", f.Slug,
		map[string]any{"owner": f.UserID.String()})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8">` +
		`<body style="font-family:system-ui;background:#000;color:#e7e7ea;display:flex;min-height:100vh;align-items:center;justify-content:center">` +
		`File moved to its owner's trash.</body>`))
}

// submitView records the analytics for a view: always the aggregated (referrer
// host, UA category) breakdown, and the public views counter when
// incrementCount is set. Normally it only bumps an in-memory counter that the
// view batcher writes every few seconds; a crawler hammering /raw costs no
// goroutine and no database write per request. Without a batcher (tests) the
// view is written inline.
func (s *Server) submitView(slug, referrerHost, uaCategory string, incrementCount bool) {
	if s.views != nil {
		s.views.add(slug, referrerHost, uaCategory, incrementCount)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d := store.ViewDelta{Slug: slug, ReferrerHost: referrerHost, UACategory: uaCategory, Count: 1}
	if incrementCount {
		d.Views = 1
	}
	_ = s.store.RecordViews(ctx, []store.ViewDelta{d})
}

// refererHost extracts just the host from the Referer header (no path/query, so
// no accidental PII) for the view analytics. It returns "" when there is no
// Referer (recorded as "(direct)") and store.ReferrerOther for anything that is
// not a plausible hostname. The header is entirely client-controlled on public,
// unauthenticated routes and every distinct value used to become a permanent
// database row, so it is normalised (lowercase, no trailing dot) and must be a
// syntactically valid DNS name or IPv4 address of at most 253 characters.
func refererHost(r *http.Request) string {
	ref := r.Header.Get("Referer")
	if ref == "" {
		return ""
	}
	u, err := neturl.Parse(ref)
	if err != nil {
		return store.ReferrerOther
	}
	return normalizeReferrerHost(u.Hostname())
}

// normalizeReferrerHost lowercases h, strips a trailing dot, and returns it if
// it is a valid hostname (IPv4 literals pass the same check; IPv6 literals and
// internationalised names in Unicode form do not), else store.ReferrerOther.
func normalizeReferrerHost(h string) string {
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	if !validHostname(h) {
		return store.ReferrerOther
	}
	return h
}

// uaCategory buckets the User-Agent into a coarse, non-identifying category.
func uaCategory(r *http.Request) string {
	ua := strings.ToLower(r.UserAgent())
	switch {
	case ua == "":
		return "other"
	case strings.Contains(ua, "discord"):
		return "discord"
	case strings.Contains(ua, "telegram"):
		return "telegram"
	case strings.Contains(ua, "slack"):
		return "slack"
	case strings.Contains(ua, "twitter") || strings.Contains(ua, "bot") || strings.Contains(ua, "crawler") || strings.Contains(ua, "spider"):
		return "bot"
	case strings.Contains(ua, "mobile") || strings.Contains(ua, "android") || strings.Contains(ua, "iphone"):
		return "mobile"
	default:
		return "desktop"
	}
}

// isDangerousInline reports whether a MIME type could execute script if rendered
// inline by a browser (stored-XSS vector), so it must be served as attachment.
func isDangerousInline(mime string) bool {
	m := strings.ToLower(strings.TrimSpace(mime))
	if i := strings.IndexByte(m, ';'); i >= 0 {
		m = strings.TrimSpace(m[:i])
	}
	switch m {
	case "text/html", "application/xhtml+xml", "image/svg+xml",
		"application/xml", "text/xml", "application/javascript",
		"text/javascript", "application/x-shockwave-flash":
		return true
	}
	return false
}

// sanitizeFilename strips characters that could break the Content-Disposition
// header or enable header/path injection.
func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\"", "_")
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "file"
	}
	return out
}
