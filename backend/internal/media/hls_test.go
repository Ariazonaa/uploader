package media

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHLSMaxDurationFitsBudget(t *testing.T) {
	d := hlsMaxDuration()
	if d < 30*time.Minute || d > 2*time.Hour {
		t.Fatalf("hlsMaxDuration = %v, outside any sensible range", d)
	}
	// At the capped bitrates, the longest allowed source must fit the budget.
	bytes := d.Seconds() * float64(hlsVideoMaxrateKbps+hlsAudioKbps) * 1000 / 8
	if bytes > maxHLSOutputBytes {
		t.Errorf("a %v rendition (~%.0f bytes) exceeds maxHLSOutputBytes", d, bytes)
	}
}

func TestHLSArgsBoundOutput(t *testing.T) {
	args := hlsArgs("/tmp/in", "/tmp/out", time.Hour, 2)
	joined := strings.Join(args, " ")
	for _, want := range []string{"-maxrate 5000k", "-bufsize 10000k", "-t 3600", "-b:a 128k", "min(1920,iw)", "-pix_fmt yuv420p"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ffmpeg args lack %q: %s", want, joined)
		}
	}
	// -fs is a no-op for the HLS muxer; it must not pose as a size bound.
	if slices.Contains(args, "-fs") {
		t.Error("ffmpeg args still carry the ineffective -fs")
	}
	// -t must be an output option (after -i) so it caps what is written.
	if slices.Index(args, "-t") < slices.Index(args, "-i") {
		t.Error("-t is placed before -i")
	}
	assertDecoderBounded(t, args)
}

// assertDecoderBounded checks that an ffmpeg command line caps the decoder's
// frame size and thread count: both are input options and must precede -i.
func assertDecoderBounded(t *testing.T, args []string) {
	t.Helper()
	in := slices.Index(args, "-i")
	for _, opt := range []string{"-max_pixels", "-threads"} {
		i := slices.Index(args, opt)
		if i < 0 || i > in {
			t.Errorf("%s is missing before -i: %v", opt, args)
		}
	}
	if i := slices.Index(args, "-max_pixels"); i >= 0 && (i+1 >= len(args) || args[i+1] != strconv.Itoa(maxDecodePixels)) {
		t.Errorf("-max_pixels is not maxDecodePixels: %v", args)
	}
}

func TestThumbArgsBoundDecoder(t *testing.T) {
	args := thumbArgs("/tmp/in", "1")
	assertDecoderBounded(t, args)
	if i := slices.Index(args, "-threads"); args[i+1] != "1" {
		t.Errorf("thumbnail decoder threads = %s, want 1", args[i+1])
	}
}

func TestDirSize(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.ts"), make([]byte, 1000), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.ts"), make([]byte, 234), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := dirSize(dir); got != 1234 {
		t.Errorf("dirSize = %d, want 1234", got)
	}
}
