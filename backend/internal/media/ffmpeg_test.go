package media

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeTestVideo renders a short synthetic clip with ffmpeg, or skips the test
// when ffmpeg/ffprobe are not installed (they are optional at runtime too).
func makeTestVideo(t *testing.T, w, h string) []byte {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	out := filepath.Join(t.TempDir(), "in.mp4")
	cmd := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi",
		"-i", "testsrc=duration=2:size="+w+"x"+h+":rate=25",
		"-f", "lavfi", "-i", "sine=duration=2",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot render a test video: %v: %s", err, b)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestProcessVideoTakesDimensionsFromStream(t *testing.T) {
	// Wider than the 640px thumbnail: the recorded size must be the video's.
	st := newMemStorage()
	st.blobs["v.mp4"] = makeTestVideo(t, "1280", "720")

	res := processVideo(context.Background(), st, "v.mp4", "thumbs/v.jpg")
	if res.Width == nil || res.Height == nil || *res.Width != 1280 || *res.Height != 720 {
		t.Fatalf("dimensions = %v x %v, want 1280x720", res.Width, res.Height)
	}
	if res.DurationMs == nil || *res.DurationMs < 1500 || *res.DurationMs > 2500 {
		t.Errorf("duration = %v, want ~2000ms", res.DurationMs)
	}
	if res.ThumbKey == nil {
		t.Error("no thumbnail")
	}
}

func TestTranscodeHLSProducesRendition(t *testing.T) {
	st := newMemStorage()
	st.blobs["v.mp4"] = makeTestVideo(t, "320", "240")

	ok, err := TranscodeHLS(context.Background(), st, "v.mp4", "hls/ab/slug", nil)
	if err != nil || !ok {
		t.Fatalf("TranscodeHLS = %v, %v", ok, err)
	}
	playlist, found := st.blobs["hls/ab/slug/index.m3u8"]
	if !found || !strings.Contains(string(playlist), "#EXTM3U") {
		t.Fatal("no playlist stored")
	}
	segs := 0
	for k := range st.blobs {
		if strings.HasPrefix(k, "hls/ab/slug/seg") && strings.HasSuffix(k, ".ts") {
			segs++
		}
	}
	if segs == 0 {
		t.Error("no segments stored")
	}
}
