package api

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"uploader/internal/auth"
	"uploader/internal/db/store"
)

// handleStats returns aggregate usage numbers for the current user (powers the
// dashboard stat widgets / system dashboard).
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	st, err := s.store.UserStats(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not load stats")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"file_count":  st.FileCount,
		"total_bytes": st.TotalBytes,
		"total_views": st.TotalViews,
	})
}

// handleViewStats returns aggregated, privacy-first traffic stats (admin only):
// top referrer hosts and view counts by user-agent category.
func (s *Server) handleViewStats(w http.ResponseWriter, r *http.Request) {
	referrers, err := s.store.TopReferrers(r.Context(), 20)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not load traffic stats")
		return
	}
	uas, err := s.store.TopUACategories(r.Context(), 20)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not load traffic stats")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"referrers": nameCounts(referrers),
		"agents":    nameCounts(uas),
	})
}

func nameCounts(in []store.NameCount) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, nc := range in {
		out = append(out, map[string]any{"name": nc.Name, "count": nc.Count})
	}
	return out
}

// statsExportPage is how many files the stats export reads per query.
const statsExportPage = 500

// handleStatsExport streams the user's file statistics as CSV or JSON. Files
// are read a page at a time (store.ListFilesAfter) and written as they come:
// loading them all first made the export's memory grow with the account, and
// keeping one result set open while a slow client reads would pin a pooled
// connection instead. Trashed files are left out, as in every other statistic.
func (s *Server) handleStatsExport(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	page, err := s.store.ListFilesAfter(r.Context(), u.ID, nil, statsExportPage)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not load files")
		return
	}
	base := s.publicBaseURL(r)
	// Per-user data like every API response (see writeJSON), but written
	// directly, so the header is set here.
	w.Header().Set("Cache-Control", "no-store")

	var row func(store.File)
	var end func()
	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="uploader-stats.json"`)
		// Element by element, the same bytes a json.Encoder with a two-space
		// indent writes for the whole []fileView.
		_, _ = io.WriteString(w, "[")
		n := 0
		row = func(f store.File) {
			b, _ := json.MarshalIndent(toFileView(f, base), "  ", "  ")
			if n > 0 {
				_, _ = io.WriteString(w, ",")
			}
			_, _ = io.WriteString(w, "\n  ")
			_, _ = w.Write(b)
			n++
		}
		end = func() {
			if n > 0 {
				_, _ = io.WriteString(w, "\n")
			}
			_, _ = io.WriteString(w, "]\n")
		}
	} else {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="uploader-stats.csv"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"slug", "name", "mime_type", "size_bytes", "views", "created_at", "url"})
		row = func(f store.File) {
			cells := []string{
				f.Slug, f.OriginalName, f.MimeType,
				strconv.FormatInt(f.SizeBytes, 10), strconv.FormatInt(f.Views, 10),
				f.CreatedAt.Format(time.RFC3339), base + "/s/" + f.Slug,
			}
			for i := range cells {
				cells[i] = csvCell(cells[i])
			}
			_ = cw.Write(cells)
		}
		end = cw.Flush
	}

	for {
		for _, f := range page {
			row(f)
		}
		if len(page) < statsExportPage {
			break
		}
		page, err = s.store.ListFilesAfter(r.Context(), u.ID, &page[len(page)-1], statsExportPage)
		if err != nil {
			// Too late for an error status. Aborting the response makes the
			// client see a failed download rather than a file that ends early.
			if r.Context().Err() == nil {
				log.Printf("stats export: user %s: %v", u.ID, err)
			}
			panic(http.ErrAbortHandler)
		}
	}
	end()
}

// csvCell keeps a value from being read as a formula. Spreadsheet apps
// evaluate a cell that starts with = + - @, a tab or a carriage return, and
// file names (and custom slugs) are chosen by whoever uploaded the file, so a
// name like =HYPERLINK(...) would become a live formula in the owner's
// spreadsheet. A leading apostrophe makes the cell plain text.
func csvCell(v string) string {
	if v != "" && strings.IndexByte("=+-@\t\r", v[0]) >= 0 {
		return "'" + v
	}
	return v
}

// activityTZPattern bounds the `tz` parameter to IANA-zone-shaped names
// ("Europe/Berlin", "America/Argentina/Buenos_Aires", "UTC", "Etc/GMT+5").
// Postgres is the one that actually resolves it (the runtime image ships no Go
// tzdata), so this only keeps junk out of the query; an unknown but well-formed
// name comes back as store.ErrUnknownTimeZone.
var activityTZPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+\-/]{0,63}$`)

// handleActivity returns per-day upload counts for the last `days` days (default
// 365), powering the streak / wrapped / activity heatmap. Days are calendar days
// in the time zone named by `tz` (the browser's zone, so they match the SPA's
// local-date heatmap cells); without it, or when Postgres does not know the
// zone, they are UTC days.
func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	days := clampInt(atoiDefault(r.URL.Query().Get("days"), 365), 1, 372)
	since := time.Now().AddDate(0, 0, -days)
	tz := r.URL.Query().Get("tz")
	if tz == "" {
		tz = "UTC"
	}
	if !activityTZPattern.MatchString(tz) {
		writeErr(w, http.StatusBadRequest, "invalid time zone")
		return
	}
	rows, err := s.store.ActivityByDay(r.Context(), u.ID, since, tz)
	if errors.Is(err, store.ErrUnknownTimeZone) {
		// The browser's tz database can be newer than the server's (renamed or
		// new zones such as Europe/Kyiv), and the SPA has no fallback: a 400
		// left the heatmap empty. UTC days are at worst a few hours off.
		rows, err = s.store.ActivityByDay(r.Context(), u.ID, since, "UTC")
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not load activity")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, dc := range rows {
		out = append(out, map[string]any{
			"day":   dc.Day.Format("2006-01-02"),
			"count": dc.Count,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
