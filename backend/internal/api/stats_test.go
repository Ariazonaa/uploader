package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"uploader/internal/auth"
	"uploader/internal/db/store"
)

func TestActivityTZPattern(t *testing.T) {
	ok := []string{"UTC", "Europe/Berlin", "America/Argentina/Buenos_Aires", "Etc/GMT+5", "Etc/GMT-14"}
	bad := []string{"", "/etc/passwd", "Europe/Berlin'; --", "Europe Berlin", "1Europe", "a\x00b",
		strings.Repeat("A", 65)} // longer than any real zone name
	for _, tz := range ok {
		if !activityTZPattern.MatchString(tz) {
			t.Errorf("tz %q rejected, want accepted", tz)
		}
	}
	for _, tz := range bad {
		if activityTZPattern.MatchString(tz) {
			t.Errorf("tz %q accepted, want rejected", tz)
		}
	}
}

func TestCSVCellDefusesFormulas(t *testing.T) {
	for in, want := range map[string]string{
		`=HYPERLINK("https://evil.example","x")`: `'=HYPERLINK("https://evil.example","x")`,
		"+1+1":                                   "'+1+1",
		"-2+3":                                   "'-2+3",
		"@SUM(A1)":                               "'@SUM(A1)",
		"\t=1":                                   "'\t=1",
		"\r=1":                                   "'\r=1",
		"holiday.png":                            "holiday.png",
		"a=b":                                    "a=b",
		"":                                       "",
		"1024":                                   "1024",
		"https://x/s/":                           "https://x/s/",
	} {
		if got := csvCell(in); got != want {
			t.Errorf("csvCell(%q) = %q, want %q", in, got, want)
		}
	}
}

// A malformed tz is refused with 400 before the store is ever touched (the
// zero Server has none, so reaching it would panic).
func TestHandleActivityRejectsMalformedTZ(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/stats/activity?tz=Europe%2FBerlin%27", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &store.User{}))
	rec := httptest.NewRecorder()
	s.handleActivity(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
