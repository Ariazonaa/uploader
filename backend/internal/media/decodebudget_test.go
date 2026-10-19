package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"testing"
	"time"

	"uploader/internal/storage"
)

// pngHeaderOnly builds the PNG signature plus a valid IHDR chunk for the given
// dimensions. image.DecodeConfig only reads that far, which is exactly the point
// of a decompression bomb: a few bytes on disk, an enormous allocation on
// decode. Writing the header by hand keeps the test cheap — actually encoding a
// 30000x30000 image would cost the very memory we are guarding against.
func pngHeaderOnly(width, height uint32) []byte {
	var b bytes.Buffer
	b.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})

	ihdr := make([]byte, 0, 13)
	ihdr = binary.BigEndian.AppendUint32(ihdr, width)
	ihdr = binary.BigEndian.AppendUint32(ihdr, height)
	ihdr = append(ihdr, 8, 2, 0, 0, 0) // 8-bit depth, truecolor, no interlace

	chunk := append([]byte("IHDR"), ihdr...)
	_ = binary.Write(&b, binary.BigEndian, uint32(len(ihdr)))
	b.Write(chunk)
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}

// memStorage is an in-memory storage.Storage for the decode tests.
type memStorage struct{ blobs map[string][]byte }

func newMemStorage() *memStorage { return &memStorage{blobs: map[string][]byte{}} }

func (m *memStorage) Put(_ context.Context, key string, r io.Reader) (int64, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	m.blobs[key] = b
	return int64(len(b)), nil
}

func (m *memStorage) Open(_ context.Context, key string) (io.ReadSeekCloser, error) {
	b, ok := m.blobs[key]
	if !ok {
		return nil, io.EOF
	}
	return nopSeekCloser{bytes.NewReader(b)}, nil
}

func (m *memStorage) Stat(_ context.Context, key string) (storage.Stat, error) {
	b, ok := m.blobs[key]
	if !ok {
		return storage.Stat{}, io.EOF
	}
	return storage.Stat{Size: int64(len(b)), ModTime: time.Now()}, nil
}

func (m *memStorage) Delete(_ context.Context, key string) error {
	delete(m.blobs, key)
	return nil
}

func (m *memStorage) DeletePrefix(context.Context, string) error { return nil }

func (m *memStorage) Walk(_ context.Context, fn func(string) error) error {
	for k := range m.blobs {
		if err := fn(k); err != nil {
			return err
		}
	}
	return nil
}

type nopSeekCloser struct{ *bytes.Reader }

func (nopSeekCloser) Close() error { return nil }

func TestProcessImageRefusesOversizedImage(t *testing.T) {
	// 30000 x 30000 = 900 MPx. Decoded at 4 bytes per pixel that is ~3.6 GB of
	// resident memory from a 33-byte file — with one worker per CPU, an OOM kill.
	st := newMemStorage()
	if _, err := st.Put(context.Background(), "bomb.png", bytes.NewReader(pngHeaderOnly(30000, 30000))); err != nil {
		t.Fatalf("seed storage: %v", err)
	}

	res := processImage(context.Background(), st, "bomb.png", "thumbs/bomb.jpg")

	if res.ThumbKey != nil {
		t.Error("a thumbnail was generated for an over-budget image")
	}
	if _, ok := st.blobs["thumbs/bomb.jpg"]; ok {
		t.Error("an over-budget image was decoded and written to storage")
	}
	// The header dimensions are still worth recording for the share page.
	if res.Width == nil || res.Height == nil {
		t.Fatal("dimensions were not recorded")
	}
	if *res.Width != 30000 || *res.Height != 30000 {
		t.Errorf("dimensions = %dx%d, want 30000x30000", *res.Width, *res.Height)
	}
}

func TestProcessImageHandlesNormalImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 32))
	for x := range 64 {
		for y := range 32 {
			img.Set(x, y, color.RGBA{R: 10, G: 200, B: 40, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}

	st := newMemStorage()
	if _, err := st.Put(context.Background(), "ok.png", bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("seed storage: %v", err)
	}

	res := processImage(context.Background(), st, "ok.png", "thumbs/ok.jpg")

	if res.Width == nil || *res.Width != 64 || res.Height == nil || *res.Height != 32 {
		t.Fatal("dimensions were not extracted from a normal image")
	}
	if res.ThumbKey == nil {
		t.Fatal("no thumbnail was generated for a normal image")
	}
	if _, ok := st.blobs["thumbs/ok.jpg"]; !ok {
		t.Fatal("thumbnail was not written to storage")
	}
	if res.DominantColor == nil || *res.DominantColor == "" {
		t.Fatal("dominant color was not computed")
	}
}

// A GIF's first frame may cover only part of its logical screen. The recorded
// size and the thumbnail must be the screen's, with the frame at its offset.
func TestProcessImageGIFUsesLogicalScreen(t *testing.T) {
	red := color.RGBA{R: 255, A: 255}
	frame := image.NewPaletted(image.Rect(40, 8, 56, 24), color.Palette{color.Transparent, red})
	for i := range frame.Pix {
		frame.Pix[i] = 1
	}
	var buf bytes.Buffer
	err := gif.EncodeAll(&buf, &gif.GIF{
		Image:  []*image.Paletted{frame},
		Delay:  []int{0},
		Config: image.Config{ColorModel: frame.Palette, Width: 64, Height: 32},
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	st := newMemStorage()
	st.blobs["a.gif"] = buf.Bytes()

	res := processImage(context.Background(), st, "a.gif", "thumbs/a.jpg")
	if res.Width == nil || res.Height == nil {
		t.Fatal("dimensions were not recorded")
	}
	if *res.Width != 64 || *res.Height != 32 {
		t.Fatalf("dimensions = %dx%d, want the 64x32 logical screen", *res.Width, *res.Height)
	}
	if res.ThumbKey == nil {
		t.Fatal("no thumbnail")
	}
	thumb, err := jpeg.Decode(bytes.NewReader(st.blobs["thumbs/a.jpg"]))
	if err != nil {
		t.Fatal(err)
	}
	if b := thumb.Bounds(); b.Dx() != 64 || b.Dy() != 32 {
		t.Fatalf("thumbnail is %dx%d, want 64x32", b.Dx(), b.Dy())
	}
	if r, g, b, _ := thumb.At(48, 16).RGBA(); r>>8 < 200 || g>>8 > 60 || b>>8 > 60 {
		t.Errorf("frame area is not red in the thumbnail: %d,%d,%d", r>>8, g>>8, b>>8)
	}
	if r, _, _, _ := thumb.At(8, 16).RGBA(); r>>8 > 60 {
		t.Errorf("area outside the frame is not empty in the thumbnail: r=%d", r>>8)
	}
}

func TestLimitedWriterCapsOutput(t *testing.T) {
	var buf bytes.Buffer
	lw := &limitedWriter{w: &buf, remaining: 10}

	// The writer must claim to have consumed everything (a subprocess whose
	// stdout returns a short write dies with EPIPE) while storing only the cap.
	n, err := lw.Write(bytes.Repeat([]byte("x"), 25))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 25 {
		t.Fatalf("Write reported %d bytes, want 25", n)
	}
	if buf.Len() != 10 {
		t.Fatalf("buffered %d bytes, want the 10-byte cap", buf.Len())
	}
	if n, err := lw.Write([]byte("more")); err != nil || n != 4 {
		t.Fatalf("Write after cap = (%d, %v), want (4, nil)", n, err)
	}
	if buf.Len() != 10 {
		t.Fatalf("buffer grew past the cap to %d bytes", buf.Len())
	}
}

func TestProcessImageRefusesExtremeAspectStrip(t *testing.T) {
	// 50,000,000 x 1 is exactly at the pixel cap, and such a PNG compresses to
	// ~50 KB. The Lanczos resampler would still allocate ~100 bytes per source
	// pixel of filter weights (~5 GB), so the per-edge cap has to reject it.
	for _, dims := range [][2]uint32{{50_000_000, 1}, {1, 50_000_000}, {maxDecodeSide + 1, 100}} {
		st := newMemStorage()
		if _, err := st.Put(context.Background(), "strip.png", bytes.NewReader(pngHeaderOnly(dims[0], dims[1]))); err != nil {
			t.Fatalf("seed storage: %v", err)
		}
		res := processImage(context.Background(), st, "strip.png", "thumbs/strip.jpg")
		if res.ThumbKey != nil {
			t.Errorf("%dx%d: a thumbnail was generated", dims[0], dims[1])
		}
		if res.Width == nil || *res.Width != int(dims[0]) || res.Height == nil || *res.Height != int(dims[1]) {
			t.Errorf("%dx%d: header dimensions not recorded", dims[0], dims[1])
		}
	}
}

func TestDecodeAllowed(t *testing.T) {
	cases := []struct {
		w, h int
		want bool
	}{
		{4000, 3000, true},
		{maxDecodeSide, 0, false},
		{maxDecodeSide, 3000, true},
		{maxDecodeSide + 1, 10, false},
		{10, maxDecodeSide + 1, false},
		{50_000_000, 1, false},
		{10000, 10000, false}, // 100 MPx, over the pixel cap
		{0, 10, false},
	}
	for _, c := range cases {
		if got := decodeAllowed(c.w, c.h); got != c.want {
			t.Errorf("decodeAllowed(%d, %d) = %v, want %v", c.w, c.h, got, c.want)
		}
	}
}

func TestByteBudgetBlocksUntilReleased(t *testing.T) {
	b := newByteBudget(100)
	ctx := context.Background()
	rel1, err := b.acquire(ctx, 60)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan struct{})
	go func() {
		rel2, err := b.acquire(ctx, 60)
		if err == nil {
			rel2()
		}
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("second acquire did not wait for capacity")
	case <-time.After(50 * time.Millisecond):
	}
	rel1()
	rel1() // idempotent
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("second acquire was not woken by release")
	}
	if b.used != 0 {
		t.Errorf("budget leaked: used = %d", b.used)
	}

	// Oversized requests are clamped to the whole budget, not refused forever.
	rel, err := b.acquire(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := b.acquire(cctx, 1); err == nil {
		t.Error("acquire on a full budget ignored context cancellation")
	}
	rel()
}

// panicFormatMagic starts a fake image format whose decoder panics, standing in
// for a decoder bug hit by crafted input.
const panicFormatMagic = "PANICIMG"

func init() {
	image.RegisterFormat("panicimg", panicFormatMagic,
		func(io.Reader) (image.Image, error) { panic("decoder bug") },
		func(io.Reader) (image.Config, error) {
			return image.Config{ColorModel: color.RGBAModel, Width: 10, Height: 10}, nil
		})
}

func TestDecodeBoundedReleasesBudgetOnPanic(t *testing.T) {
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the decoder panic did not propagate")
			}
		}()
		_, _, _, _ = decodeBounded(context.Background(), bytes.NewReader([]byte(panicFormatMagic)))
	}()
	decodeBudget.mu.Lock()
	used := decodeBudget.used
	decodeBudget.mu.Unlock()
	if used != 0 {
		t.Fatalf("a panicking decode leaked %d bytes of the decode budget", used)
	}
}

func TestParseProbeOutput(t *testing.T) {
	landscape := `{"streams":[{"width":1920,"height":1080}],"format":{"duration":"12.500000"}}`
	info := parseProbeOutput([]byte(landscape))
	if info.Width != 1920 || info.Height != 1080 || info.Duration != 12500*time.Millisecond {
		t.Errorf("landscape = %+v", info)
	}
	// Phone video: coded landscape, display matrix rotates it to portrait.
	rotated := `{"streams":[{"width":1920,"height":1080,"side_data_list":[{"rotation":-90}]}],"format":{"duration":"3.0"}}`
	if info := parseProbeOutput([]byte(rotated)); info.Width != 1080 || info.Height != 1920 {
		t.Errorf("rotated = %dx%d, want 1080x1920", info.Width, info.Height)
	}
	legacy := `{"streams":[{"width":640,"height":480,"tags":{"rotate":"270"}}],"format":{}}`
	if info := parseProbeOutput([]byte(legacy)); info.Width != 480 || info.Height != 640 || info.Duration != 0 {
		t.Errorf("legacy rotate = %+v", info)
	}
	flipped := `{"streams":[{"width":640,"height":480,"side_data_list":[{"rotation":180}]}]}`
	if info := parseProbeOutput([]byte(flipped)); info.Width != 640 || info.Height != 480 {
		t.Errorf("180 degrees swapped dimensions: %+v", info)
	}
	if info := parseProbeOutput([]byte("garbage")); info != (VideoInfo{}) {
		t.Errorf("garbage = %+v", info)
	}
}
