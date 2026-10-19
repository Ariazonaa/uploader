package api

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"uploader/internal/db/store"
	"uploader/internal/upload"
)

// maxEmbedNameRunes caps the file name (and extension) substituted into embed
// templates. The name is chosen by the uploader, and an operator template may
// repeat {filename}, so an uncapped name would make every unfurl of the file
// as large as the name times the number of placeholders.
const maxEmbedNameRunes = 255

// embedReplacer builds a strings.Replacer that expands the {placeholder}
// variables usable in embed templates (title/description/site name/author).
// Random placeholders are resolved here, so a fresh replacer per request yields
// the "random embed per load" behavior.
//
// templates are the templates the caller is about to render. {uploader} is the
// only placeholder that costs a query, and /s and /oembed are public and hot,
// so the owner is looked up only when one of them actually uses it.
func (s *Server) embedReplacer(ctx context.Context, f store.File, templates ...string) *strings.Replacer {
	uploader := ""
	if usesPlaceholder(templates, "{uploader}") {
		if u, err := s.store.GetUserByID(ctx, f.UserID); err == nil {
			uploader = u.Username
		}
	}
	return newEmbedReplacer(f, uploader)
}

// usesPlaceholder reports whether any of templates contains placeholder.
func usesPlaceholder(templates []string, placeholder string) bool {
	for _, t := range templates {
		if strings.Contains(t, placeholder) {
			return true
		}
	}
	return false
}

// newEmbedReplacer is embedReplacer without the uploader lookup.
func newEmbedReplacer(f store.File, uploader string) *strings.Replacer {
	name := truncate(f.OriginalName, maxEmbedNameRunes)
	return strings.NewReplacer(
		"{filename}", name,
		"{originalname}", name,
		"{slug}", f.Slug,
		// The same extension the raw URL and the storage key carry, not the raw
		// path.Ext of a client-chosen name ("photo.JPG?x" or a 200-character
		// suffix), and falling back to the sniffed type when the name has none.
		"{ext}", strings.TrimPrefix(upload.PreferredExt(f.OriginalName, f.MimeType), "."),
		"{mimetype}", f.MimeType,
		"{size}", humanBytes(f.SizeBytes),
		"{size.bytes}", strconv.FormatInt(f.SizeBytes, 10),
		"{width}", intPtrStr(f.Width),
		"{height}", intPtrStr(f.Height),
		"{duration}", durationMsStr(f.DurationMs),
		"{uploader}", uploader,
		"{date}", f.CreatedAt.Format("2006-01-02"),
		"{date.relative}", relativeTime(f.CreatedAt),
		"{time}", f.CreatedAt.Format("15:04"),
		"{views}", strconv.FormatInt(f.Views, 10),
		"{random.emoji}", randomFrom(randomEmojis),
		"{random.word}", randomFrom(randomWords),
	)
}

// renderEmbed expands placeholders in tmpl. The replacer (and the uploader
// lookup, if any template needed it) already exists by now; the guard only
// skips the Replace pass for plain strings without any "{".
func (s *Server) renderEmbed(rep *strings.Replacer, tmpl string) string {
	if tmpl == "" || !strings.ContainsRune(tmpl, '{') {
		return tmpl
	}
	return rep.Replace(tmpl)
}

// embedTitle renders the operator's title template, or returns the file name
// verbatim when no template is set. The name is uploader-controlled and must
// never be expanded as a template itself: strings.Replacer substitutes every
// {filename} in it with the whole name, so a name made of repeated
// placeholders grows quadratically, and a single anonymous GET /oembed for
// such a file could exhaust the container's memory.
func (s *Server) embedTitle(rep *strings.Replacer, tmpl, name string) string {
	if title := s.renderEmbed(rep, tmpl); title != "" {
		return title
	}
	return name
}

// handleOEmbed serves the oEmbed document referenced from the share page. Discord
// uses author_name/author_url from here to render the small clickable line above
// the embed title. Keyed by slug so the author fields can use per-file variables.
func (s *Server) handleOEmbed(w http.ResponseWriter, r *http.Request) {
	set := s.settings.Current()
	base := s.publicBaseURL(r)
	resp := map[string]any{
		"version":       "1.0",
		"type":          "link",
		"provider_name": set.AppName,
		"provider_url":  base,
	}
	if slug := r.URL.Query().Get("slug"); slug != "" {
		if f, err := s.store.GetFileBySlug(r.Context(), slug); err == nil {
			rep := s.embedReplacer(r.Context(), f,
				set.EmbedAuthorName, set.EmbedAuthorURL, set.EmbedTitle, set.EmbedDescription)
			if an := s.renderEmbed(rep, set.EmbedAuthorName); an != "" {
				resp["author_name"] = an
			}
			if au := s.renderEmbed(rep, set.EmbedAuthorURL); au != "" {
				resp["author_url"] = au
			}
			// Richer fields for consumers that render full oEmbed cards
			// (Mastodon/Slack/Telegram/WhatsApp): title, thumbnail, dimensions and
			// a "photo"/"video" type when the media URL is directly embeddable.
			if title := s.embedTitle(rep, set.EmbedTitle, f.OriginalName); title != "" {
				resp["title"] = title
			}
			if desc := s.renderEmbed(rep, set.EmbedDescription); desc != "" {
				resp["description"] = desc
			}
			rawURL := rawURLFor(base, f)
			switch {
			// oEmbed requires width+height for the "photo" type, so only promote
			// to photo once dimensions are known (media processing is async);
			// otherwise leave the default "link" type, which has no such requirement.
			case strings.HasPrefix(f.MimeType, "image/") && !isDangerousInline(f.MimeType) &&
				f.Width != nil && f.Height != nil:
				resp["type"] = "photo"
				resp["url"] = rawURL
				resp["width"] = *f.Width
				resp["height"] = *f.Height
			case strings.HasPrefix(f.MimeType, "video/") || strings.HasPrefix(f.MimeType, "audio/"):
				resp["type"] = "video"
				resp["html"] = fmt.Sprintf(
					`<iframe src=%q width="%d" height="%d" frameborder="0" allowfullscreen></iframe>`,
					base+"/p/"+f.Slug, 1280, 720)
				resp["width"] = 1280
				resp["height"] = 720
			}
			if f.ThumbKey != nil {
				resp["thumbnail_url"] = base + "/thumb/" + f.Slug
				if f.Width != nil {
					resp["thumbnail_width"] = *f.Width
				}
				if f.Height != nil {
					resp["thumbnail_height"] = *f.Height
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func intPtrStr(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

func durationMsStr(p *int64) string {
	if p == nil || *p <= 0 {
		return ""
	}
	d := time.Duration(*p) * time.Millisecond
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

var (
	randomEmojis = []string{"✨", "🚀", "🔥", "🎉", "📎", "🖼️", "🌈", "⚡", "💾", "👀", "🪄", "🛰️"}
	randomWords  = []string{"fresh", "shiny", "secret", "spicy", "cosmic", "turbo", "mystic", "crisp"}
)

func randomFrom(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	// #nosec G404 -- decorative embed variation ({random.emoji}/{random.word});
	// nothing here is a secret or a token.
	return xs[rand.Intn(len(xs))]
}

// resolveEmbedColor returns the configured embed color, or a fresh random color
// per load when the operator set it to "random" (or left it empty).
func resolveEmbedColor(configured string) string {
	c := strings.ToLower(strings.TrimSpace(configured))
	if c == "" || c == "random" {
		// #nosec G404 -- a cosmetic accent color, not a security value.
		return fmt.Sprintf("#%06x", rand.Intn(0x1000000))
	}
	return configured
}

// embedColorFor picks the share-page accent color: when the operator set the
// embed color to "auto", use the file's dominant color (if known); otherwise
// fall back to the configured/random color.
func embedColorFor(configured string, dominant *string) string {
	if strings.EqualFold(strings.TrimSpace(configured), "auto") && dominant != nil && *dominant != "" {
		return *dominant
	}
	return resolveEmbedColor(configured)
}
