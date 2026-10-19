package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"uploader/internal/blobcrypt"
	"uploader/internal/db/store"
)

// A multi-range request for an encrypted blob gets the whole file in one
// pass, not one 64 KiB chunk decryption per listed range; a single range is
// still served as 206.
func TestSingleRangeOnlyForEncryptedBlobs(t *testing.T) {
	key := blobcrypt.DeriveKey(bytes.Repeat([]byte{7}, 32))
	plain := make([]byte, 5*65536+123)
	for i := range plain {
		plain[i] = byte(i)
	}
	enc, err := blobcrypt.NewEncryptingReader(bytes.NewReader(plain), key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := io.ReadAll(enc)
	if err != nil {
		t.Fatal(err)
	}
	serve := func(rangeHeader string) *httptest.ResponseRecorder {
		t.Helper()
		dr, err := blobcrypt.NewReader(bytes.NewReader(sealed), key, int64(len(plain)))
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, "/raw/x", nil)
		r.Header.Set("Range", rangeHeader)
		singleRangeOnly(r)
		w := httptest.NewRecorder()
		http.ServeContent(w, r, "x.bin", time.Time{}, dr)
		return w
	}

	var ranges []string
	for i := range 5 {
		ranges = append(ranges, fmt.Sprintf("%d-%d", i*65536, i*65536))
	}
	w := serve("bytes=" + strings.Join(ranges, ","))
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), plain) {
		t.Fatalf("multi-range: status %d, %d bytes (%s); want 200 with the whole file",
			w.Code, w.Body.Len(), w.Header().Get("Content-Type"))
	}
	w = serve("bytes=70000-70009")
	if w.Code != http.StatusPartialContent || !bytes.Equal(w.Body.Bytes(), plain[70000:70010]) {
		t.Fatalf("single range: status %d, body %v; want 206 with bytes 70000-70009", w.Code, w.Body.Bytes())
	}
}

// A blob sealed under another key, or with a damaged first chunk, is refused
// before anything is sent; a good one is rewound for ServeContent.
func TestVerifyFirstChunk(t *testing.T) {
	key := blobcrypt.DeriveKey(bytes.Repeat([]byte{7}, 32))
	seal := func(plain []byte) []byte {
		t.Helper()
		enc, err := blobcrypt.NewEncryptingReader(bytes.NewReader(plain), key)
		if err != nil {
			t.Fatal(err)
		}
		sealed, err := io.ReadAll(enc)
		if err != nil {
			t.Fatal(err)
		}
		return sealed
	}
	open := func(sealed []byte, k []byte, size int) io.ReadSeeker {
		t.Helper()
		dr, err := blobcrypt.NewReader(bytes.NewReader(sealed), k, int64(size))
		if err != nil {
			t.Fatal(err)
		}
		return dr
	}
	plain := bytes.Repeat([]byte("abc"), 30000)
	sealed := seal(plain)

	dr := open(sealed, key, len(plain))
	if err := verifyFirstChunk(dr); err != nil {
		t.Fatalf("intact blob: %v", err)
	}
	if got, err := io.ReadAll(dr); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("after verification: %d bytes, %v; want the whole plaintext from offset 0", len(got), err)
	}

	wrongKey := blobcrypt.DeriveKey(bytes.Repeat([]byte{8}, 32))
	if err := verifyFirstChunk(open(sealed, wrongKey, len(plain))); !errors.Is(err, blobcrypt.ErrAuth) {
		t.Fatalf("wrong key: %v, want ErrAuth", err)
	}
	damaged := bytes.Clone(sealed)
	damaged[40] ^= 1
	if err := verifyFirstChunk(open(damaged, key, len(plain))); !errors.Is(err, blobcrypt.ErrAuth) {
		t.Fatalf("damaged first chunk: %v, want ErrAuth", err)
	}
	if err := verifyFirstChunk(open(seal(nil), key, 0)); err != nil {
		t.Fatalf("empty blob: %v", err)
	}
}

func TestRangeFromStart(t *testing.T) {
	for h, want := range map[string]bool{
		"":                false,
		"bytes=0-":        true,
		"bytes=0-1":       true,
		"bytes= 0-99":     true,
		"bytes=0-9,20-29": true,
		"bytes=10-19":     false,
		"bytes=-500":      false,
		"bytes=00-":       false,
		"items=0-9":       false,
	} {
		if got := rangeFromStart(h); got != want {
			t.Errorf("rangeFromStart(%q) = %v, want %v", h, got, want)
		}
	}
}

// statusHook reports the status ServeContent commits to exactly once, also
// when the body is written without an explicit WriteHeader.
func TestStatusHook(t *testing.T) {
	var got []int
	rec := httptest.NewRecorder()
	h := &statusHook{ResponseWriter: rec, onStatus: func(code int) { got = append(got, code) }}
	if _, err := io.Copy(h, strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	h.WriteHeader(http.StatusTeapot)
	if len(got) != 1 || got[0] != http.StatusOK || rec.Body.String() != "hello" {
		t.Fatalf("statuses %v, body %q; want one 200 and the body", got, rec.Body)
	}
}

// The ShareX deletion link only soft-deletes, so its page must not promise a
// permanent deletion.
func TestDeleteConfirmPageMentionsTrash(t *testing.T) {
	var b strings.Builder
	if err := deleteConfirmTmpl.Execute(&b, map[string]string{"Name": "a.png", "Action": "/d/k"}); err != nil {
		t.Fatal(err)
	}
	if page := b.String(); strings.Contains(strings.ToLower(page), "permanent") || !strings.Contains(page, "trash") {
		t.Fatalf("confirmation page does not describe a soft delete: %s", page)
	}
}

func TestSlugFromParam(t *testing.T) {
	cases := map[string]string{
		"aB3kdq":         "aB3kdq",
		"aB3kdq.mp4":     "aB3kdq",
		"aB3kdq.tar.gz":  "aB3kdq.tar", // only the last dot starts the extension
		".hidden":        ".hidden",    // leading dot is not an extension
		"my-custom-slug": "my-custom-slug",
	}
	for in, want := range cases {
		if got := slugFromParam(in); got != want {
			t.Errorf("slugFromParam(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsDangerousInline(t *testing.T) {
	// These render as active content in a browser, so they must never be served
	// inline from our own origin — that would be stored XSS against the app.
	dangerous := []string{
		"text/html",
		"text/html; charset=utf-8",
		"TEXT/HTML",
		"image/svg+xml",
		"application/xhtml+xml",
		"text/xml",
		"application/xml",
		"application/javascript",
		"text/javascript",
	}
	for _, m := range dangerous {
		if !isDangerousInline(m) {
			t.Errorf("isDangerousInline(%q) = false, want true", m)
		}
	}
	safe := []string{"image/png", "image/jpeg", "video/mp4", "application/pdf", "text/plain"}
	for _, m := range safe {
		if isDangerousInline(m) {
			t.Errorf("isDangerousInline(%q) = true, want false", m)
		}
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		`normal.png`:          "normal.png",
		`../../etc/passwd`:    ".._.._etc_passwd",
		`a"b.png`:             `a_b.png`,
		"line\r\nbreak.png":   "linebreak.png", // CR/LF would split the header
		`C:\Windows\evil.exe`: "C:_Windows_evil.exe",
		"   ":                 "file", // never emit an empty filename
		"":                    "file",
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeFilenameDropsControlCharacters(t *testing.T) {
	got := sanitizeFilename("a\x00b\x1fc\x7fd.png")
	if strings.ContainsAny(got, "\x00\x1f\x7f") {
		t.Fatalf("sanitizeFilename left control characters in %q", got)
	}
	if got != "abcd.png" {
		t.Fatalf("sanitizeFilename = %q, want abcd.png", got)
	}
}

func TestHLSNameAllowlist(t *testing.T) {
	// handleHLS builds a storage key from this parameter, so anything outside the
	// files we generate has to be refused before it gets there.
	for _, ok := range []string{"index.m3u8", "seg000.ts", "seg1234.ts"} {
		if !hlsName.MatchString(ok) {
			t.Errorf("hlsName rejected %q, want accepted", ok)
		}
	}
	for _, bad := range []string{
		"../../secret", "index.m3u8/../x", "seg1.ts", "seg000.TS",
		"other.m3u8", "", "index.m3u8 ", "seg000.ts.bak",
	} {
		if hlsName.MatchString(bad) {
			t.Errorf("hlsName accepted %q, want rejected", bad)
		}
	}
}

func TestVersionSegmentURIs(t *testing.T) {
	in := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:6\n\n" +
		"#EXTINF:6.000000,\nseg000.ts\n#EXTINF:2.5,\r\nseg001.ts\r\nother.ts?x=1\n#EXT-X-ENDLIST\nseg002.ts"
	want := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:6\n\n" +
		"#EXTINF:6.000000,\nseg000.ts?v=abc\n#EXTINF:2.5,\r\nseg001.ts?v=abc\r\nother.ts?x=1&v=abc\n#EXT-X-ENDLIST\nseg002.ts?v=abc"
	if got := string(versionSegmentURIs([]byte(in), "abc")); got != want {
		t.Fatalf("versionSegmentURIs =\n%q\nwant\n%q", got, want)
	}
}

// The player relaxes framing only: the rest of the global policy, including
// object-src and base-uri, stays in force.
func TestWithFrameAncestorsKeepsTheGlobalPolicy(t *testing.T) {
	rec := httptest.NewRecorder()
	securityHeaders(false)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/p/x", nil))
	global := rec.Header().Get("Content-Security-Policy")

	got := withFrameAncestors(global, "https:")
	for _, d := range strings.Split(global, ";") {
		d = strings.TrimSpace(d)
		if strings.HasPrefix(d, "frame-ancestors") {
			continue
		}
		if !strings.Contains(got, d) {
			t.Errorf("player policy %q lost %q", got, d)
		}
	}
	if !strings.HasSuffix(got, "; frame-ancestors https:") || strings.Contains(got, "frame-ancestors 'none'") {
		t.Errorf("player policy %q: want frame-ancestors https: only", got)
	}
	if got := withFrameAncestors("", "https:"); got != "frame-ancestors https:" {
		t.Errorf("without a global policy: %q", got)
	}
}

func TestEtagForHidesContentHash(t *testing.T) {
	const contentHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	s := testServer(nil)
	s.SetEncryptionKey([]byte("0123456789abcdef0123456789abcdef"))

	tag := s.etagFor(contentHash)
	if tag == "" {
		t.Fatal("etagFor returned an empty tag")
	}
	// The raw SHA-256 must not leak: it would let anyone confirm that a share
	// link holds a specific known file without downloading it.
	if strings.Contains(contentHash, tag) {
		t.Fatal("etagFor exposed the raw content hash")
	}
	// Stable for the same input (conditional requests must keep working) …
	if again := s.etagFor(contentHash); again != tag {
		t.Fatal("etagFor is not deterministic")
	}
	// … and distinct for different content.
	if other := s.etagFor("00" + contentHash[2:]); other == tag {
		t.Fatal("etagFor collided for different content hashes")
	}
}

func TestUACategory(t *testing.T) {
	cases := map[string]string{
		"":                              "other",
		"Mozilla/5.0 (Windows NT 10.0)": "desktop",
		"Discordbot/2.0":                "discord",
		"TelegramBot (like TwitterBot)": "telegram",
		"Slackbot-LinkExpanding 1.0":    "slack",
		"Googlebot/2.1":                 "bot",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0)": "mobile",
	}
	for ua, want := range cases {
		r := requestFrom("203.0.113.1:1234", map[string]string{"User-Agent": ua})
		if got := uaCategory(r); got != want {
			t.Errorf("uaCategory(%q) = %q, want %q", ua, got, want)
		}
	}
}

func TestRefererHostDropsPathAndQuery(t *testing.T) {
	r := requestFrom("203.0.113.1:1234", map[string]string{
		"Referer": "https://example.com:8443/secret/path?token=abc",
	})
	if got := refererHost(r); got != "example.com" {
		t.Fatalf("refererHost = %q, want example.com (no path, port or query)", got)
	}
}

func TestTruncateBoundsStoredFields(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Fatalf("truncate shortened a short string: %q", got)
	}
	long := strings.Repeat("ä", 1000) // multi-byte: must not split a rune
	got := truncate(long, maxUserAgentLen)
	if len([]rune(got)) != maxUserAgentLen {
		t.Fatalf("truncate produced %d runes, want %d", len([]rune(got)), maxUserAgentLen)
	}
	if !strings.HasPrefix(long, got) {
		t.Fatal("truncate corrupted the string (split a multi-byte rune?)")
	}
}

func TestNormalizeReferrerHost(t *testing.T) {
	cases := map[string]string{
		"Example.COM":                   "example.com",
		"example.com.":                  "example.com",
		"cdn.discordapp.com":            "cdn.discordapp.com",
		"203.0.113.9":                   "203.0.113.9",
		"":                              store.ReferrerOther,
		"::1":                           store.ReferrerOther,
		"bad_host.com":                  store.ReferrerOther,
		"-x.com":                        store.ReferrerOther,
		"a..b":                          store.ReferrerOther,
		"bücher.de":                     store.ReferrerOther,
		strings.Repeat("a", 64):         store.ReferrerOther, // label too long
		strings.Repeat("a.", 127) + "a": store.ReferrerOther, // 255 chars
	}
	for in, want := range cases {
		if got := normalizeReferrerHost(in); got != want {
			t.Errorf("normalizeReferrerHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRefererHostBuckets(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/raw/x", nil)
	if got := refererHost(r); got != "" {
		t.Fatalf("no Referer: got %q, want \"\" (recorded as direct)", got)
	}
	r.Header.Set("Referer", "https://"+strings.Repeat("x", 300)+".com/")
	if got := refererHost(r); got != store.ReferrerOther {
		t.Fatalf("oversized host: got %q, want %q", got, store.ReferrerOther)
	}
	r.Header.Set("Referer", "%zz")
	if got := refererHost(r); got != store.ReferrerOther {
		t.Fatalf("unparseable Referer: got %q, want %q", got, store.ReferrerOther)
	}
	r.Header.Set("Referer", "https://WWW.Example.com:8443/a?b=c")
	if got := refererHost(r); got != "www.example.com" {
		t.Fatalf("got %q, want www.example.com", got)
	}
}

func TestCleanTextMakesValuesStorable(t *testing.T) {
	got := cleanText("Mozilla\xff\xfe/5.0\x00 (X11)", maxUserAgentLen)
	if !utf8.ValidString(got) {
		t.Fatalf("cleanText left invalid UTF-8: %q", got)
	}
	if strings.ContainsRune(got, 0) {
		t.Fatalf("cleanText left a NUL byte: %q", got)
	}
	if got != "Mozilla�/5.0 (X11)" {
		t.Fatalf("cleanText = %q", got)
	}
	if n := len([]rune(cleanText(strings.Repeat("\xff", 2000), maxAuditFieldLen))); n > maxAuditFieldLen {
		t.Fatalf("cleanText returned %d runes, want <= %d", n, maxAuditFieldLen)
	}
}

func TestCleanMetadataSanitisesNestedStrings(t *testing.T) {
	md := map[string]any{
		"name":  "tok\x00en\xff",
		"keys":  []string{"a\x00"},
		"count": 3,
		"inner": map[string]any{"v": []any{"x\xff"}},
	}
	b, err := json.Marshal(cleanMetadata(md))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `\u0000`) {
		t.Fatalf("metadata still carries a NUL escape (JSONB rejects it): %s", b)
	}
	if !utf8.Valid(b) {
		t.Fatalf("metadata is not valid UTF-8: %q", b)
	}
}
