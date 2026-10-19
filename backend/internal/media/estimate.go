package media

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
)

// maxJPEGScans caps the scans (SOS segments) a JPEG may contain before it is
// decoded. image/jpeg revisits every block of a scan's components once per
// scan and puts no limit on how many scans there are. A progressive scan can
// be a few bytes (one end-of-band run covers thousands of blocks), so a JPEG of
// a few megabytes with tens of thousands of scans kept a media worker busy for
// minutes to hours. Real encoders write a dozen at most; libjpeg's default
// progressive script has 10.
const maxJPEGScans = 64

var errTooManyScans = fmt.Errorf("%w: more than %d JPEG scans", errDecodeRefused, maxJPEGScans)

// estimateDecodeBytes is a conservative estimate of the peak memory a full
// decode of the image in r (positioned at its start) needs, used to charge the
// decode budget. The colour model from image.DecodeConfig is not enough: it
// does not tell a PNG with a tRNS chunk (decoded to NRGBA) from one without,
// nor a progressive JPEG (which keeps every DCT coefficient as an int32 until
// the last scan) from a baseline one. So the format's header is read here,
// and for JPEG every marker up to EOI, which is also where too many scans are
// refused.
func estimateDecodeBytes(r io.Reader, cfg image.Config, format string) (int64, error) {
	var n int64
	switch format {
	case "jpeg":
		info, err := scanJPEG(r)
		if err != nil {
			return 0, err
		}
		n = info.decodeBytes(cfg)
	case "png":
		n = scanPNG(r).decodeBytes(cfg)
	case "webp":
		// Lossless (VP8L) decodes to NRGBA and its inverse transforms may hold a
		// second copy; a VP8X header reports YCbCr even then. 8 bytes per pixel
		// covers every variant.
		n = pixels(cfg) * 8
	case "gif":
		// The first frame (paletted, at most the logical screen) plus the NRGBA
		// canvas onCanvas puts it on when it covers only part of the screen.
		n = pixels(cfg) * (1 + 4)
	default:
		n = pixels(cfg) * colorModelBytes(cfg.ColorModel)
	}
	// Thumbnailing resizes horizontally first: an NRGBA intermediate of
	// min(width, thumbMaxEdge) x height. Filter weights and the encoder's
	// buffers fit in the fixed allowance.
	return n + 4*int64(min(cfg.Width, thumbMaxEdge))*int64(cfg.Height) + 8<<20, nil
}

func pixels(cfg image.Config) int64 { return int64(cfg.Width) * int64(cfg.Height) }

// colorModelBytes is the bytes per pixel of an image decoded in model m.
func colorModelBytes(m color.Model) int64 {
	// The palette check comes first: color.Palette is a slice, and keeping it
	// out of the == comparisons below keeps them trivially safe.
	if _, ok := m.(color.Palette); ok {
		return 1
	}
	switch m {
	case color.GrayModel, color.AlphaModel:
		return 1
	case color.Gray16Model, color.Alpha16Model:
		return 2
	case color.YCbCrModel:
		return 3
	case color.RGBAModel, color.NRGBAModel, color.CMYKModel, color.NYCbCrAModel:
		return 4
	default:
		return 8 // RGBA64/NRGBA64 and anything unrecognised
	}
}

// jpegInfo is what scanJPEG learns from a JPEG's markers.
type jpegInfo struct {
	sof         bool
	progressive bool
	sampling    [][2]int // per component: horizontal, vertical sampling factor
	scans       int
}

// scanJPEG walks the markers of a JPEG up to EOI, counting scans and reading
// the frame header. Like image/jpeg it skips stray bytes between segments, and
// the entropy-coded data after each SOS is skipped byte-wise (0xFF00 stuffing
// and RSTn markers carry no length), so no SOS image/jpeg would process can go
// uncounted. A truncated file ends the walk without an error; the decoder
// fails on it anyway.
func scanJPEG(r io.Reader) (jpegInfo, error) {
	var info jpegInfo
	br := bufio.NewReaderSize(r, 64<<10)
	var soi [2]byte
	if _, err := io.ReadFull(br, soi[:]); err != nil || soi != [2]byte{0xFF, 0xD8} {
		return info, errors.New("jpeg: missing SOI marker")
	}
	for {
		if _, err := br.ReadSlice(0xFF); err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			return info, eofIsEnd(err)
		}
		m, err := br.ReadByte()
		for err == nil && m == 0xFF { // fill bytes
			m, err = br.ReadByte()
		}
		if err != nil {
			return info, eofIsEnd(err)
		}
		switch {
		case m == 0x00, m == 0x01, m == 0xD8, m >= 0xD0 && m <= 0xD7:
			continue // stuffed 0xFF, TEM, stray SOI, RSTn: no payload
		case m == 0xD9:
			return info, nil // EOI: image/jpeg stops here too
		}
		var lb [2]byte
		if _, err := io.ReadFull(br, lb[:]); err != nil {
			return info, eofIsEnd(err)
		}
		n := int(binary.BigEndian.Uint16(lb[:])) - 2
		if n < 0 {
			return info, errors.New("jpeg: bad segment length")
		}
		switch {
		case (m == 0xC0 || m == 0xC1 || m == 0xC2) && !info.sof:
			seg := make([]byte, n)
			if _, err := io.ReadFull(br, seg); err != nil {
				return info, eofIsEnd(err)
			}
			info.parseSOF(seg, m == 0xC2)
			continue
		case m == 0xDA:
			info.scans++
			if info.scans > maxJPEGScans {
				return info, errTooManyScans
			}
		}
		if _, err := br.Discard(n); err != nil {
			return info, eofIsEnd(err)
		}
	}
}

func (info *jpegInfo) parseSOF(seg []byte, progressive bool) {
	// precision(1) height(2) width(2) components(1), then 3 bytes each.
	if len(seg) < 6 {
		return
	}
	nc := int(seg[5])
	if nc < 1 || len(seg) < 6+3*nc {
		return
	}
	info.sof = true
	info.progressive = progressive
	if nc == 1 {
		// image/jpeg treats a single component as 1x1 whatever the header says.
		info.sampling = [][2]int{{1, 1}}
		return
	}
	for i := range nc {
		hv := seg[6+3*i+1]
		info.sampling = append(info.sampling, [2]int{int(hv >> 4), int(hv & 0x0F)})
	}
}

// decodeBytes estimates image/jpeg's peak allocation for the image.
func (info jpegInfo) decodeBytes(cfg image.Config) int64 {
	if !info.sof {
		// Should not happen (DecodeConfig found a frame header); assume the
		// worst case: progressive CMYK, 4x4 bytes of coefficients plus 8 of output.
		return pixels(cfg) * 24
	}
	hmax, vmax := 1, 1
	for _, s := range info.sampling {
		hmax, vmax = max(hmax, s[0]), max(vmax, s[1])
	}
	// image/jpeg works in whole MCUs.
	mxx := int64((cfg.Width + 8*hmax - 1) / (8 * hmax))
	mxy := int64((cfg.Height + 8*vmax - 1) / (8 * vmax))
	area := mxx * 8 * int64(hmax) * mxy * 8 * int64(vmax)

	var n int64
	switch len(info.sampling) {
	case 1:
		n = area // image.Gray
	case 3:
		n = area * 3 // image.YCbCr, counted as 4:4:4
	default:
		// CMYK/YCCK: a YCbCr image plus a separate K plane, then converted
		// into a fresh 4-byte-per-pixel image.CMYK.
		n = area * 8
	}
	if info.progressive {
		// One [64]int32 block per 8x8 block of every component, kept until the
		// last scan.
		for _, s := range info.sampling {
			n += mxx * mxy * int64(s[0]*s[1]) * 64 * 4
		}
	}
	return n
}

// pngInfo is what scanPNG learns from the chunks before the image data.
type pngInfo struct {
	ok         bool
	depth      byte
	colorType  byte
	interlaced bool
	trns       bool
}

// scanPNG reads the IHDR and the chunks up to the first IDAT. Any problem
// leaves ok false, which decodeBytes answers with its worst case.
func scanPNG(r io.Reader) pngInfo {
	var info pngInfo
	br := bufio.NewReader(r)
	var sig [8]byte
	if _, err := io.ReadFull(br, sig[:]); err != nil || string(sig[:]) != "\x89PNG\r\n\x1a\n" {
		return info
	}
	for first := true; ; first = false {
		var hdr [8]byte
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			return info
		}
		length := int(binary.BigEndian.Uint32(hdr[:4]))
		typ := string(hdr[4:])
		switch {
		case first && typ == "IHDR" && length == 13:
			var ihdr [13]byte
			if _, err := io.ReadFull(br, ihdr[:]); err != nil {
				return info
			}
			info.ok = true
			info.depth, info.colorType, info.interlaced = ihdr[8], ihdr[9], ihdr[12] == 1
			length = 0
		case first:
			return info // IHDR must come first
		case typ == "tRNS":
			info.trns = true
		case typ == "IDAT", typ == "IEND":
			return info
		}
		if _, err := br.Discard(length + 4); err != nil { // payload + CRC
			return info
		}
	}
}

// decodeBytes estimates image/png's peak allocation for the image.
func (info pngInfo) decodeBytes(cfg image.Config) int64 {
	bpp := int64(8)
	if info.ok {
		wide := info.depth == 16
		switch info.colorType {
		case 0: // grayscale; with tRNS it decodes to NRGBA(64), not Gray(16)
			switch {
			case info.trns && wide:
				bpp = 8
			case info.trns:
				bpp = 4
			case wide:
				bpp = 2
			default:
				bpp = 1
			}
		case 3: // paletted
			bpp = 1
		case 2, 4, 6: // truecolor, gray+alpha, truecolor+alpha
			if wide {
				bpp = 8
			} else {
				bpp = 4
			}
		}
	}
	n := pixels(cfg) * bpp
	if !info.ok || info.interlaced {
		// Adam7: every pass is decoded into its own image and merged into the
		// full-size one, so up to two copies are live.
		n *= 2
	}
	return n
}

func eofIsEnd(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return nil
	}
	return err
}
