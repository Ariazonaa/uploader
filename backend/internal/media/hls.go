package media

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"

	"uploader/internal/storage"
)

const (
	// hlsTimeout bounds a single transcode. Generous enough for a long video on a
	// slow box, short enough that a crafted input cannot occupy a worker forever.
	hlsTimeout = 30 * time.Minute
	// maxHLSOutputBytes caps the size of one rendition, both in the scratch
	// directory while ffmpeg runs and in the blob store afterwards. It is
	// enforced by hlsWatchInterval polling (see TranscodeHLS), so the scratch
	// directory can overshoot it by at most one interval's worth of output.
	maxHLSOutputBytes = 2 << 30 // 2 GiB
	// hlsWatchInterval is how often the scratch directory is measured while
	// ffmpeg runs.
	hlsWatchInterval = time.Second

	// The rendition's shape bounds its size by construction: longest edge at
	// most hlsMaxEdge, video bitrate VBV-capped at hlsVideoMaxrateKbps, fixed
	// stereo audio. Together with the -t duration cap that makes the output
	// predictable instead of "whatever the input asks for".
	hlsMaxEdge          = 1920
	hlsVideoMaxrateKbps = 5000
	hlsVideoBufsizeKbps = 2 * hlsVideoMaxrateKbps
	hlsAudioKbps        = 128
	// hlsMuxOverhead approximates MPEG-TS packetization overhead on top of the
	// elementary-stream bitrates.
	hlsMuxOverhead = 1.15
)

// hlsSlots limits concurrent transcodes process-wide. A libx264 re-encode uses
// every core it is given and a few hundred MB of RAM; two of them next to the
// image workers is what pushes a 2 GiB container into the OOM killer. main.go
// also runs HLS on a dedicated single-worker queue, so this is the backstop for
// any other caller.
var hlsSlots = make(chan struct{}, 1)

// hlsMaxDuration is the longest source that fits maxHLSOutputBytes at the capped
// bitrates (~48 minutes). Longer videos are not transcoded and are served as
// plain MP4 only.
func hlsMaxDuration() time.Duration {
	bytesPerSec := float64(hlsVideoMaxrateKbps+hlsAudioKbps) * 1000 / 8 * hlsMuxOverhead
	return time.Duration(float64(maxHLSOutputBytes) / bytesPerSec * float64(time.Second))
}

// HLSAvailable reports whether ffmpeg is present (HLS transcoding possible).
func HLSAvailable() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

// hlsArgs builds the ffmpeg command line for a bounded HLS rendition.
func hlsArgs(in, outDir string, maxDur time.Duration, threads int) []string {
	t := strconv.Itoa(threads)
	edge := strconv.Itoa(hlsMaxEdge)
	return []string{
		"-nostdin", "-y",
		"-threads", t, // decoder threads; ffmpeg otherwise sizes by host CPUs
		// Decoder frame size cap, also for frames past what ffprobe looked at.
		"-max_pixels", strconv.Itoa(maxDecodePixels),
		"-i", in,
		// First video stream plus the first audio stream if there is one;
		// subtitle/data streams are not carried into the rendition.
		"-map", "0:v:0", "-map", "0:a:0?",
		"-t", strconv.FormatFloat(maxDur.Seconds(), 'f', 0, 64),
		"-vf", "scale=w='min(" + edge + ",iw)':h='min(" + edge + ",ih)'" +
			":force_original_aspect_ratio=decrease:force_divisible_by=2",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
		"-maxrate", strconv.Itoa(hlsVideoMaxrateKbps) + "k",
		"-bufsize", strconv.Itoa(hlsVideoBufsizeKbps) + "k",
		"-pix_fmt", "yuv420p",
		"-threads", t,
		"-c:a", "aac", "-b:a", strconv.Itoa(hlsAudioKbps) + "k", "-ac", "2",
		"-hls_time", "6", "-hls_playlist_type", "vod",
		"-hls_flags", "independent_segments",
		"-hls_segment_filename", filepath.Join(outDir, "seg%03d.ts"),
		filepath.Join(outDir, "index.m3u8"),
	}
}

// TranscodeHLS transcodes the video at srcKey into an HLS rendition (VOD
// playlist + .ts segments) and stores every output file under hlsPrefix in the
// blob store. Returns false (no error) when ffmpeg is unavailable, the video is
// too long for the output budget or its frames exceed the decode caps.
// Best-effort and CPU-heavy — callers run it in the background. On error some
// files may already have been stored under hlsPrefix; the caller is expected to
// remove the prefix.
//
// probed is what Process's ffprobe run reported for the same blob; nil makes
// TranscodeHLS probe it itself.
func TranscodeHLS(ctx context.Context, st storage.Storage, srcKey, hlsPrefix string, probed *VideoInfo) (bool, error) {
	if !HLSAvailable() {
		return false, nil
	}

	select {
	case hlsSlots <- struct{}{}:
		defer func() { <-hlsSlots }()
	case <-ctx.Done():
		return false, ctx.Err()
	}

	in, cleanupIn, err := inputPath(ctx, st, srcKey)
	if err != nil {
		return false, err
	}
	defer cleanupIn()

	maxDur := hlsMaxDuration()
	// A known duration past the budget is skipped up front, and so is a frame
	// size past the decode caps. Both can be forged, so -t, -max_pixels and the
	// size watchdog below still apply to everything that does get transcoded.
	var info VideoInfo
	if probed != nil {
		info = *probed
	} else {
		info = probeVideo(ctx, in)
	}
	if info.Duration > maxDur {
		return false, nil
	}
	if info.Width > 0 && info.Height > 0 && !decodeAllowed(info.Width, info.Height) {
		return false, nil
	}
	// ffmpeg runs outside GOMEMLIMIT but inside the container's memory limit,
	// so the transcode is charged to the decode budget for as long as it runs:
	// image decodes wait for it rather than adding up past the limit with it.
	// A stream whose reference frames alone would take more than the budget
	// (a crafted video just under the pixel cap) is not transcoded at all.
	threads := runtime.GOMAXPROCS(0)
	need := hlsDecodeBytes(info, threads)
	if need > decodeBudgetBytes {
		return false, nil
	}
	release, err := decodeBudget.acquire(ctx, need)
	if err != nil {
		return false, err
	}
	defer release()

	outDir, err := os.MkdirTemp("", "uploader-hls-*")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(outDir)

	// A full libx264 re-encode is the most expensive thing this service does, and
	// it runs on input an unprivileged user chose. Bound it in wall-clock time so
	// one pathological file cannot pin a worker for the lifetime of the process.
	transcodeCtx, cancel := context.WithTimeout(ctx, hlsTimeout)
	defer cancel()
	// #nosec G204 -- fixed binary and flags; the only paths are our own
	// os.CreateTemp/os.MkdirTemp results, never a client-supplied name.
	cmd := exec.CommandContext(transcodeCtx, "ffmpeg", hlsArgs(in, outDir, maxDur, threads)...)

	// ffmpeg's -fs does nothing for the HLS muxer (it writes its segments
	// itself, outside the output-size accounting), so the size cap is enforced
	// here instead: measure the scratch directory while ffmpeg runs and kill the
	// transcode the moment it goes over.
	var exceeded atomic.Bool
	watchDone := make(chan struct{})
	watchExited := make(chan struct{})
	go func() {
		defer close(watchExited)
		ticker := time.NewTicker(hlsWatchInterval)
		defer ticker.Stop()
		for {
			select {
			case <-watchDone:
				return
			case <-transcodeCtx.Done():
				return
			case <-ticker.C:
				if dirSize(outDir) > maxHLSOutputBytes {
					exceeded.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	runErr := cmd.Run()
	close(watchDone)
	<-watchExited
	if exceeded.Load() {
		return false, fmt.Errorf("hls output exceeded %d bytes", int64(maxHLSOutputBytes))
	}
	if runErr != nil {
		return false, runErr
	}

	entries, err := os.ReadDir(outDir)
	if err != nil {
		return false, err
	}
	// Final check on the complete output: the watchdog samples, so a burst in
	// the last interval could have slipped past it.
	if total := dirSize(outDir); total > maxHLSOutputBytes {
		return false, fmt.Errorf("hls output exceeds %d bytes", int64(maxHLSOutputBytes))
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		f, err := os.Open(filepath.Join(outDir, e.Name()))
		if err != nil {
			return false, err
		}
		_, perr := st.Put(ctx, hlsPrefix+"/"+e.Name(), f)
		_ = f.Close()
		if perr != nil {
			return false, perr
		}
	}
	return true, nil
}

// dirSize sums the sizes of the regular files directly or indirectly under dir.
// Errors (a file vanishing mid-walk) are skipped: the result is a measurement,
// not an inventory.
func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
