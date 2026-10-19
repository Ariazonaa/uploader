// Package media performs post-upload processing: EXIF stripping, thumbnail
// generation, dimension/duration extraction and HLS transcoding. Everything
// except EXIF stripping is best-effort — a failure there must never fail the
// upload. Stripping is a privacy guarantee, so it fails closed instead (see
// SanitizeJPEG).
package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"io"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/disintegration/imaging"

	"uploader/internal/storage"

	// Register image decoders for dimension reads and thumbnailing.
	_ "golang.org/x/image/webp"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

const (
	thumbMaxEdge = 512 // px
	thumbQuality = 82

	// maxDecodePixels caps the pixel count we are willing to fully decode. A
	// decoded image costs 4-8 bytes per pixel, so decoding without a budget turns
	// a few-megabyte "decompression bomb" (a 30000x30000 PNG compresses to almost
	// nothing) into gigabytes of resident memory. 50 MPx is far above any real
	// photo. It is one of three limits: maxDecodeSide bounds the resampler, and
	// decodeBudget bounds concurrent decodes (see budget.go). The first two also
	// apply to video frames, which ffmpeg decodes outside that budget.
	maxDecodePixels = 50_000_000

	// ffmpegTimeout bounds a single thumbnail extraction. Frame grabbing is quick;
	// anything longer means a pathological input, not a slow disk.
	ffmpegTimeout = 2 * time.Minute
	// ffprobeTimeout bounds metadata probing, which is quicker still.
	ffprobeTimeout = 30 * time.Second
	// maxThumbSourceBytes caps the JPEG frame ffmpeg may hand back to us.
	maxThumbSourceBytes = 16 << 20
	// maxProbeOutputBytes caps ffprobe's JSON. The entries asked for are tiny,
	// but the buffer is bounded regardless of what the input claims.
	maxProbeOutputBytes = 1 << 20
)

// Result carries whatever metadata processing managed to produce.
type Result struct {
	ThumbKey      *string
	Width         *int
	Height        *int
	DurationMs    *int64
	DominantColor *string // average color as #rrggbb (images only)
	// Video is what ffprobe reported for a video, nil when it was not probed.
	// The HLS transcode reuses it instead of probing the file again.
	Video *VideoInfo
}

// Process generates a thumbnail and extracts metadata for the blob at srcKey,
// writing the thumbnail to thumbKey. It is best-effort and returns partial
// results; callers should persist whatever is non-nil.
func Process(ctx context.Context, st storage.Storage, srcKey, thumbKey, mime string) Result {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return processImage(ctx, st, srcKey, thumbKey)
	case strings.HasPrefix(mime, "video/"):
		return processVideo(ctx, st, srcKey, thumbKey)
	default:
		return Result{}
	}
}

func processImage(ctx context.Context, st storage.Storage, srcKey, thumbKey string) Result {
	var res Result
	rc, err := st.Open(ctx, srcKey)
	if err != nil {
		return res
	}
	defer rc.Close()

	img, cfg, release, err := decodeBounded(ctx, rc)
	// A stored JPEG keeps its EXIF orientation (the metadata strip preserves
	// it), so browsers display it turned. Dimensions and thumbnail follow that.
	orientation := 0
	if _, serr := rc.Seek(0, io.SeekStart); serr == nil {
		orientation = jpegOrientation(rc)
	}
	// The header dimensions are recorded even when the pixels are not decoded
	// (over the limits) — the share page and oEmbed use them. They are also the
	// right ones when decoding works: for a GIF they are the logical screen,
	// while the decoded image is only its first frame.
	if cfg.Width > 0 && cfg.Height > 0 {
		w, h := cfg.Width, cfg.Height
		if orientation >= 5 { // 5-8 turn the image on its side
			w, h = h, w
		}
		res.Width, res.Height = &w, &h
	}
	if err != nil {
		return res // refused or undecodable — deliberately not an error
	}
	defer release()

	// Turned after scaling: the thumbnail carries no EXIF, and turning it is
	// cheaper than turning the full image.
	thumb := orient(imaging.Fit(onCanvas(img, cfg), thumbMaxEdge, thumbMaxEdge, imaging.Lanczos), orientation)

	// Average color (downscale to 1px) → dominant theme-color for embeds.
	if c := averageColor(thumb); c != "" {
		res.DominantColor = &c
	}

	var buf bytes.Buffer
	if err := imaging.Encode(&buf, thumb, imaging.JPEG, imaging.JPEGQuality(thumbQuality)); err != nil {
		return res
	}
	if _, err := st.Put(ctx, thumbKey, &buf); err == nil {
		key := thumbKey
		res.ThumbKey = &key
	}
	return res
}

// errDecodeRefused reports an image whose header is outside the decode caps.
var errDecodeRefused = errors.New("image exceeds the decode limits")

// decodeBounded decodes the image in rs, but only after its header passed the
// per-image caps (decodeAllowed), the format-specific checks of
// estimateDecodeBytes (e.g. the JPEG scan limit), and its estimated decode
// memory has been charged to the process-wide decodeBudget. The header config
// is returned even when decoding is refused. On success the caller must call
// release once it no longer needs the decoded image.
//
// Reading the header first is the point: a decompression bomb is a few bytes on
// disk and gigabytes once materialized, so the decision has to be made before
// any pixel is allocated.
func decodeBounded(ctx context.Context, rs io.ReadSeeker) (image.Image, image.Config, func(), error) {
	cfg, format, err := image.DecodeConfig(rs)
	if err != nil {
		return nil, image.Config{}, nil, err
	}
	if !decodeAllowed(cfg.Width, cfg.Height) {
		return nil, cfg, nil, errDecodeRefused
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, cfg, nil, err
	}
	need, err := estimateDecodeBytes(rs, cfg, format)
	if err != nil {
		return nil, cfg, nil, err
	}
	if need > decodeBudgetBytes {
		// Waiting until it could run alone would still put more than the whole
		// budget on top of everything else in the container.
		return nil, cfg, nil, errDecodeRefused
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, cfg, nil, err
	}
	release, err := decodeBudget.acquire(ctx, need)
	if err != nil {
		return nil, cfg, nil, err
	}
	// Deferred so a decoder panic gives the charge back too: the job queue
	// recovers panics, and a leaked charge would shrink the budget for the life
	// of the process until every later decode blocks on it.
	handedOff := false
	defer func() {
		if !handedOff {
			release()
		}
	}()
	img, _, err := image.Decode(rs)
	if err != nil {
		return nil, cfg, nil, err
	}
	handedOff = true
	return img, cfg, release, nil
}

// onCanvas returns img placed on a transparent canvas of the header's size, or
// img itself when it already covers exactly that. Only a GIF differs: its first
// frame may cover just part of the logical screen, offset into it, and a
// thumbnail of the bare frame would show only that part.
func onCanvas(img image.Image, cfg image.Config) image.Image {
	screen := image.Rect(0, 0, cfg.Width, cfg.Height)
	if img.Bounds() == screen {
		return img
	}
	canvas := image.NewNRGBA(screen)
	draw.Draw(canvas, img.Bounds(), img, img.Bounds().Min, draw.Src)
	return canvas
}

// orient returns img as EXIF orientation o (1-8) says it is to be displayed.
func orient(img *image.NRGBA, o int) *image.NRGBA {
	switch o {
	case 2:
		return imaging.FlipH(img)
	case 3:
		return imaging.Rotate180(img)
	case 4:
		return imaging.FlipV(img)
	case 5:
		return imaging.Transpose(img)
	case 6: // 90° clockwise; imaging rotates counter-clockwise
		return imaging.Rotate270(img)
	case 7:
		return imaging.Transverse(img)
	case 8:
		return imaging.Rotate90(img)
	default:
		return img
	}
}

// averageColor downsizes the image to 1x1 and returns its color as #rrggbb.
func averageColor(img image.Image) string {
	one := imaging.Resize(img, 1, 1, imaging.Box)
	r, g, b, _ := one.At(0, 0).RGBA() // 16-bit per channel
	return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
}

// processVideo shells out to ffprobe/ffmpeg when available. If ffmpeg is not
// installed the upload still succeeds, just without a thumbnail.
func processVideo(ctx context.Context, st storage.Storage, srcKey, thumbKey string) Result {
	var res Result

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return res // ffmpeg unavailable; skip silently
	}
	// ffmpeg needs a real file path: the blob itself when the storage has one,
	// else a temporary copy.
	tmp, cleanup, err := inputPath(ctx, st, srcKey)
	if err != nil {
		return res
	}
	defer cleanup()

	// Dimensions and duration come from the stream itself. The thumbnail frame
	// below is scaled down to at most 640px, so it cannot provide them.
	info := probeVideo(ctx, tmp)
	res.Video = &info
	if info.Duration > 0 {
		ms := info.Duration.Milliseconds()
		res.DurationMs = &ms
	}
	if info.Width > 0 && info.Height > 0 {
		w, h := info.Width, info.Height
		res.Width, res.Height = &w, &h
		// A frame size no image would be decoded at is not decoded here either.
		if !decodeAllowed(w, h) {
			return res
		}
	}
	// ffmpeg is a separate process, outside GOMEMLIMIT, but inside the same
	// container: its frames are charged to the decode budget like an image
	// decode, so thumbnails, transcodes and image decodes cannot add up past
	// the memory limit together.
	need := thumbDecodeBytes(info)
	if need > decodeBudgetBytes {
		return res
	}
	release, err := decodeBudget.acquire(ctx, need)
	if err != nil {
		return res
	}
	defer release()

	// Extract a frame ~1s in (or at start for very short clips). Bounded in time
	// (a crafted file must not pin a worker forever) and in output size.
	seek := "1"
	if res.DurationMs != nil && *res.DurationMs < 1000 {
		seek = "0"
	}
	frameCtx, cancel := context.WithTimeout(ctx, ffmpegTimeout)
	defer cancel()
	// #nosec G204 -- fixed binary and flags; tmp is our own os.CreateTemp result.
	cmd := exec.CommandContext(frameCtx, "ffmpeg", thumbArgs(tmp, seek)...)
	var out bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &out, remaining: maxThumbSourceBytes}
	if err := cmd.Run(); err != nil || out.Len() == 0 {
		return res
	}
	if _, err := st.Put(ctx, thumbKey, bytes.NewReader(out.Bytes())); err == nil {
		key := thumbKey
		res.ThumbKey = &key
	}
	return res
}

// thumbArgs builds the ffmpeg command line that writes one frame of in, taken
// at seek, as a JPEG to stdout.
func thumbArgs(in, seek string) []string {
	return []string{"-y",
		// One decoder thread: frame threading holds a set of frames per thread,
		// and the media workers already run one per CPU.
		"-threads", "1",
		// Checked against every frame, so a stream whose size changes after the
		// part ffprobe looked at is refused by the decoder rather than allocated.
		"-max_pixels", strconv.Itoa(maxDecodePixels),
		"-ss", seek, "-i", in,
		"-frames:v", "1", "-vf", "scale='min(640,iw)':-2", "-f", "image2pipe", "-vcodec", "mjpeg", "pipe:1"}
}

// limitedWriter forwards at most remaining bytes and silently discards the rest,
// so a subprocess writing to a buffer cannot grow it without bound.
type limitedWriter struct {
	w         *bytes.Buffer
	remaining int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.remaining <= 0 {
		return len(p), nil // pretend it was consumed; the caller's buffer is full
	}
	chunk := p
	if len(chunk) > l.remaining {
		chunk = chunk[:l.remaining]
	}
	n, err := l.w.Write(chunk)
	l.remaining -= n
	if err != nil {
		return n, err
	}
	return len(p), nil
}

// VideoInfo is what ffprobe reports about a video file. Zero values mean
// "unknown".
type VideoInfo struct {
	Duration time.Duration
	// Width and Height are the display dimensions: a rotation flag (phones
	// record portrait video as rotated landscape) is already applied.
	Width, Height int
	// Refs is the number of reference frames the stream declares, PixFmt its
	// decoded pixel format. Both feed the decode-memory estimate.
	Refs   int
	PixFmt string
}

// probeVideo reads the duration and the first video stream's dimensions via
// ffprobe. Best-effort: any failure yields a zero VideoInfo.
func probeVideo(ctx context.Context, path string) VideoInfo {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return VideoInfo{}
	}
	probeCtx, cancel := context.WithTimeout(ctx, ffprobeTimeout)
	defer cancel()
	// ffprobe decodes a frame when the container does not describe the stream
	// fully; -max_pixels keeps that frame bounded too. The dimensions the
	// container declares are reported either way.
	// #nosec G204 -- fixed binary and flags; path is our own os.CreateTemp result.
	cmd := exec.CommandContext(probeCtx, "ffprobe", "-v", "error",
		"-max_pixels", strconv.Itoa(maxDecodePixels),
		"-select_streams", "v:0",
		"-show_entries", "format=duration:stream=width,height,refs,pix_fmt:stream_tags=rotate:stream_side_data=rotation",
		"-of", "json", path)
	var out bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &out, remaining: maxProbeOutputBytes}
	if err := cmd.Run(); err != nil {
		return VideoInfo{}
	}
	return parseProbeOutput(out.Bytes())
}

// parseProbeOutput extracts VideoInfo from ffprobe's JSON output.
func parseProbeOutput(b []byte) VideoInfo {
	var probe struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			Width  int    `json:"width"`
			Height int    `json:"height"`
			Refs   int    `json:"refs"`
			PixFmt string `json:"pix_fmt"`
			Tags   struct {
				Rotate string `json:"rotate"`
			} `json:"tags"`
			SideData []struct {
				Rotation *float64 `json:"rotation"`
			} `json:"side_data_list"`
		} `json:"streams"`
	}
	var info VideoInfo
	if err := json.Unmarshal(b, &probe); err != nil {
		return info
	}
	secs, err := strconv.ParseFloat(strings.TrimSpace(probe.Format.Duration), 64)
	if err == nil && secs > 0 && secs < float64(math.MaxInt64/int64(time.Second)) {
		info.Duration = time.Duration(secs * float64(time.Second))
	}
	if len(probe.Streams) == 0 {
		return info
	}
	s := probe.Streams[0]
	if s.Width <= 0 || s.Height <= 0 {
		return info
	}
	info.Width, info.Height = s.Width, s.Height
	info.Refs, info.PixFmt = s.Refs, s.PixFmt
	rotation := 0.0
	if r, err := strconv.ParseFloat(s.Tags.Rotate, 64); err == nil {
		rotation = r
	}
	for _, sd := range s.SideData {
		if sd.Rotation != nil {
			rotation = *sd.Rotation
		}
	}
	if int(math.Round(rotation/90))%2 != 0 { // 90 or 270 degrees, either sign
		info.Width, info.Height = info.Height, info.Width
	}
	return info
}
