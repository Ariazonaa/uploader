package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

// segment encodes one length-prefixed JPEG marker segment.
func segment(marker byte, payload []byte) []byte {
	b := []byte{0xFF, marker, 0, 0}
	binary.BigEndian.PutUint16(b[2:], uint16(len(payload)+2))
	return append(b, payload...)
}

// exifWithGPS builds an EXIF APP1 payload (little-endian TIFF) whose IFD0 holds
// an Orientation tag and a GPS IFD pointer; the GPS IFD holds a latitude
// reference and a marker string the test can search for.
func exifWithGPS(orientation uint16) []byte {
	le := binary.LittleEndian
	t := []byte("II*\x00")
	t = le.AppendUint32(t, 8) // IFD0 offset
	// IFD0 at 8: two entries.
	t = le.AppendUint16(t, 2)
	t = le.AppendUint16(t, exifTagOrientation)
	t = le.AppendUint16(t, 3) // SHORT
	t = le.AppendUint32(t, 1)
	t = le.AppendUint16(t, orientation)
	t = append(t, 0, 0)
	gpsIFD := uint32(8 + 2 + 2*12 + 4)
	t = le.AppendUint16(t, 0x8825) // GPSInfo pointer
	t = le.AppendUint16(t, 4)      // LONG
	t = le.AppendUint32(t, 1)
	t = le.AppendUint32(t, gpsIFD)
	t = le.AppendUint32(t, 0) // no IFD1
	// GPS IFD: one ASCII entry pointing at a string after the IFD.
	strOff := gpsIFD + 2 + 12 + 4
	secret := []byte("GPSSECRET-52.52N-13.40E\x00")
	t = le.AppendUint16(t, 1)
	t = le.AppendUint16(t, 0x0001) // GPSLatitudeRef (abused as a long string)
	t = le.AppendUint16(t, 2)      // ASCII
	t = le.AppendUint32(t, uint32(len(secret)))
	t = le.AppendUint32(t, strOff)
	t = le.AppendUint32(t, 0)
	t = append(t, secret...)
	return append([]byte("Exif\x00\x00"), t...)
}

// realJPEG encodes a small solid-colour image with the standard library.
func realJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 16))
	for x := range 32 {
		for y := range 16 {
			img.Set(x, y, color.RGBA{R: 200, G: 40, B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

var iccPayload = append([]byte("ICC_PROFILE\x00\x01\x01"), []byte("fake-display-p3-profile-bytes")...)

// withMetadata splices metadata segments in after SOI, and a trailer after EOI.
func withMetadata(t *testing.T, base []byte, extra ...[]byte) []byte {
	t.Helper()
	out := []byte{0xFF, markerSOI}
	for _, e := range extra {
		out = append(out, e...)
	}
	out = append(out, base[2:]...)
	return append(out, []byte("TRAILERSECRET")...)
}

func strip(t *testing.T, in []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := StripJPEGMetadata(&out, bytes.NewReader(in)); err != nil {
		t.Fatalf("StripJPEGMetadata: %v", err)
	}
	return out.Bytes()
}

// appSegments returns the payloads of every segment with the given marker
// before the first SOS.
func appSegments(t *testing.T, data []byte, marker byte) [][]byte {
	t.Helper()
	var out [][]byte
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			t.Fatalf("no marker at offset %d", i)
		}
		m := data[i+1]
		if m == markerSOS {
			break
		}
		n := int(binary.BigEndian.Uint16(data[i+2:]))
		if m == marker {
			out = append(out, data[i+4:i+2+n])
		}
		i += 2 + n
	}
	return out
}

func TestStripJPEGMetadataRemovesGPSKeepsOrientation(t *testing.T) {
	in := withMetadata(t, realJPEG(t),
		segment(markerAPP1, exifWithGPS(6)),
		segment(markerAPP1, []byte("http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta>XMPSECRET</x:xmpmeta>")),
		segment(0xED, []byte("Photoshop 3.0\x008BIM IPTCSECRET")),
		segment(markerCOM, []byte("COMMENTSECRET")),
	)
	out := strip(t, in)

	for _, secret := range []string{"GPSSECRET", "XMPSECRET", "IPTCSECRET", "COMMENTSECRET", "TRAILERSECRET"} {
		if bytes.Contains(out, []byte(secret)) {
			t.Errorf("%s survived the strip", secret)
		}
	}
	app1 := appSegments(t, out, markerAPP1)
	if len(app1) != 1 {
		t.Fatalf("want exactly one (rebuilt) APP1, got %d", len(app1))
	}
	if !bytes.HasPrefix(app1[0], []byte("Exif\x00\x00")) {
		t.Fatal("rebuilt APP1 is not EXIF")
	}
	if got := exifOrientation(app1[0][6:]); got != 6 {
		t.Errorf("orientation = %d, want 6", got)
	}
	// The result must still be a decodable JPEG of the same image.
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("stripped output does not decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 32 || b.Dy() != 16 {
		t.Errorf("decoded size %dx%d, want 32x16", b.Dx(), b.Dy())
	}
}

func TestStripJPEGMetadataNoOrientationSegmentForUpright(t *testing.T) {
	out := strip(t, withMetadata(t, realJPEG(t), segment(markerAPP1, exifWithGPS(1))))
	if n := len(appSegments(t, out, markerAPP1)); n != 0 {
		t.Errorf("upright image got %d APP1 segments, want 0", n)
	}
}

func TestStripJPEGMetadataKeepsICCAndAdobe(t *testing.T) {
	adobe := []byte("Adobe\x00\x64\x00\x00\x00\x00\x02")
	in := withMetadata(t, realJPEG(t),
		segment(markerAPP2, iccPayload),
		segment(markerAPP2, []byte("MPF\x00MPFSECRET")),
		segment(markerAPP14, adobe),
	)
	out := strip(t, in)

	app2 := appSegments(t, out, markerAPP2)
	if len(app2) != 1 || !bytes.Equal(app2[0], iccPayload) {
		t.Errorf("ICC profile not kept verbatim (APP2 segments: %q)", app2)
	}
	if bytes.Contains(out, []byte("MPFSECRET")) {
		t.Error("MPF APP2 was kept")
	}
	app14 := appSegments(t, out, markerAPP14)
	if len(app14) != 1 || !bytes.Equal(app14[0], adobe) {
		t.Errorf("Adobe APP14 not kept verbatim (got %q)", app14)
	}
}

func TestStripJPEGMetadataHandlesFillBytes(t *testing.T) {
	base := realJPEG(t)
	// Fill bytes (extra 0xFF) are legal before any marker. Put some before the
	// first segment after SOI and before the APP1 we want dropped.
	in := []byte{0xFF, markerSOI, 0xFF, 0xFF, 0xFF}
	in = append(in, segment(markerAPP1, exifWithGPS(3))...)
	in = append(in, 0xFF, 0xFF)
	in = append(in, base[2:]...)

	out := strip(t, in)
	if bytes.Contains(out, []byte("GPSSECRET")) {
		t.Error("GPS survived a strip with fill bytes")
	}
	if app1 := appSegments(t, out, markerAPP1); len(app1) != 1 || exifOrientation(app1[0][6:]) != 3 {
		t.Error("orientation lost with fill bytes present")
	}
	if _, err := jpeg.Decode(bytes.NewReader(out)); err != nil {
		t.Fatalf("output with fill bytes does not decode: %v", err)
	}
}

func TestStripJPEGMetadataDropsJFIFThumbnail(t *testing.T) {
	jfif := []byte("JFIF\x00\x01\x02\x00\x00\x01\x00\x01\x01\x01")
	jfif = append(jfif, 1, 2, 3) // a 1x1 RGB thumbnail
	out := strip(t, withMetadata(t, realJPEG(t), segment(markerAPP0, jfif)))
	app0 := appSegments(t, out, markerAPP0)
	if len(app0) == 0 {
		t.Fatal("JFIF header was dropped")
	}
	for _, p := range app0 {
		if len(p) != 14 || p[12] != 0 || p[13] != 0 {
			t.Errorf("JFIF thumbnail kept: %q", p)
		}
	}
}

func TestStripJPEGMetadataNonJPEG(t *testing.T) {
	var out bytes.Buffer
	if err := StripJPEGMetadata(&out, bytes.NewReader([]byte("not a jpeg at all"))); err == nil {
		t.Error("expected an error for non-JPEG input")
	}
}

func writeFile(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "in.jpg")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// malformedJPEG returns a decodable JPEG with orientation 8 and GPS data that
// the lossless strip cannot parse.
func malformedJPEG(t *testing.T) []byte {
	t.Helper()
	// Garbage between segments is a format error for the lossless strip, but
	// decoders (Go's and libjpeg) skip it — so the file still displays, with
	// its GPS data.
	base := realJPEG(t)
	in := []byte{0xFF, markerSOI}
	in = append(in, segment(markerAPP1, exifWithGPS(8))...)
	in = append(in, []byte("GARBAGE")...)
	in = append(in, base[2:]...)
	if _, err := jpeg.Decode(bytes.NewReader(in)); err != nil {
		t.Fatalf("test input should be decodable: %v", err)
	}
	return in
}

func TestSanitizeJPEGReencodesMalformedStructure(t *testing.T) {
	// It must be re-encoded, never stored as-is.
	outPath, err := SanitizeJPEG(context.Background(), writeFile(t, malformedJPEG(t)))
	if err != nil {
		t.Fatalf("SanitizeJPEG: %v", err)
	}
	defer os.Remove(outPath)
	out, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("GPSSECRET")) {
		t.Fatal("the original (with GPS) was stored")
	}
	if app1 := appSegments(t, out, markerAPP1); len(app1) != 1 || exifOrientation(app1[0][6:]) != 8 {
		t.Error("orientation not carried over into the re-encoded image")
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("re-encoded output does not decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 32 || b.Dy() != 16 {
		t.Errorf("decoded size %dx%d, want 32x16", b.Dx(), b.Dy())
	}
}

func TestSanitizeJPEGRejectsUndecodable(t *testing.T) {
	in := append([]byte{0xFF, markerSOI}, segment(markerAPP1, exifWithGPS(1))...)
	in = append(in, []byte("this is not image data")...)
	_, err := SanitizeJPEG(context.Background(), writeFile(t, in))
	if !errors.Is(err, ErrJPEGUnsanitizable) {
		t.Fatalf("err = %v, want ErrJPEGUnsanitizable", err)
	}
}

var errDiskFull = errors.New("no space left on device")

// fullDisk is a sanitize output whose writes fail like a full disk's.
type fullDisk struct{}

func (fullDisk) Write([]byte) (int, error)      { return 0, errDiskFull }
func (fullDisk) Seek(int64, int) (int64, error) { return 0, nil }
func (fullDisk) Truncate(int64) error           { return nil }

// A failing output says nothing about the JPEG: it must come back as the
// write error (worth a retry), not as a refusal of the file.
func TestSanitizeJPEGReportsWriteErrors(t *testing.T) {
	inputs := map[string][]byte{
		"lossless":  withMetadata(t, realJPEG(t), segment(markerAPP1, exifWithGPS(1))),
		"re-encode": malformedJPEG(t),
	}
	for name, in := range inputs {
		err := sanitizeJPEG(context.Background(), fullDisk{}, bytes.NewReader(in))
		if !errors.Is(err, errDiskFull) || errors.Is(err, ErrJPEGUnsanitizable) {
			t.Errorf("%s: err = %v, want the write error", name, err)
		}
	}
}

func TestSanitizeJPEGReportsCancellation(t *testing.T) {
	// The re-encode waits for decode budget; hold all of it, so it can only
	// end by the context.
	release, err := decodeBudget.acquire(context.Background(), decodeBudgetBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = SanitizeJPEG(ctx, writeFile(t, malformedJPEG(t)))
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrJPEGUnsanitizable) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestSanitizeJPEGLosslessPath(t *testing.T) {
	in := withMetadata(t, realJPEG(t), segment(markerAPP1, exifWithGPS(1)))
	outPath, err := SanitizeJPEG(context.Background(), writeFile(t, in))
	if err != nil {
		t.Fatalf("SanitizeJPEG: %v", err)
	}
	defer os.Remove(outPath)
	out, _ := os.ReadFile(outPath)
	// Lossless: the scan data is byte-identical to the input's.
	if !bytes.Equal(out, strip(t, in)) {
		t.Error("valid input did not take the lossless path")
	}
}

func TestJPEGOrientation(t *testing.T) {
	base := realJPEG(t)
	cases := map[string]struct {
		data []byte
		want int
	}{
		"rotated":   {withMetadata(t, base, segment(markerAPP1, exifWithGPS(6))), 6},
		"after XMP": {withMetadata(t, base, segment(markerAPP1, []byte("http://ns.adobe.com/xap/1.0/\x00")), segment(markerAPP1, exifWithGPS(3))), 3},
		"stripped":  {strip(t, withMetadata(t, base, segment(markerAPP1, exifWithGPS(8)))), 8},
		"none":      {base, 0},
		"not jpeg":  {[]byte("GIF89a"), 0},
	}
	for name, c := range cases {
		if got := jpegOrientation(bytes.NewReader(c.data)); got != c.want {
			t.Errorf("%s: orientation %d, want %d", name, got, c.want)
		}
	}
	// An EXIF segment after the first scan is not read by decoders either.
	late := append(bytes.TrimSuffix(base, []byte{0xFF, markerEOI}), segment(markerAPP1, exifWithGPS(6))...)
	late = append(late, 0xFF, markerEOI)
	if got := jpegOrientation(bytes.NewReader(late)); got != 0 {
		t.Errorf("EXIF after the scan: orientation %d, want 0", got)
	}
}

// Where orient puts two pixels of the first row of a 3x2 image, per the EXIF
// definition of each orientation.
func TestOrient(t *testing.T) {
	a := color.NRGBA{R: 255, A: 255}
	b := color.NRGBA{G: 255, A: 255}
	cases := []struct {
		o        int
		w, h     int
		atA, atB image.Point
	}{
		{1, 3, 2, image.Pt(0, 0), image.Pt(1, 0)},
		{2, 3, 2, image.Pt(2, 0), image.Pt(1, 0)},
		{3, 3, 2, image.Pt(2, 1), image.Pt(1, 1)},
		{4, 3, 2, image.Pt(0, 1), image.Pt(1, 1)},
		{5, 2, 3, image.Pt(0, 0), image.Pt(0, 1)},
		{6, 2, 3, image.Pt(1, 0), image.Pt(1, 1)},
		{7, 2, 3, image.Pt(1, 2), image.Pt(1, 1)},
		{8, 2, 3, image.Pt(0, 2), image.Pt(0, 1)},
	}
	for _, c := range cases {
		img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
		img.SetNRGBA(0, 0, a)
		img.SetNRGBA(1, 0, b)
		got := orient(img, c.o)
		if s := got.Bounds().Size(); s.X != c.w || s.Y != c.h {
			t.Errorf("orientation %d: %dx%d, want %dx%d", c.o, s.X, s.Y, c.w, c.h)
			continue
		}
		if got.NRGBAAt(c.atA.X, c.atA.Y) != a || got.NRGBAAt(c.atB.X, c.atB.Y) != b {
			t.Errorf("orientation %d: first-row pixels not at %v and %v", c.o, c.atA, c.atB)
		}
	}
}

// A portrait photo stored as rotated landscape: recorded size and thumbnail
// must be portrait, as browsers display it.
func TestProcessImageAppliesOrientation(t *testing.T) {
	st := newMemStorage()
	st.blobs["p.jpg"] = withMetadata(t, realJPEG(t), segment(markerAPP1, exifWithGPS(6)))

	res := processImage(context.Background(), st, "p.jpg", "thumbs/p.jpg")
	if res.Width == nil || res.Height == nil {
		t.Fatal("dimensions were not recorded")
	}
	if *res.Width != 16 || *res.Height != 32 {
		t.Fatalf("dimensions = %dx%d, want 16x32", *res.Width, *res.Height)
	}
	if res.ThumbKey == nil {
		t.Fatal("no thumbnail")
	}
	thumb, err := jpeg.Decode(bytes.NewReader(st.blobs["thumbs/p.jpg"]))
	if err != nil {
		t.Fatal(err)
	}
	if b := thumb.Bounds(); b.Dx() != 16 || b.Dy() != 32 {
		t.Errorf("thumbnail is %dx%d, want 16x32", b.Dx(), b.Dy())
	}
}

func TestIsJPEG(t *testing.T) {
	for _, m := range []string{"image/jpeg", "image/jpg", "IMAGE/JPEG"} {
		if !IsJPEG(m) {
			t.Errorf("IsJPEG(%q) = false", m)
		}
	}
	if IsJPEG("image/png") {
		t.Error("IsJPEG(image/png) = true")
	}
}
