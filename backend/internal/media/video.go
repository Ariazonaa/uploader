package media

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"uploader/internal/storage"
)

// localPather is implemented by storage backends whose blobs are plain files
// (storage.FileStorage). ffmpeg and ffprobe then read the blob in place
// instead of a temporary copy of it.
type localPather interface {
	LocalPath(key string) (string, error)
}

// inputPath returns a file system path ffmpeg/ffprobe can read the blob at
// key from, and a cleanup function to call once they are done: the blob
// itself where the storage has one, else a temporary copy. Only unencrypted
// blobs are ever processed, so the file on disk is the media itself.
func inputPath(ctx context.Context, st storage.Storage, key string) (string, func(), error) {
	if lp, ok := st.(localPather); ok {
		if p, err := lp.LocalPath(key); err == nil {
			return p, func() {}, nil
		}
	}
	rc, err := st.Open(ctx, key)
	if err != nil {
		return "", nil, err
	}
	defer rc.Close()
	tmp, err := writeTemp(rc)
	if err != nil {
		return "", nil, err
	}
	return tmp, func() { cleanupTemp(tmp) }, nil
}

const (
	// maxVideoRefs is the most reference frames a stream may keep (H.264's
	// and HEVC's DPB limit), assumed when ffprobe does not report a count.
	maxVideoRefs = 16
	// videoDecodeOverhead covers ffmpeg's own buffers, codec tables and
	// demuxer state on top of the frames themselves.
	videoDecodeOverhead = 32 << 20
	// minVideoDecodeBytes is the least a thumbnail extraction is charged.
	minVideoDecodeBytes = 64 << 20
	// minHLSBytes is the least a transcode is charged: libx264 alone keeps
	// lookahead, reference and threading buffers well past a decoder's.
	minHLSBytes = 256 << 20
	// x264FramesBase and x264FramesPerThread approximate the frames libx264
	// holds with the veryfast preset (rc-lookahead 10, 3 B-frames, 1 ref)
	// plus about one and a half frames in flight per encoder thread.
	x264FramesBase      = 16
	x264FramesPerThread = 2
)

// videoFrameBytes estimates the memory one decoded frame of info takes. An
// unknown size returns 0; callers treat that as "charge the whole budget".
func videoFrameBytes(info VideoInfo) int64 {
	if info.Width <= 0 || info.Height <= 0 {
		return 0
	}
	return int64(float64(info.Width) * float64(info.Height) * pixFmtBytes(info.PixFmt))
}

// videoRefs is the reference-frame count to plan for: the declared one,
// clamped, or the codec maximum when ffprobe did not say.
func videoRefs(info VideoInfo) int64 {
	if info.Refs <= 0 || info.Refs > maxVideoRefs {
		return maxVideoRefs
	}
	return int64(info.Refs)
}

// videoDecodeBytes estimates the peak memory of decoding info with the given
// number of decoder threads: the reference frames the stream keeps, one frame
// per frame-threading worker, and a few in the filter chain and output.
func videoDecodeBytes(info VideoInfo, threads int) int64 {
	frame := videoFrameBytes(info)
	if frame == 0 {
		return decodeBudgetBytes
	}
	return frame*(videoRefs(info)+int64(max(threads, 1))+3) + videoDecodeOverhead
}

// thumbDecodeBytes is what a thumbnail extraction (one decoder thread, see
// thumbArgs) is charged against the decode budget. An unknown frame size
// charges the whole budget: the extraction then runs alone rather than next
// to decodes whose sum nobody can bound.
func thumbDecodeBytes(info VideoInfo) int64 {
	return max(videoDecodeBytes(info, 1), minVideoDecodeBytes)
}

// hlsDecodeBytes is what a transcode with the given thread count is charged:
// the decode of the source plus libx264 encoding the rendition, which is at
// most hlsMaxEdge on its longest side.
func hlsDecodeBytes(info VideoInfo, threads int) int64 {
	if videoFrameBytes(info) == 0 {
		return decodeBudgetBytes // unknown size: run alone, see thumbDecodeBytes
	}
	dec := videoDecodeBytes(info, threads)
	w, h := float64(info.Width), float64(info.Height)
	if long := max(w, h); long > hlsMaxEdge {
		w, h = w*hlsMaxEdge/long, h*hlsMaxEdge/long
	}
	// The rendition is always yuv420p: 1.5 bytes per pixel.
	enc := int64(w*h*1.5) * (x264FramesBase + x264FramesPerThread*int64(max(threads, 1)))
	return max(dec+enc, minHLSBytes)
}

// pixFmtBytes is the bytes per pixel of a frame decoded in ffmpeg pixel
// format pf: chroma subsampling sets the base, more than 8 bits per sample
// doubles it. Unknown or exotic formats count as 4:4:4 with alpha at 16 bits,
// the most any common format takes.
func pixFmtBytes(pf string) float64 {
	pf = strings.ToLower(pf)
	var base float64
	switch {
	case pf == "":
		return 8
	case strings.HasPrefix(pf, "nv12"), strings.HasPrefix(pf, "nv21"), strings.HasPrefix(pf, "p010"),
		strings.HasPrefix(pf, "p016"), strings.Contains(pf, "420"), strings.Contains(pf, "411"):
		base = 1.5
	case strings.HasPrefix(pf, "nv16"), strings.Contains(pf, "422"), strings.Contains(pf, "440"):
		base = 2
	case strings.HasPrefix(pf, "gray"):
		base = 1
	case strings.Contains(pf, "444"), strings.HasPrefix(pf, "rgb"), strings.HasPrefix(pf, "bgr"),
		strings.HasPrefix(pf, "gbr"):
		base = 3
	default:
		return 8
	}
	if strings.HasPrefix(pf, "yuva") || strings.HasPrefix(pf, "gbra") || strings.HasSuffix(pf, "a") ||
		strings.Contains(pf, "rgba") || strings.Contains(pf, "bgra") || strings.Contains(pf, "argb") {
		base += 1
	}
	// Formats past 8 bits per sample spell out their byte order:
	// yuv420p10le, gray16be, rgb48le, p010le.
	if m := deepPixFmt.FindStringSubmatch(pf); m != nil {
		if bits, err := strconv.Atoi(m[1]); err == nil && bits > 8 {
			return base * 2
		}
	}
	return base
}

// deepPixFmt matches the bit depth and byte order an ffmpeg pixel format name
// ends in.
var deepPixFmt = regexp.MustCompile(`(\d+)(le|be)$`)
