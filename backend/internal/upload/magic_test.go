package upload

import (
	"net/http"
	"strings"
	"testing"
)

// Executables, scripts and SVG sniff as octet-stream or text, so the blocklist
// matches them through blocklistTypes.
func TestBlockedContentRecognizesExecutablesAndSVG(t *testing.T) {
	pe := "MZ\x90\x00\x03\x00\x00\x00\x04\x00\x00\x00\xff\xff\x00\x00" + strings.Repeat("\x00", 48)
	cases := []struct {
		name, head, entry string
		want              bool
	}{
		{"windows exe", pe, "application/x-msdownload", true},
		{"windows exe, prefix", pe, "application/x-*", true},
		{"windows exe, iana name", pe, "application/vnd.microsoft.portable-executable", true},
		{"elf", "\x7fELF\x02\x01\x01\x00" + strings.Repeat("\x00", 8), "application/x-executable", true},
		{"mach-o 64", "\xcf\xfa\xed\xfe\x07\x00\x00\x01" + strings.Repeat("\x00", 8), "application/x-mach-binary", true},
		{"universal mach-o", "\xca\xfe\xba\xbe\x00\x00\x00\x02" + strings.Repeat("\x00", 8), "application/x-mach-binary", true},
		{"java class is not mach-o", "\xca\xfe\xba\xbe\x00\x00\x00\x41" + strings.Repeat("\x00", 8), "application/x-mach-binary", false},
		{"shell script", "#!/bin/sh\nrm -rf /\n", "text/x-shellscript", true},
		{"python script", "#!/usr/bin/env python3\nprint(1)\n", "application/x-sh", true},
		{"svg", `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`, "image/svg+xml", true},
		{"svg, prolog and doctype", "\xef\xbb\xbf<?xml version=\"1.0\"?>\n<!-- made by hand -->\n" +
			`<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd">` + "\n<SVG>", "image/", true},
		{"svg, doctype subset", `<!DOCTYPE svg [ <!ENTITY a "b"> ]><svg/>`, "image/svg+xml", true},
		{"text starting with MZ", "MZ is a text file", "application/x-msdownload", false},
		{"text mentioning svg", "notes about <svg> tags", "image/svg+xml", false},
		{"not an svg element", "<svgfoo></svgfoo>", "image/svg+xml", false},
		{"html", "<html><svg></svg></html>", "image/svg+xml", false},
		// The sniffed type still matches on its own.
		{"png", "\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 8), "image/png", true},
		{"png not exe", "\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 8), "application/x-msdownload", false},
	}
	for _, c := range cases {
		head := []byte(c.head)
		sniffed := http.DetectContentType(head)
		if got := blockedContent(c.entry, sniffed, head); got != c.want {
			t.Errorf("%s (sniffed %s): blocked by %q = %v, want %v", c.name, sniffed, c.entry, got, c.want)
		}
	}
}
