package api

import (
	"strings"
	"testing"
	"unicode/utf8"

	"uploader/internal/db/store"
)

// A file name made of placeholders must come back verbatim as the default
// title. Rendering it as a template expanded every {filename} into the whole
// name: quadratic growth from a single anonymous GET /oembed.
func TestEmbedTitleDoesNotExpandTheFileName(t *testing.T) {
	s := &Server{}
	name := strings.Repeat("{filename}", 1000)
	rep := newEmbedReplacer(store.File{OriginalName: name, Slug: "abc"}, "")

	if got := s.embedTitle(rep, "", name); got != name {
		t.Fatalf("default title is %d bytes, want the %d-byte name verbatim", len(got), len(name))
	}
	if got := s.embedTitle(rep, "{slug}", name); got != "abc" {
		t.Fatalf("templated title = %q, want %q", got, "abc")
	}
}

// An operator template that repeats {filename} stays bounded by the name cap.
func TestEmbedReplacerCapsTheFileName(t *testing.T) {
	long := strings.Repeat("ä", 10_000) + ".png"
	rep := newEmbedReplacer(store.File{OriginalName: long}, "")

	got := rep.Replace("{filename}|{originalname}|{ext}")
	parts := strings.Split(got, "|")
	if len(parts) != 3 {
		t.Fatalf("rendered %q", got)
	}
	for i, p := range parts[:2] {
		if n := utf8.RuneCountInString(p); n != maxEmbedNameRunes {
			t.Errorf("part %d is %d runes, want %d", i, n, maxEmbedNameRunes)
		}
		if !utf8.ValidString(p) {
			t.Errorf("part %d split a rune", i)
		}
	}
	if parts[2] != "png" {
		t.Errorf("{ext} = %q, want png (taken from the full name)", parts[2])
	}
}

// {ext} is the extension the raw URL carries: sanitized, lowercased, and taken
// from the sniffed type when the name has none.
func TestEmbedReplacerExtMatchesRawURL(t *testing.T) {
	for name, want := range map[string]string{
		"photo.JPG":                    "jpg",
		"noext":                        "png",
		"weird.p n g":                  "png",
		"x." + strings.Repeat("a", 40): "png",
	} {
		rep := newEmbedReplacer(store.File{OriginalName: name, MimeType: "image/png"}, "")
		if got := rep.Replace("{ext}"); got != want {
			t.Errorf("{ext} for %q = %q, want %q", name, got, want)
		}
	}
}

func TestUsesPlaceholder(t *testing.T) {
	if usesPlaceholder([]string{"", "{filename} by someone"}, "{uploader}") {
		t.Fatal("reported {uploader} in templates without it")
	}
	if !usesPlaceholder([]string{"{filename}", "by {uploader}"}, "{uploader}") {
		t.Fatal("missed {uploader}")
	}
}
