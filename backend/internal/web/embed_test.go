package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testHandler() http.Handler {
	return handler(fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>app</title>")},
		"assets/index-abc1.js": {Data: []byte("console.log(1)")},
		"sw.js":                {Data: []byte("self.addEventListener('push', () => {})")},
	})
}

func get(h http.Handler, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHashedAssetsAreImmutable(t *testing.T) {
	h := testHandler()
	rec := get(h, "/assets/index-abc1.js", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "console.log(1)" {
		t.Fatalf("asset: %d %q", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != immutableCache {
		t.Errorf("asset Cache-Control = %q, want %q", cc, immutableCache)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("asset has no ETag")
	}
	if rec := get(h, "/assets/index-abc1.js", map[string]string{"If-None-Match": etag}); rec.Code != http.StatusNotModified {
		t.Errorf("revalidation: status %d, want 304", rec.Code)
	}
}

// Files outside assets/ keep their name across builds and must not be cached
// as immutable.
func TestUnhashedFilesRevalidate(t *testing.T) {
	rec := get(testHandler(), "/sw.js", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-cache" || rec.Header().Get("ETag") == "" {
		t.Fatalf("sw.js: %d, Cache-Control %q, ETag %q", rec.Code, rec.Header().Get("Cache-Control"), rec.Header().Get("ETag"))
	}
}

func TestShellIsNoCacheWithETag(t *testing.T) {
	h := testHandler()
	for _, p := range []string{"/", "/index.html", "/settings", "/assets/", "/assets"} {
		rec := get(h, p, nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>app</title>") {
			t.Errorf("%s: %d %q, want the SPA shell", p, rec.Code, rec.Body)
			continue
		}
		if cc := rec.Header().Get("Cache-Control"); !strings.HasPrefix(cc, "no-cache") {
			t.Errorf("%s: Cache-Control = %q, want no-cache", p, cc)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: Content-Type = %q", p, ct)
		}
	}
	etag := get(h, "/", nil).Header().Get("ETag")
	if rec := get(h, "/dashboard", map[string]string{"If-None-Match": etag}); etag == "" || rec.Code != http.StatusNotModified {
		t.Errorf("shell revalidation with ETag %q: status %d, want 304", etag, rec.Code)
	}
}
