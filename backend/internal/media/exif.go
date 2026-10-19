package media

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"os"
	"strings"
)

// JPEG marker bytes (the byte following 0xFF).
const (
	markerTEM   = 0x01
	markerRST0  = 0xD0
	markerRST7  = 0xD7
	markerSOI   = 0xD8
	markerEOI   = 0xD9
	markerSOS   = 0xDA
	markerAPP0  = 0xE0
	markerAPP1  = 0xE1
	markerAPP2  = 0xE2
	markerAPP14 = 0xEE
	markerAPP15 = 0xEF
	markerCOM   = 0xFE

	// exifTagOrientation is the TIFF tag of the EXIF Orientation field.
	exifTagOrientation = 0x0112
	// reencodeQuality is used when a JPEG has to be re-encoded because its
	// structure could not be stripped losslessly.
	reencodeQuality = 92
)

// ErrJPEGUnsanitizable is returned when a JPEG's metadata can neither be
// stripped losslessly nor removed by decoding and re-encoding the image. The
// upload must then be rejected: storing the original would publish exactly the
// metadata (GPS position, camera serial) the strip exists to remove.
var ErrJPEGUnsanitizable = errors.New("jpeg metadata could not be removed")

var errJPEGStructure = errors.New("jpeg: unexpected structure")

// SanitizeJPEG writes a metadata-free copy of the JPEG at srcPath to a new
// temporary file and returns its path; the caller owns (and removes) it.
//
// The fast path is a streaming, lossless strip (StripJPEGMetadata) that never
// holds the file in memory. If the file's structure cannot be parsed, the image
// is decoded — under the same caps and decode budget as thumbnailing — and
// re-encoded, keeping only its orientation. If that fails too, the result is
// ErrJPEGUnsanitizable; the original is never passed through. Failures that say
// nothing about the file (reading or writing it, the context ending) are
// returned as they are, so callers can tell them from a refusal.
func SanitizeJPEG(ctx context.Context, srcPath string) (string, error) {
	out, err := os.CreateTemp("", "uploader-*")
	if err != nil {
		return "", err
	}
	outPath := out.Name()
	fail := func(err error) (string, error) {
		_ = out.Close()
		_ = os.Remove(outPath)
		return "", err
	}

	src, err := os.Open(srcPath)
	if err != nil {
		return fail(err)
	}
	defer src.Close()

	if err := sanitizeJPEG(ctx, out, src); err != nil {
		return fail(err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(outPath)
		return "", err
	}
	return outPath, nil
}

// rewritable is the output of sanitizeJPEG, which starts over on it when the
// lossless strip fails.
type rewritable interface {
	io.Writer
	io.Seeker
	Truncate(size int64) error
}

// sanitizeJPEG implements SanitizeJPEG on an open source and output.
func sanitizeJPEG(ctx context.Context, out rewritable, src io.ReadSeeker) error {
	// The strip and the decoder report a source or output that fails as a
	// broken JPEG, or wrap the error into one. So failures of the files are
	// recorded on the way through instead: a full disk must not turn into a
	// refusal of the upload as unsanitizable.
	in, dst := &errReader{r: src}, &errWriter{w: out}
	ioErr := func() error { return cmp.Or(in.err, dst.err) }

	orientation, stripErr := stripJPEG(dst, in)
	if stripErr == nil {
		return nil
	}
	if err := ioErr(); err != nil {
		return err
	}

	// Lossless strip failed: start over and re-encode from the decoded pixels.
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := out.Truncate(0); err != nil {
		return err
	}
	err := reencodeJPEG(ctx, dst, in, orientation)
	switch {
	case err == nil:
		return nil
	case ioErr() != nil:
		return ioErr()
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err // waiting for the decode budget was cut short
	}
	return fmt.Errorf("%w: %v (strip: %v)", ErrJPEGUnsanitizable, err, stripErr)
}

// errReader remembers the first error of r other than io.EOF.
type errReader struct {
	r   io.ReadSeeker
	err error
}

func (e *errReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) && e.err == nil {
		e.err = err
	}
	return n, err
}

func (e *errReader) Seek(offset int64, whence int) (int64, error) {
	n, err := e.r.Seek(offset, whence)
	if err != nil && e.err == nil {
		e.err = err
	}
	return n, err
}

// errWriter remembers the first error of w.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) Write(p []byte) (int, error) {
	n, err := e.w.Write(p)
	if err != nil && e.err == nil {
		e.err = err
	}
	return n, err
}

// StripJPEGMetadata copies the JPEG in src to dst without its metadata and
// without recompressing the image data — privacy without quality loss. It
// streams: memory use is independent of the file size.
//
// Kept: the JFIF APP0 header (minus its embedded thumbnail), APP2 ICC colour
// profiles, the APP14 Adobe segment (needed to decode CMYK/YCCK colours
// correctly), every non-metadata segment, and — rebuilt as a minimal EXIF
// block holding only that one tag — the EXIF Orientation, so portrait photos
// still display upright. Dropped: all other EXIF (GPS, camera serial, the EXIF
// thumbnail), XMP, IPTC/Photoshop, MPF, comments, any other APPn, and anything
// after the end-of-image marker (MPF secondary images, vendor trailers).
//
// On error dst may hold partial output and must be discarded.
func StripJPEGMetadata(dst io.Writer, src io.Reader) error {
	_, err := stripJPEG(dst, src)
	return err
}

// stripJPEG implements StripJPEGMetadata. It also returns the EXIF orientation
// (0 = none seen), including when it fails part-way, so a re-encode fallback
// can keep it.
func stripJPEG(dst io.Writer, src io.Reader) (int, error) {
	br := bufio.NewReaderSize(src, 64<<10)
	bw := bufio.NewWriterSize(dst, 64<<10)

	var soi [2]byte
	if _, err := io.ReadFull(br, soi[:]); err != nil || soi[0] != 0xFF || soi[1] != markerSOI {
		return 0, errJPEGStructure
	}
	_, _ = bw.Write(soi[:])

	orientation := 0
	wroteOrientation := false
	payload := make([]byte, 0xFFFF) // reused; a segment payload is < 64 KiB
	pending := -1                   // marker already consumed by the entropy copier

	for {
		var marker byte
		if pending >= 0 {
			marker, pending = byte(pending), -1
		} else {
			m, err := nextMarker(br)
			if err != nil {
				return orientation, err
			}
			marker = m
		}

		switch {
		case marker == markerEOI:
			// Anything after EOI is not part of this image — MPF secondary images
			// carry their own full EXIF — so it is deliberately not copied.
			_, _ = bw.Write([]byte{0xFF, markerEOI})
			return orientation, bw.Flush()
		case marker == markerTEM || (marker >= markerRST0 && marker <= markerRST7):
			_, _ = bw.Write([]byte{0xFF, marker}) // standalone, no payload
			continue
		case marker == markerSOI || marker == 0x00:
			return orientation, errJPEGStructure
		}

		var lenBuf [2]byte
		if _, err := io.ReadFull(br, lenBuf[:]); err != nil {
			return orientation, errJPEGStructure
		}
		segLen := int(binary.BigEndian.Uint16(lenBuf[:]))
		if segLen < 2 {
			return orientation, errJPEGStructure
		}
		p := payload[:segLen-2]
		if _, err := io.ReadFull(br, p); err != nil {
			return orientation, errJPEGStructure
		}

		switch {
		case marker == markerAPP0:
			// Keep the JFIF header but not its optional embedded thumbnail — a
			// thumbnail can show what a crop removed. JFXX and other APP0 go.
			if len(p) >= 14 && bytes.HasPrefix(p, []byte("JFIF\x00")) {
				jfif := append([]byte(nil), p[:14]...)
				jfif[12], jfif[13] = 0, 0 // thumbnail width/height
				writeSegment(bw, markerAPP0, jfif)
			}
		case marker == markerAPP1:
			if bytes.HasPrefix(p, []byte("Exif\x00\x00")) && orientation == 0 {
				orientation = exifOrientation(p[6:])
				if orientation > 1 && !wroteOrientation {
					writeSegment(bw, markerAPP1, minimalExif(orientation))
					wroteOrientation = true
				}
			}
			// The original APP1 (EXIF or XMP) is always dropped.
		case marker == markerAPP2:
			if bytes.HasPrefix(p, []byte("ICC_PROFILE\x00")) {
				writeSegment(bw, marker, p)
			}
		case marker == markerAPP14:
			if bytes.HasPrefix(p, []byte("Adobe")) {
				writeSegment(bw, marker, p)
			}
		case (marker > markerAPP0 && marker <= markerAPP15) || marker == markerCOM:
			// Remaining APPn and comments: metadata, dropped.
		default:
			writeSegment(bw, marker, p)
		}

		if marker == markerSOS {
			next, err := copyEntropyData(bw, br)
			if errors.Is(err, io.EOF) {
				// Truncated after the scan started (a camera or transfer cut the
				// file short). Terminate it properly; there is nothing left that
				// could hold metadata.
				_, _ = bw.Write([]byte{0xFF, markerEOI})
				return orientation, bw.Flush()
			}
			if err != nil {
				return orientation, err
			}
			pending = int(next)
		}
	}
}

// nextMarker reads the next marker between segments. Any number of 0xFF fill
// bytes may precede a marker (JPEG B.1.1.2); anything else is a format error.
func nextMarker(br *bufio.Reader) (byte, error) {
	b, err := br.ReadByte()
	if err != nil || b != 0xFF {
		return 0, errJPEGStructure
	}
	for b == 0xFF {
		if b, err = br.ReadByte(); err != nil {
			return 0, errJPEGStructure
		}
	}
	return b, nil
}

// copyEntropyData copies entropy-coded scan data from br to bw until it meets a
// real marker, which it consumes and returns. Byte stuffing (FF 00), restart
// markers (FF D0-D7) and fill bytes are part of the scan. Returns io.EOF when
// the input ends inside the scan.
func copyEntropyData(bw *bufio.Writer, br *bufio.Reader) (byte, error) {
	for {
		chunk, err := br.ReadSlice(0xFF)
		if errors.Is(err, bufio.ErrBufferFull) {
			_, _ = bw.Write(chunk)
			continue
		}
		if err != nil {
			_, _ = bw.Write(chunk)
			return 0, err
		}
		_, _ = bw.Write(chunk[:len(chunk)-1]) // everything before the 0xFF

		b, err := br.ReadByte()
		for err == nil && b == 0xFF {
			b, err = br.ReadByte() // fill bytes collapse into one 0xFF
		}
		if err != nil {
			return 0, err
		}
		if b == 0x00 || (b >= markerRST0 && b <= markerRST7) {
			_, _ = bw.Write([]byte{0xFF, b})
			continue
		}
		return b, nil
	}
}

func writeSegment(bw *bufio.Writer, marker byte, payload []byte) {
	var hdr [4]byte
	hdr[0], hdr[1] = 0xFF, marker
	binary.BigEndian.PutUint16(hdr[2:], uint16(len(payload)+2)) // #nosec G115 -- payload < 64 KiB
	_, _ = bw.Write(hdr[:])
	_, _ = bw.Write(payload)
}

// exifOrientation returns the Orientation tag (1-8) from IFD0 of a TIFF
// structure (an EXIF APP1 payload after its "Exif\0\0" prefix), or 0.
func exifOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 0
	}
	var bo binary.ByteOrder
	switch string(tiff[:4]) {
	case "II*\x00":
		bo = binary.LittleEndian
	case "MM\x00*":
		bo = binary.BigEndian
	default:
		return 0
	}
	off := int64(bo.Uint32(tiff[4:8]))
	if off < 8 || off+2 > int64(len(tiff)) {
		return 0
	}
	count := int64(bo.Uint16(tiff[off:]))
	for i := int64(0); i < count; i++ {
		e := off + 2 + 12*i
		if e+12 > int64(len(tiff)) {
			return 0
		}
		if bo.Uint16(tiff[e:]) != exifTagOrientation {
			continue
		}
		typ, n := bo.Uint16(tiff[e+2:]), bo.Uint32(tiff[e+4:])
		if typ != 3 || n < 1 { // SHORT
			return 0
		}
		if v := int(bo.Uint16(tiff[e+8:])); v >= 1 && v <= 8 {
			return v
		}
		return 0
	}
	return 0
}

// jpegOrientation returns the EXIF orientation (1-8) of the JPEG in r, or 0
// when there is none or r is not a JPEG whose header it can follow. Like
// stripJPEG it takes the first EXIF segment that holds a valid one, but stops at
// the first scan: decoders, browsers included, read metadata no further.
func jpegOrientation(r io.Reader) int {
	br := bufio.NewReader(r)
	var soi [2]byte
	if _, err := io.ReadFull(br, soi[:]); err != nil || soi != [2]byte{0xFF, markerSOI} {
		return 0
	}
	for {
		marker, err := nextMarker(br)
		if err != nil {
			return 0
		}
		switch {
		case marker == markerSOS || marker == markerEOI:
			return 0
		case marker == markerTEM || (marker >= markerRST0 && marker <= markerRST7):
			continue
		}
		var lenBuf [2]byte
		if _, err := io.ReadFull(br, lenBuf[:]); err != nil {
			return 0
		}
		n := int(binary.BigEndian.Uint16(lenBuf[:])) - 2
		if n < 0 {
			return 0
		}
		if marker != markerAPP1 {
			if _, err := br.Discard(n); err != nil {
				return 0
			}
			continue
		}
		p := make([]byte, n)
		if _, err := io.ReadFull(br, p); err != nil {
			return 0
		}
		if bytes.HasPrefix(p, []byte("Exif\x00\x00")) {
			if o := exifOrientation(p[6:]); o != 0 {
				return o
			}
		}
	}
}

// minimalExif builds an EXIF APP1 payload holding only the Orientation tag.
func minimalExif(orientation int) []byte {
	b := make([]byte, 0, 32)
	b = append(b, "Exif\x00\x00"...)
	b = append(b, "MM\x00*"...)
	b = binary.BigEndian.AppendUint32(b, 8) // IFD0 right after the header
	b = binary.BigEndian.AppendUint16(b, 1) // one entry
	b = binary.BigEndian.AppendUint16(b, exifTagOrientation)
	b = binary.BigEndian.AppendUint16(b, 3)                   // SHORT
	b = binary.BigEndian.AppendUint32(b, 1)                   // count
	b = binary.BigEndian.AppendUint16(b, uint16(orientation)) // #nosec G115 -- 1..8
	b = append(b, 0, 0)                                       // value padding
	b = binary.BigEndian.AppendUint32(b, 0)                   // no next IFD
	return b
}

// reencodeJPEG decodes src (within the decode caps and budget) and writes it
// to dst as a fresh JPEG carrying no metadata except the given orientation.
func reencodeJPEG(ctx context.Context, dst io.Writer, src io.ReadSeeker, orientation int) error {
	img, _, release, err := decodeBounded(ctx, src)
	if err != nil {
		return err
	}
	defer release()

	bw := bufio.NewWriterSize(dst, 64<<10)
	_, _ = bw.Write([]byte{0xFF, markerSOI})
	if orientation > 1 {
		writeSegment(bw, markerAPP1, minimalExif(orientation))
	}
	// jpeg.Encode starts with its own SOI; skip it so the orientation segment
	// sits right after ours.
	if err := jpeg.Encode(&skipWriter{w: bw, skip: 2}, img, &jpeg.Options{Quality: reencodeQuality}); err != nil {
		return err
	}
	return bw.Flush()
}

// skipWriter drops the first skip bytes written to it.
type skipWriter struct {
	w    io.Writer
	skip int
}

func (s *skipWriter) Write(p []byte) (int, error) {
	n := len(p)
	if s.skip > 0 {
		k := min(s.skip, len(p))
		s.skip -= k
		p = p[k:]
	}
	if len(p) == 0 {
		return n, nil
	}
	if _, err := s.w.Write(p); err != nil {
		return 0, err
	}
	return n, nil
}

// IsJPEG reports whether the sniffed MIME type is JPEG.
func IsJPEG(mime string) bool {
	return strings.EqualFold(mime, "image/jpeg") || strings.EqualFold(mime, "image/jpg")
}
