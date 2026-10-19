package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthcheckProbesReadiness(t *testing.T) {
	cases := []struct {
		name    string
		ready   int
		wantErr bool
	}{
		{"ready", http.StatusOK, false},
		{"database or storage down", http.StatusServiceUnavailable, true},
		// Rate-limited: the server is answering, so liveness decides.
		{"rate limited", http.StatusTooManyRequests, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/ready":
					w.WriteHeader(c.ready)
				case "/api/v1/health":
					w.WriteHeader(http.StatusOK)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			t.Setenv("UPLOADER_HTTP_ADDR", strings.TrimPrefix(srv.URL, "http://"))

			err := runHealthcheck()
			if (err != nil) != c.wantErr {
				t.Fatalf("runHealthcheck() = %v, want error: %v", err, c.wantErr)
			}
		})
	}
}
