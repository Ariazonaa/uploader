package media

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

// pngChunk frames data as a PNG chunk (length, type, data, CRC).
func pngChunk(typ string, data []byte) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
	b.WriteString(typ)
	b.Write(data)
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(typ), data...)))
	return b.Bytes()
}

// pngWith builds a PNG with the given IHDR fields, an optional tRNS chunk and
// the given (already zlib-compressed) image data.
func pngWith(w, h uint32, depth, colorType, interlace byte, trns, idat []byte) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := binary.BigEndian.AppendUint32(nil, w)
	ihdr = binary.BigEndian.AppendUint32(ihdr, h)
	ihdr = append(ihdr, depth, colorType, 0, 0, interlace)
	b.Write(pngChunk("IHDR", ihdr))
	if trns != nil {
		b.Write(pngChunk("tRNS", trns))
	}
	b.Write(pngChunk("IDAT", idat))
	b.Write(pngChunk("IEND", nil))
	return b.Bytes()
}

// grayTRNSPNG is a real, decodable 8-bit grayscale PNG with a tRNS chunk.
// image.DecodeConfig reports it as Gray (1 byte per pixel), but image/png
// decodes it to NRGBA (4).
func grayTRNSPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var raw bytes.Buffer
	for range h {
		raw.WriteByte(0) // filter: none
		raw.Write(bytes.Repeat([]byte{0x80}, w))
	}
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return pngWith(uint32(w), uint32(h), 8, 0, 0, []byte{0, 0x80}, z.Bytes()) // #nosec G115 -- test sizes
}

// decodedBytes is the size of the pixel buffers of a decoded image.
func decodedBytes(t *testing.T, img image.Image) int64 {
	t.Helper()
	switch m := img.(type) {
	case *image.Gray:
		return int64(len(m.Pix))
	case *image.RGBA:
		return int64(len(m.Pix))
	case *image.NRGBA:
		return int64(len(m.Pix))
	case *image.NRGBA64:
		return int64(len(m.Pix))
	case *image.Paletted:
		return int64(len(m.Pix))
	case *image.YCbCr:
		return int64(len(m.Y) + len(m.Cb) + len(m.Cr))
	default:
		t.Fatalf("unexpected decoded type %T", img)
		return 0
	}
}

// The estimate must cover what the decoder really allocates, in particular
// where the header's colour model understates it.
func TestEstimateDecodeBytesCoversTheDecodedImage(t *testing.T) {
	const w, h = 300, 200
	nrgba := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range nrgba.Pix {
		nrgba.Pix[i] = byte(i)
	}
	gray := image.NewGray(image.Rect(0, 0, w, h))
	encode := func(enc func(*bytes.Buffer) error) []byte {
		var b bytes.Buffer
		if err := enc(&b); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	cases := map[string][]byte{
		"png nrgba":     encode(func(b *bytes.Buffer) error { return png.Encode(b, nrgba) }),
		"png gray":      encode(func(b *bytes.Buffer) error { return png.Encode(b, gray) }),
		"png gray+tRNS": grayTRNSPNG(t, w, h),
		"jpeg color":    encode(func(b *bytes.Buffer) error { return jpeg.Encode(b, nrgba, nil) }),
		"jpeg gray":     encode(func(b *bytes.Buffer) error { return jpeg.Encode(b, gray, nil) }),
		"gif":           encode(func(b *bytes.Buffer) error { return gif.Encode(b, nrgba, nil) }),
	}
	for name, data := range cases {
		cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: DecodeConfig: %v", name, err)
		}
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: Decode: %v", name, err)
		}
		est, err := estimateDecodeBytes(bytes.NewReader(data), cfg, format)
		if err != nil {
			t.Fatalf("%s: estimate: %v", name, err)
		}
		if got := decodedBytes(t, img); est < got {
			t.Errorf("%s: estimate %d is below the decoded %T of %d bytes", name, est, img, got)
		}
	}
	// The case the colour model gets wrong, spelled out.
	if img, _, _ := image.Decode(bytes.NewReader(grayTRNSPNG(t, 4, 4))); img == nil {
		t.Fatal("gray+tRNS PNG does not decode")
	} else if _, ok := img.(*image.NRGBA); !ok {
		t.Fatalf("gray+tRNS PNG decoded to %T; the estimate assumes NRGBA", img)
	}
}

func TestPNGDecodeBytesPerFormat(t *testing.T) {
	const w, h = 1000, 1000
	cases := []struct {
		name                        string
		depth, colorType, interlace byte
		trns                        []byte
		bytesPerPixel               int64
	}{
		{"gray8", 8, 0, 0, nil, 1},
		{"gray8 tRNS", 8, 0, 0, []byte{0, 1}, 4},
		{"gray4 tRNS", 4, 0, 0, []byte{0, 1}, 4},
		{"gray16", 16, 0, 0, nil, 2},
		{"gray16 tRNS", 16, 0, 0, []byte{0, 1}, 8},
		{"rgb8", 8, 2, 0, nil, 4},
		{"rgba16", 16, 6, 0, nil, 8},
		{"rgba16 interlaced", 16, 6, 1, nil, 16},
		{"paletted", 8, 3, 0, nil, 1},
	}
	for _, c := range cases {
		data := pngWith(w, h, c.depth, c.colorType, c.interlace, c.trns, nil)
		got := scanPNG(bytes.NewReader(data)).decodeBytes(image.Config{Width: w, Height: h})
		if want := c.bytesPerPixel * w * h; got != want {
			t.Errorf("%s: %d bytes, want %d", c.name, got, want)
		}
	}
	// Anything unreadable gets the worst case.
	if got := scanPNG(bytes.NewReader([]byte("nope"))).decodeBytes(image.Config{Width: w, Height: h}); got != 16*w*h {
		t.Errorf("unreadable PNG: %d bytes, want the worst case %d", got, 16*w*h)
	}
}

func TestJPEGDecodeBytes(t *testing.T) {
	cfg := image.Config{Width: 1000, Height: 1000}
	baseline := jpegInfo{sof: true, sampling: [][2]int{{2, 2}, {1, 1}, {1, 1}}}
	if got, want := baseline.decodeBytes(cfg), int64(1008*1008*3); got != want {
		t.Errorf("baseline 4:2:0 = %d, want %d (YCbCr over whole MCUs)", got, want)
	}
	progressive := baseline
	progressive.progressive = true
	// 63x63 MCUs; per MCU 4 luma + 2 chroma blocks of 64 int32 coefficients.
	if got, want := progressive.decodeBytes(cfg), int64(1008*1008*3+63*63*6*256); got != want {
		t.Errorf("progressive 4:2:0 = %d, want %d", got, want)
	}
	cmyk := jpegInfo{sof: true, progressive: true, sampling: [][2]int{{1, 1}, {1, 1}, {1, 1}, {1, 1}}}
	if got, want := cmyk.decodeBytes(cfg), int64(1000*1000*8+125*125*4*256); got != want {
		t.Errorf("progressive CMYK = %d, want %d", got, want)
	}
}

// sosSegment is a minimal one-component SOS header followed by one byte of
// entropy-coded data.
var sosSegment = []byte{0xFF, 0xDA, 0x00, 0x08, 0x01, 0x01, 0x00, 0x00, 0x3F, 0x00, 0x00}

// jpegWithScans returns a real JPEG whose EOI is preceded by extra scans.
func jpegWithScans(t *testing.T, extra int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewGray(image.Rect(0, 0, 64, 64)), nil); err != nil {
		t.Fatal(err)
	}
	data := bytes.TrimSuffix(b.Bytes(), []byte{0xFF, 0xD9})
	for range extra {
		data = append(data, sosSegment...)
	}
	return append(data, 0xFF, 0xD9)
}

func TestScanJPEG(t *testing.T) {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 64, 64)), nil); err != nil {
		t.Fatal(err)
	}
	info, err := scanJPEG(bytes.NewReader(b.Bytes()))
	if err != nil || !info.sof || info.progressive || info.scans != 1 || len(info.sampling) != 3 || info.sampling[0] != [2]int{2, 2} {
		t.Fatalf("encoder output: %+v, %v", info, err)
	}

	if info, err := scanJPEG(bytes.NewReader(jpegWithScans(t, maxJPEGScans-1))); err != nil || info.scans != maxJPEGScans {
		t.Fatalf("%d scans: %+v, %v; want accepted", maxJPEGScans, info, err)
	}
	if _, err := scanJPEG(bytes.NewReader(jpegWithScans(t, maxJPEGScans))); !errors.Is(err, errTooManyScans) {
		t.Fatalf("%d scans: err = %v, want errTooManyScans", maxJPEGScans+1, err)
	}

	// SOS markers after EOI are not decoded by image/jpeg and are not counted.
	trailer := append(b.Bytes(), bytes.Repeat(sosSegment, 2*maxJPEGScans)...)
	if info, err := scanJPEG(bytes.NewReader(trailer)); err != nil || info.scans != 1 {
		t.Fatalf("trailing data after EOI: %+v, %v", info, err)
	}
}

// A JPEG with too many scans is refused before any pixel is decoded; the
// header dimensions are still recorded.
func TestProcessImageRefusesTooManyJPEGScans(t *testing.T) {
	data := jpegWithScans(t, 10*maxJPEGScans)
	if _, _, _, err := decodeBounded(context.Background(), bytes.NewReader(data)); !errors.Is(err, errDecodeRefused) {
		t.Fatalf("decodeBounded err = %v, want errDecodeRefused", err)
	}
	st := newMemStorage()
	if _, err := st.Put(context.Background(), "scans.jpg", bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	res := processImage(context.Background(), st, "scans.jpg", "thumbs/scans.jpg")
	if res.ThumbKey != nil {
		t.Error("a thumbnail was generated")
	}
	if res.Width == nil || *res.Width != 64 {
		t.Error("header dimensions were not recorded")
	}
}

// An image whose estimate alone exceeds the whole budget is refused instead
// of waiting to run alone: 50 MPx of interlaced 16-bit RGBA is ~800 MB.
func TestDecodeBoundedRefusesOverBudgetImage(t *testing.T) {
	data := pngWith(7071, 7071, 16, 6, 1, nil, nil)
	_, cfg, _, err := decodeBounded(context.Background(), bytes.NewReader(data))
	if !errors.Is(err, errDecodeRefused) {
		t.Fatalf("err = %v, want errDecodeRefused", err)
	}
	if cfg.Width != 7071 {
		t.Fatalf("config not returned: %+v", cfg)
	}
	// The same image without interlacing fits.
	need, err := estimateDecodeBytes(bytes.NewReader(pngWith(7071, 7071, 16, 6, 0, nil, nil)),
		image.Config{ColorModel: color.NRGBA64Model, Width: 7071, Height: 7071}, "png")
	if err != nil || need > decodeBudgetBytes {
		t.Fatalf("non-interlaced estimate = %d, %v; want within the budget", need, err)
	}
}
