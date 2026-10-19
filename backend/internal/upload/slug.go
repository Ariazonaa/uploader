// Package upload handles the file-upload pipeline: slug generation, storage-key
// layout, multipart parsing and metadata persistence.
package upload

import (
	"crypto/rand"
	"path"
	"strings"
	"unicode/utf8"
)

// slugAlphabet excludes visually ambiguous characters (0/O, 1/l/I) so slugs are
// easy to read and dictate.
const slugAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// RandomSlug returns a cryptographically random slug of length n. It uses
// rejection sampling so every character is drawn uniformly from the alphabet
// (a plain byte%len would bias the first 256%len characters).
func RandomSlug(n int) (string, error) {
	if n < 4 {
		n = 4
	}
	// Largest multiple of the alphabet size that fits in a byte; reject above it.
	limit := byte(256 - (256 % len(slugAlphabet)))

	out := make([]byte, n)
	buf := make([]byte, n)
	for filled := 0; filled < n; {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if b >= limit {
				continue // would bias the distribution; draw again
			}
			out[filled] = slugAlphabet[int(b)%len(slugAlphabet)]
			filled++
			if filled == n {
				break
			}
		}
	}
	return string(out), nil
}

// StorageKey lays out the blob path: two 2-char shard directories keep any
// single directory from growing unbounded. ext includes the leading dot (or is
// empty). The slug controls the whole path, so it is never user-supplied text.
// The key is stored with the file and never re-derived, so the shard scheme
// can change without moving existing blobs.
func StorageKey(slug, ext string) string {
	return path.Join(shard(slug), slug+ext)
}

// ThumbKey is the storage key for a file's thumbnail (always JPEG). Like
// StorageKey, it is stored (thumb_key) once the thumbnail exists.
func ThumbKey(slug string) string {
	return path.Join("thumbs", shard(slug), slug+".jpg")
}

// HLSPrefix is the storage-key prefix for a file's HLS playlist + segments. It
// is not stored, so it is derived from the file's stored storage key (whose
// directory is the shard the file was created with) rather than from the
// current shard scheme: renditions published under an earlier scheme must
// still be found and purged.
func HLSPrefix(slug, storageKey string) string {
	return path.Join("hls", path.Dir(storageKey), slug)
}

// shard names the two directory levels for a new slug's files, taken from its
// random tail: date slugs ("2026-07-19-aB3kdq") share their first characters
// for a whole year and gfycat slugs for a whole adjective, which put them all
// in one directory. Random slugs have no '-' and shard by their start.
func shard(slug string) string {
	if i := strings.LastIndexByte(slug, '-'); i >= 0 && len(slug)-i-1 >= 4 {
		slug = slug[i+1:]
	}
	if len(slug) < 4 {
		return "_"
	}
	return path.Join(slug[0:2], slug[2:4])
}

// ValidCustomSlug reports whether a user-requested slug is acceptable: 3–64
// characters of [A-Za-z0-9_-]. The charset excludes path separators and dots, so
// a custom slug can never affect the storage path or be mistaken for a "slug.ext"
// raw URL. Uniqueness is still enforced by the database.
func ValidCustomSlug(s string) bool {
	if len(s) < 3 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// PreferredExt returns the best file extension (with leading dot) for a file,
// derived from its original name, falling back to one inferred from the MIME
// type. Used to build raw URLs that end in a real extension so chat clients
// (Discord, etc.) auto-embed the media. Returns "" when none is known.
func PreferredExt(originalName, mime string) string {
	if ext := SanitizeExt(originalName); ext != "" {
		return ext
	}
	return extFromMime(mime)
}

// MimeBlocked reports whether mime matches the operator's blocklist. The list is
// comma/space/newline separated; each entry is either an exact MIME type
// ("application/x-msdownload"), a trailing-slash prefix ("application/" blocks
// every application/* type) or a "*" wildcard prefix ("application/x-*"). Matching
// ignores any "; charset=" parameter and is case-insensitive.
func MimeBlocked(list, mime string) bool {
	if strings.TrimSpace(list) == "" {
		return false
	}
	m := strings.ToLower(strings.TrimSpace(mime))
	if i := strings.IndexByte(m, ';'); i >= 0 {
		m = strings.TrimSpace(m[:i])
	}
	for _, raw := range strings.FieldsFunc(list, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t'
	}) {
		entry := strings.ToLower(strings.TrimSpace(raw))
		if entry == "" {
			continue
		}
		switch {
		case strings.HasSuffix(entry, "*"):
			if strings.HasPrefix(m, strings.TrimSuffix(entry, "*")) {
				return true
			}
		case strings.HasSuffix(entry, "/"):
			if strings.HasPrefix(m, entry) {
				return true
			}
		default:
			if m == entry {
				return true
			}
		}
	}
	return false
}

// maxFilenameRunes caps a stored original filename. A multipart part header
// may carry a filename of several megabytes.
const maxFilenameRunes = 255

// CleanFilename turns a client-provided filename into the one that is stored:
// path separators and quotes become '_', control characters are dropped,
// invalid UTF-8 becomes U+FFFD, and names over 255 runes are cut, keeping a
// valid extension (see SanitizeExt) so raw links and downloads keep their
// type. A name with nothing left becomes "file".
func CleanFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
			continue
		case r == '/' || r == '\\' || r == '"':
			r = '_'
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(out) > maxFilenameRunes {
		ext := ""
		if SanitizeExt(out) != "" {
			ext = path.Ext(out)
		}
		keep, n := maxFilenameRunes-len(ext), 0
		for i := range out {
			if n == keep {
				out = out[:i]
				break
			}
			n++
		}
		out = strings.TrimSpace(out) + ext
	}
	if out == "" {
		return "file"
	}
	return out
}

// SanitizeExt returns a safe, lowercase file extension (max 12 chars) derived
// from a client-provided filename. Returns "" when there is no usable ext.
func SanitizeExt(filename string) string {
	ext := strings.ToLower(path.Ext(filename))
	if ext == "" || len(ext) > 12 {
		return ""
	}
	for _, r := range ext[1:] {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
			return ""
		}
	}
	return ext
}
