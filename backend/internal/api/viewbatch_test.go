package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"uploader/internal/auth"
	"uploader/internal/config"
	"uploader/internal/db/store"
	"uploader/internal/settings"
)

// Views are aggregated per bucket between flushes; a flush hands each bucket
// over once and leaves nothing pending.
func TestViewBatcherAggregates(t *testing.T) {
	var got []store.ViewDelta
	b := newViewBatcher(func(_ context.Context, d []store.ViewDelta) error {
		got = append(got, d...)
		return nil
	})
	for i := 0; i < 3; i++ {
		b.add("abc", "example.com", "desktop", true)
	}
	b.add("abc", "example.com", "desktop", false) // share page: breakdown only
	b.add("abc", "", "bot", true)

	b.flush(context.Background())
	if len(got) != 2 {
		t.Fatalf("flushed %d buckets, want 2: %+v", len(got), got)
	}
	for _, d := range got {
		switch d.UACategory {
		case "desktop":
			if d.Count != 4 || d.Views != 3 {
				t.Errorf("desktop bucket = %+v, want count 4, views 3", d)
			}
		case "bot":
			if d.Count != 1 || d.Views != 1 {
				t.Errorf("bot bucket = %+v, want 1/1", d)
			}
		}
	}
	got = nil
	b.flush(context.Background())
	if len(got) != 0 {
		t.Fatalf("second flush wrote %d buckets, want none", len(got))
	}
}

// New buckets past the cap are dropped; buckets already pending keep counting.
func TestViewBatcherCapsPendingBuckets(t *testing.T) {
	var got []store.ViewDelta
	b := newViewBatcher(func(_ context.Context, d []store.ViewDelta) error {
		got = append(got, d...)
		return nil
	})
	for i := 0; i < maxPendingViewKeys+50; i++ {
		b.add("abc", fmt.Sprintf("h%d.example", i), "desktop", true)
	}
	b.add("abc", "h0.example", "desktop", true)
	b.flush(context.Background())
	if len(got) != maxPendingViewKeys {
		t.Fatalf("flushed %d buckets, want the cap of %d", len(got), maxPendingViewKeys)
	}
	for _, d := range got {
		if d.ReferrerHost == "h0.example" && d.Count != 2 {
			t.Fatalf("pending bucket stopped counting at the cap: %+v", d)
		}
	}
}

// A media request from the instance's own pages counts the view but adds no
// breakdown row; one from elsewhere records both.
func TestMediaViewRecorderOwnReferer(t *testing.T) {
	var got []store.ViewDelta
	s := &Server{
		cfg:      &config.Config{},
		settings: settings.NewStatic(settings.Snapshot{PublicURL: "https://up.example"}),
		views: newViewBatcher(func(_ context.Context, d []store.ViewDelta) error {
			got = append(got, d...)
			return nil
		}),
	}
	request := func(referer string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/raw/abc.png", nil)
		if referer != "" {
			r.Header.Set("Referer", referer)
		}
		return r
	}
	s.mediaViewRecorder(request("https://UP.example/s/abc"), "abc")()
	s.mediaViewRecorder(request("https://discord.com/channels/1"), "abc")()
	s.mediaViewRecorder(request(""), "abc")()

	s.views.flush(context.Background())
	var count, views int64
	for _, d := range got {
		if d.ReferrerHost == "up.example" {
			t.Errorf("the instance's own host was recorded as a referrer: %+v", d)
		}
		count += d.Count
		views += d.Views
	}
	if count != 2 || views != 3 {
		t.Fatalf("recorded %d breakdown entries and %d views, want 2 and 3: %+v", count, views, got)
	}
}

// A blank name is refused, not turned into "file".
func TestRenameFileRejectsBlankName(t *testing.T) {
	s := &Server{}
	for _, body := range []string{`{}`, `{"name":"   "}`, `{"name":"\u0001\u0002"}`} {
		req := httptest.NewRequest(http.MethodPatch, "/files/x", strings.NewReader(body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", uuid.NewString())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		req = req.WithContext(auth.WithUser(ctx, &store.User{ID: uuid.New()}))
		rec := httptest.NewRecorder()
		s.handleRenameFile(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("rename with body %s: status %d, want 400", body, rec.Code)
		}
	}
}
