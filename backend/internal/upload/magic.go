package upload

import (
	"bytes"
	"encoding/binary"
)

// blockedContent reports whether an upload matches the operator's MIME
// blocklist, by its sniffed type or by a type blocklistTypes recognizes in its
// first bytes.
func blockedContent(list, sniffed string, head []byte) bool {
	if MimeBlocked(list, sniffed) {
		return true
	}
	for _, t := range blocklistTypes(sniffed, head) {
		if MimeBlocked(list, t) {
			return true
		}
	}
	return false
}

// blocklistTypes names the content kinds operators actually want to block but
// http.DetectContentType does not know: executables sniff as
// application/octet-stream, "#!" scripts and SVG as text. The names are used
// for blocklist matching only; the stored and served type stays the sniffed
// one. Several names are returned where several are in common use.
func blocklistTypes(sniffed string, head []byte) []string {
	// A real executable header contains binary bytes, so it never sniffs as
	// text; requiring that keeps a text file starting with "MZ" off the list.
	if sniffed == "application/octet-stream" {
		switch {
		case bytes.HasPrefix(head, []byte("MZ")):
			return []string{"application/x-msdownload", "application/vnd.microsoft.portable-executable", "application/x-dosexec"}
		case bytes.HasPrefix(head, []byte("\x7fELF")):
			return []string{"application/x-executable", "application/x-elf"}
		case isMachO(head):
			return []string{"application/x-mach-binary"}
		}
	}
	switch {
	case bytes.HasPrefix(head, []byte("#!")):
		return []string{"text/x-shellscript", "application/x-sh"}
	case isSVG(head):
		return []string{"image/svg+xml"}
	}
	return nil
}

// isMachO recognizes thin (32/64-bit, either byte order) and universal Mach-O
// binaries.
func isMachO(b []byte) bool {
	if len(b) < 8 {
		return false
	}
	switch binary.BigEndian.Uint32(b) {
	case 0xfeedface, 0xfeedfacf, 0xcefaedfe, 0xcffaedfe, 0xcafebabf:
		return true
	case 0xcafebabe:
		// Shared with Java class files. The next word is the architecture count
		// of a universal binary, but the class-file version (45 and up) of a
		// class file.
		n := binary.BigEndian.Uint32(b[4:])
		return n > 0 && n < 45
	}
	return false
}

// isSVG reports whether b starts an SVG document: an <svg> root element,
// optionally after a byte-order mark, whitespace, an XML declaration,
// processing instructions, comments and a doctype.
func isSVG(b []byte) bool {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	for {
		b = bytes.TrimLeft(b, " \t\r\n")
		var end []byte
		switch {
		case len(b) >= 4 && bytes.EqualFold(b[:4], []byte("<svg")):
			return len(b) == 4 || bytes.IndexByte([]byte(" \t\r\n>/"), b[4]) >= 0
		case bytes.HasPrefix(b, []byte("<?")):
			end = []byte("?>")
		case bytes.HasPrefix(b, []byte("<!--")):
			end = []byte("-->")
		case bytes.HasPrefix(b, []byte("<!")):
			// A doctype, whose internal subset ([...]) may itself contain '>'.
			if i, j := bytes.IndexByte(b, '['), bytes.IndexByte(b, '>'); i >= 0 && i < j {
				k := bytes.IndexByte(b[i:], ']')
				if k < 0 {
					return false
				}
				b = b[i+k:]
			}
			end = []byte(">")
		default:
			return false
		}
		i := bytes.Index(b, end)
		if i < 0 {
			return false
		}
		b = b[i+len(end):]
	}
}
