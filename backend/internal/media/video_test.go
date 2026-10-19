package media

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uploader/internal/storage"
)

func TestPixFmtBytes(t *testing.T) {
	for pf, want := range map[string]float64{
		"yuv420p":     1.5,
		"yuvj420p":    1.5,
		"nv12":        1.5,
		"yuv420p10le": 3,
		"p010le":      3,
		"yuv422p":     2,
		"yuv444p":     3,
		"yuv444p12be": 6,
		"yuva420p":    2.5,
		"rgb24":       3,
		"rgba":        4,
		"rgb48le":     6,
		"gray":        1,
		"gray16le":    2,
		"":            8,
		"weird_fmt":   8,
	} {
		if got := pixFmtBytes(pf); got != want {
			t.Errorf("pixFmtBytes(%q) = %v, want %v", pf, got, want)
		}
	}
}

// A crafted stream just under the pixel cap that declares the maximum number
// of reference frames is refused before ffmpeg runs; ordinary videos are not,
// and an unknown size is charged the whole budget so it runs alone.
func TestVideoDecodeEstimates(t *testing.T) {
	bomb := VideoInfo{Width: 8000, Height: 6000, Refs: 16, PixFmt: "yuv420p"}
	if !decodeAllowed(bomb.Width, bomb.Height) {
		t.Fatal("setup: the bomb must pass the per-frame caps")
	}
	if got := hlsDecodeBytes(bomb, 4); got <= decodeBudgetBytes {
		t.Errorf("hls estimate for %+v = %d MiB, want over the %d MiB budget", bomb, got>>20, decodeBudgetBytes>>20)
	}
	if got := thumbDecodeBytes(bomb); got <= decodeBudgetBytes {
		t.Errorf("thumbnail estimate for %+v = %d MiB, want over the budget", bomb, got>>20)
	}

	for _, v := range []VideoInfo{
		{Width: 1920, Height: 1080, Refs: 4, PixFmt: "yuv420p"},
		{Width: 3840, Height: 2160, Refs: 4, PixFmt: "yuv420p10le"},
		{Width: 1080, Height: 1920}, // refs and format unknown
	} {
		if got := hlsDecodeBytes(v, 8); got > decodeBudgetBytes || got < minHLSBytes {
			t.Errorf("hls estimate for %+v = %d MiB, want within [%d, %d] MiB", v, got>>20, minHLSBytes>>20, decodeBudgetBytes>>20)
		}
		if got := thumbDecodeBytes(v); got > decodeBudgetBytes/2 || got < minVideoDecodeBytes {
			t.Errorf("thumbnail estimate for %+v = %d MiB", v, got>>20)
		}
	}

	if got := hlsDecodeBytes(VideoInfo{}, 8); got != decodeBudgetBytes {
		t.Errorf("unknown size: hls charged %d, want the whole budget", got)
	}
	if got := thumbDecodeBytes(VideoInfo{}); got != decodeBudgetBytes {
		t.Errorf("unknown size: thumbnail charged %d, want the whole budget", got)
	}
}

// On the file storage ffmpeg reads the blob in place; elsewhere it gets a
// temporary copy that cleanup removes.
func TestInputPath(t *testing.T) {
	root := t.TempDir()
	fs, err := storage.NewFileStorage(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Put(context.Background(), "ab/cd/v.mp4", strings.NewReader("video")); err != nil {
		t.Fatal(err)
	}
	p, cleanup, err := inputPath(context.Background(), fs, "ab/cd/v.mp4")
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if want := filepath.Join(root, "ab", "cd", "v.mp4"); p != want {
		t.Fatalf("path = %s, want the blob itself (%s)", p, want)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("cleanup removed the blob itself")
	}

	mem := newMemStorage()
	mem.blobs["v.mp4"] = []byte("video")
	p, cleanup, err = inputPath(context.Background(), mem, "v.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != "video" {
		t.Fatalf("temp copy = %q, %v", b, err)
	}
	cleanup()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("cleanup left the temp copy behind")
	}
}
