package upload

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRandomSlugLengthAndAlphabet(t *testing.T) {
	for _, n := range []int{4, 6, 10} {
		s, err := RandomSlug(n)
		if err != nil {
			t.Fatalf("RandomSlug(%d): %v", n, err)
		}
		if len(s) != n {
			t.Errorf("RandomSlug(%d) length = %d", n, len(s))
		}
		for _, r := range s {
			if !strings.ContainsRune(slugAlphabet, r) {
				t.Errorf("slug %q contains out-of-alphabet rune %q", s, r)
			}
		}
	}
}

func TestRandomSlugMinLength(t *testing.T) {
	s, err := RandomSlug(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(s) < 4 {
		t.Errorf("expected min length 4, got %d", len(s))
	}
}

func TestStorageKeySharding(t *testing.T) {
	got := StorageKey("abcdef", ".png")
	want := "ab/cd/abcdef.png"
	if got != want {
		t.Errorf("StorageKey = %q, want %q", got, want)
	}
	if tk := ThumbKey("abcdef"); tk != "thumbs/ab/cd/abcdef.jpg" {
		t.Errorf("ThumbKey = %q", tk)
	}
	// Generated slugs with a fixed head shard by their random tail.
	for slug, want := range map[string]string{
		"2026-07-19-aB3kdq":      "aB/3k/2026-07-19-aB3kdq.png",
		"brave-quick-otter-k3m9": "k3/m9/brave-quick-otter-k3m9.png",
		"my-cool-link":           "li/nk/my-cool-link.png",
		"my-x":                   "my/-x/my-x.png",
		"abc":                    "_/abc.png",
	} {
		if got := StorageKey(slug, ".png"); got != want {
			t.Errorf("StorageKey(%q) = %q, want %q", slug, got, want)
		}
	}
}

// The HLS prefix is not stored: it must follow the file's stored storage key,
// so renditions published under the earlier slug-head sharding (every date slug
// of a year under hls/20/26/) are still served and purged.
func TestHLSPrefixFollowsStoredKey(t *testing.T) {
	for _, c := range []struct{ slug, key, want string }{
		{"2026-07-19-aB3kdq", "20/26/2026-07-19-aB3kdq.mp4", "hls/20/26/2026-07-19-aB3kdq"},
		{"2026-07-19-aB3kdq", StorageKey("2026-07-19-aB3kdq", ".mp4"), "hls/aB/3k/2026-07-19-aB3kdq"},
		{"abcdef", "ab/cd/abcdef.mp4", "hls/ab/cd/abcdef"},
		{"abc", "_/abc", "hls/_/abc"},
	} {
		if got := HLSPrefix(c.slug, c.key); got != c.want {
			t.Errorf("HLSPrefix(%q, %q) = %q, want %q", c.slug, c.key, got, c.want)
		}
	}
}

func TestValidCustomSlug(t *testing.T) {
	valid := []string{"abc", "my-link", "my_link", "AaBb09", strings.Repeat("a", 64)}
	for _, s := range valid {
		if !ValidCustomSlug(s) {
			t.Errorf("ValidCustomSlug(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "ab", "has space", "dot.ext", "slash/x", "..", strings.Repeat("a", 65), "emoji✨"}
	for _, s := range invalid {
		if ValidCustomSlug(s) {
			t.Errorf("ValidCustomSlug(%q) = true, want false", s)
		}
	}
}

func TestMimeBlocked(t *testing.T) {
	list := "application/x-msdownload, application/x-sh\n image/* , text/html"
	blocked := []string{
		"application/x-msdownload",
		"application/x-sh",
		"image/png",
		"image/svg+xml",
		"text/html; charset=utf-8",
		"TEXT/HTML",
	}
	for _, m := range blocked {
		if !MimeBlocked(list, m) {
			t.Errorf("MimeBlocked(%q) = false, want true", m)
		}
	}
	allowed := []string{"application/pdf", "video/mp4", "text/plain", "application/json"}
	for _, m := range allowed {
		if MimeBlocked(list, m) {
			t.Errorf("MimeBlocked(%q) = true, want false", m)
		}
	}
	if MimeBlocked("", "application/x-msdownload") {
		t.Error("empty blocklist should block nothing")
	}
	// Trailing-slash prefix form.
	if !MimeBlocked("application/", "application/zip") {
		t.Error(`"application/" should block application/zip`)
	}
}

func TestPreferredExt(t *testing.T) {
	if got := PreferredExt("clip.mp4", "video/mp4"); got != ".mp4" {
		t.Errorf("PreferredExt name = %q", got)
	}
	if got := PreferredExt("noext", "image/png"); got != ".png" {
		t.Errorf("PreferredExt from mime = %q", got)
	}
	if got := PreferredExt("noext", "application/octet-stream"); got != "" {
		t.Errorf("PreferredExt unknown = %q, want empty", got)
	}
}

func TestCleanFilename(t *testing.T) {
	cases := map[string]string{
		"photo.png":                     "photo.png",
		"  spaced name.jpg ":            "spaced name.jpg",
		`dir/sub\a"b.txt`:               "dir_sub_a_b.txt",
		"bell\x07\r\nname\x00.gif\x7f":  "bellname.gif",
		"bad\xffutf8.png":               "bad\uFFFDutf8.png",
		"\x01\x02":                      "file",
		"":                              "file",
		"müller–ß.pdf":                  "müller–ß.pdf",
		strings.Repeat("ä", 300):        strings.Repeat("ä", 255),
		strings.Repeat("a", 300) + ".X": strings.Repeat("a", 253) + ".X",
	}
	for in, want := range cases {
		if got := CleanFilename(in); got != want {
			t.Errorf("CleanFilename(%.40q) = %.60q, want %.60q", in, got, want)
		}
	}

	// A huge name keeps its (valid) extension and is cut to 255 runes.
	long := strings.Repeat("名", 5<<20) + ".mp4"
	got := CleanFilename(long)
	if n := utf8.RuneCountInString(got); n != 255 || !strings.HasSuffix(got, ".mp4") {
		t.Fatalf("CleanFilename(5M runes + .mp4) = %d runes ending %q", n, got[len(got)-8:])
	}
	// An invalid extension is not worth keeping; the name is just cut.
	got = CleanFilename(strings.Repeat("b", 300) + ".<script>")
	if got != strings.Repeat("b", 255) {
		t.Fatalf("CleanFilename(long, bad ext) = %.20q… (%d runes)", got, utf8.RuneCountInString(got))
	}
	// Cleaning is idempotent: SaveWith re-cleans names the handlers cleaned.
	for _, in := range []string{long, " a/b ", strings.Repeat("x ", 200) + ".png", "\x00"} {
		once := CleanFilename(in)
		if twice := CleanFilename(once); twice != once {
			t.Errorf("CleanFilename is not idempotent for %.20q: %.20q → %.20q", in, once, twice)
		}
	}
}

func TestSanitizeExt(t *testing.T) {
	cases := map[string]string{
		"photo.PNG":                    ".png",
		"a.tar.gz":                     ".gz",
		"noext":                        "",
		"evil.<script>":                "",
		"x." + strings.Repeat("a", 20): "",
		"clip.mp4":                     ".mp4",
	}
	for in, want := range cases {
		if got := SanitizeExt(in); got != want {
			t.Errorf("SanitizeExt(%q) = %q, want %q", in, got, want)
		}
	}
}
