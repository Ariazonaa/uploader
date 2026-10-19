package api

import (
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// sharePageTmpl renders a minimal share page carrying OpenGraph/Twitter meta so
// links unfurl nicely in Discord/Slack/Twitter. All dynamic values are escaped
// by html/template.
var sharePageTmpl = template.Must(template.New("share").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<meta name="theme-color" content="{{.Color}}">
<meta property="og:title" content="{{.Title}}">
{{if .SiteName}}<meta property="og:site_name" content="{{.SiteName}}">{{end}}
{{if .Description}}<meta property="og:description" content="{{.Description}}">
<meta name="description" content="{{.Description}}">{{end}}
<meta property="og:url" content="{{.PageURL}}">
<meta property="og:locale" content="en_US">
<meta property="article:published_time" content="{{.Published}}">
<meta property="og:updated_time" content="{{.Published}}">
<link rel="alternate" type="application/json+oembed" href="{{.OEmbedURL}}">
{{if .IsImage}}<meta property="og:type" content="website">
<meta property="og:image" content="{{.RawURL}}">
<meta property="og:image:secure_url" content="{{.RawURL}}">
<meta property="og:image:type" content="{{.MimeType}}">
{{if .Width}}<meta property="og:image:width" content="{{.Width}}">{{end}}
{{if .Height}}<meta property="og:image:height" content="{{.Height}}">{{end}}
<meta property="og:image:alt" content="{{.Name}}">
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:image" content="{{.RawURL}}">
<meta name="twitter:image:alt" content="{{.Name}}">
{{else if .IsVideo}}<meta property="og:type" content="video.other">
<meta property="og:video" content="{{.RawURL}}">
<meta property="og:video:secure_url" content="{{.RawURL}}">
<meta property="og:video:type" content="{{.MimeType}}">
{{if .Width}}<meta property="og:video:width" content="{{.Width}}">{{end}}
{{if .Height}}<meta property="og:video:height" content="{{.Height}}">{{end}}
{{if .Duration}}<meta property="og:video:duration" content="{{.Duration}}">{{end}}
{{if .ThumbURL}}<meta property="og:image" content="{{.ThumbURL}}">{{end}}
<meta name="twitter:card" content="player">
<meta name="twitter:player" content="{{.PlayerURL}}">
<meta name="twitter:player:width" content="{{.PlayerWidth}}">
<meta name="twitter:player:height" content="{{.PlayerHeight}}">
<meta name="twitter:player:stream" content="{{.RawURL}}">
<meta name="twitter:player:stream:content_type" content="{{.MimeType}}">
{{if .ThumbURL}}<meta name="twitter:image" content="{{.ThumbURL}}">{{end}}
{{else if .IsAudio}}<meta property="og:type" content="music.song">
<meta property="og:audio" content="{{.RawURL}}">
<meta property="og:audio:secure_url" content="{{.RawURL}}">
<meta property="og:audio:type" content="{{.MimeType}}">
{{if .Duration}}<meta property="music:duration" content="{{.Duration}}">{{end}}
{{if .ThumbURL}}<meta property="og:image" content="{{.ThumbURL}}">{{end}}
<meta name="twitter:card" content="player">
<meta name="twitter:player" content="{{.PlayerURL}}">
<meta name="twitter:player:width" content="{{.PlayerWidth}}">
<meta name="twitter:player:height" content="{{.PlayerHeight}}">
<meta name="twitter:player:stream" content="{{.RawURL}}">
<meta name="twitter:player:stream:content_type" content="{{.MimeType}}">
{{else}}<meta property="og:type" content="website">
<meta name="twitter:card" content="summary">{{end}}
<style>
:root{color-scheme:dark light}
body{margin:0;min-height:100vh;display:flex;flex-direction:column;align-items:center;justify-content:center;
gap:1rem;background:#000;color:#e7e7ea;font-family:system-ui,sans-serif}
img,video{max-width:92vw;max-height:82vh;border-radius:12px}
a{color:#9aa4ff}.meta{font-size:.85rem;opacity:.7}
.actions{display:flex;gap:.5rem;flex-wrap:wrap;justify-content:center}
.btn{border:1px solid #1f1f1f;background:#000;color:#e7e7ea;
font-size:.85rem;padding:.45rem .9rem;border-radius:.6rem;text-decoration:none}
.btn:hover{background:#0d0d0d;border-color:#2a2a2a}
.powered{position:fixed;bottom:.6rem;right:.8rem;font-size:.7rem;opacity:.4}
audio{width:min(92vw,520px)}
</style>
</head>
<body>
{{if .IsImage}}<img src="{{.RawURL}}" alt="{{.Name}}">
{{else if .IsVideo}}<video controls>
{{if .HLSURL}}<source src="{{.HLSURL}}" type="application/vnd.apple.mpegurl">{{end}}
<source src="{{.RawURL}}" type="{{.MimeType}}">
</video>
{{else if .IsAudio}}<audio src="{{.RawURL}}" controls></audio>
{{else}}<p>{{.Name}}</p>{{end}}
<div class="meta">{{.Name}} · {{.SizeText}}{{if .Dimensions}} · {{.Dimensions}}{{end}} · {{.Views}} views</div>
<div class="actions">
<a class="btn" href="{{.RawURL}}" download>Download</a>
<a class="btn" href="{{.RawURL}}" target="_blank" rel="noreferrer">Open raw</a>
</div>
{{if not .HidePoweredBy}}<footer class="powered">powered by {{.AppName}}</footer>{{end}}
</body>
</html>`))

type sharePageData struct {
	Title         string
	Description   string
	SiteName      string
	Color         string
	Name          string
	MimeType      string
	PageURL       string
	RawURL        string
	ThumbURL      string
	OEmbedURL     string
	HLSURL        string
	PlayerURL     string
	SizeText      string
	Dimensions    string
	AppName       string
	Published     string
	Duration      string
	HidePoweredBy bool
	Views         int64
	IsImage       bool
	IsVideo       bool
	IsAudio       bool
	Width         int
	Height        int
	PlayerWidth   int
	PlayerHeight  int
}

func (s *Server) handleSharePage(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	f, err := s.store.GetFileBySlug(r.Context(), slug)
	if err != nil {
		writeErrorPage(w, http.StatusNotFound, "This file does not exist or was removed.")
		return
	}
	base := s.publicBaseURL(r)
	set := s.settings.Current()

	// Expand {placeholder} variables in the embed templates against this file.
	siteName := firstNonEmpty(set.EmbedSiteName, set.AppName)
	rep := s.embedReplacer(r.Context(), f, set.EmbedTitle, set.EmbedDescription, siteName)
	title := s.embedTitle(rep, set.EmbedTitle, f.OriginalName)

	data := sharePageData{
		Title:         title,
		Description:   s.renderEmbed(rep, set.EmbedDescription),
		SiteName:      s.renderEmbed(rep, siteName),
		Color:         embedColorFor(set.EmbedColor, f.DominantColor),
		Name:          f.OriginalName,
		MimeType:      f.MimeType,
		PageURL:       base + "/s/" + f.Slug,
		RawURL:        rawURLFor(base, f),
		OEmbedURL:     base + "/oembed?slug=" + url.QueryEscape(f.Slug),
		SizeText:      humanBytes(f.SizeBytes),
		AppName:       set.AppName,
		Published:     f.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		HidePoweredBy: set.HidePoweredBy == "true",
		Views:         f.Views,
		IsImage:       strings.HasPrefix(f.MimeType, "image/") && !isDangerousInline(f.MimeType),
		IsVideo:       strings.HasPrefix(f.MimeType, "video/"),
		IsAudio:       strings.HasPrefix(f.MimeType, "audio/"),
	}
	if f.Width != nil && f.Height != nil {
		data.Dimensions = fmt.Sprintf("%d×%d", *f.Width, *f.Height)
	}
	if f.ThumbKey != nil {
		data.ThumbURL = base + "/thumb/" + f.Slug
	}
	if f.HasHLS {
		data.HLSURL = base + "/hls/" + f.Slug + "/index.m3u8"
	}
	if f.Width != nil {
		data.Width = *f.Width
	}
	if f.Height != nil {
		data.Height = *f.Height
	}
	if f.DurationMs != nil && *f.DurationMs > 0 {
		data.Duration = strconv.Itoa(int((*f.DurationMs + 999) / 1000)) // whole seconds
	}
	if data.IsVideo || data.IsAudio {
		data.PlayerURL = base + "/p/" + f.Slug
		data.PlayerWidth, data.PlayerHeight = 1280, 720
		if f.Width != nil && f.Height != nil && *f.Width > 0 && *f.Height > 0 {
			data.PlayerWidth, data.PlayerHeight = *f.Width, *f.Height
		}
	}

	if r.Method == http.MethodGet {
		s.submitView(f.Slug, refererHost(r), uaCategory(r), false)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Be explicit rather than leaving caching to a CDN's heuristics: the page
	// embeds absolute URLs built from the request host when public_url is unset,
	// so a long-lived shared cache entry is worth avoiding.
	w.Header().Set("Cache-Control", "public, max-age=60")
	_ = sharePageTmpl.Execute(w, data)
}

// handlePublicFile returns public metadata for a slug as JSON.
func (s *Server) handlePublicFile(w http.ResponseWriter, r *http.Request) {
	f, err := s.store.GetFileBySlug(r.Context(), chi.URLParam(r, "slug"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, toFileView(f, s.publicBaseURL(r)))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
