package media

import (
	"context"
	"sync"
)

const (
	// maxDecodeSide caps each edge of an image we are willing to decode. The
	// pixel cap alone is not enough: a 50,000,000x1 PNG is under maxDecodePixels
	// and compresses to ~50 KB, but the Lanczos resampler in imaging.Fit
	// allocates filter weights proportional to the source edge length divided by
	// the target — roughly 100 bytes per source pixel for such a strip, i.e.
	// several gigabytes. 16384 is the largest edge JPEG/WebP encoders commonly
	// produce and far past any camera sensor.
	maxDecodeSide = 16384

	// decodeBudgetBytes is the process-wide ceiling on the estimated memory of
	// all image decodes running at the same time. The per-image caps bound one
	// decode; this bounds their sum, so N media workers cannot each hold a
	// worst-case decode inside the container's memory limit at once. An image
	// whose estimate alone exceeds it is not decoded at all.
	decodeBudgetBytes = 768 << 20
)

// decodeAllowed reports whether an image with the given header dimensions may
// be fully decoded.
func decodeAllowed(width, height int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	if width > maxDecodeSide || height > maxDecodeSide {
		return false
	}
	return int64(width)*int64(height) <= maxDecodePixels
}

// byteBudget is a weighted semaphore over bytes. It is deliberately tiny — the
// module avoids pulling golang.org/x/sync in as a direct dependency for this.
type byteBudget struct {
	mu   sync.Mutex
	size int64
	used int64
	wake chan struct{} // closed (and replaced) whenever capacity is released
}

func newByteBudget(size int64) *byteBudget {
	return &byteBudget{size: size, wake: make(chan struct{})}
}

// acquire blocks until n bytes are available (or ctx ends) and returns the
// matching release function. A request larger than the whole budget is clamped
// to it, i.e. it waits until it can run alone rather than never running.
func (b *byteBudget) acquire(ctx context.Context, n int64) (func(), error) {
	if n < 1 {
		n = 1
	}
	if n > b.size {
		n = b.size
	}
	for {
		b.mu.Lock()
		if b.used+n <= b.size {
			b.used += n
			b.mu.Unlock()
			var once sync.Once
			return func() { once.Do(func() { b.release(n) }) }, nil
		}
		wake := b.wake
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wake:
		}
	}
}

func (b *byteBudget) release(n int64) {
	b.mu.Lock()
	b.used -= n
	close(b.wake)
	b.wake = make(chan struct{})
	b.mu.Unlock()
}

// decodeBudget is shared by every full image decode in the process: thumbnail
// generation on the job queue and the JPEG re-encode fallback on the request
// path.
var decodeBudget = newByteBudget(decodeBudgetBytes)
