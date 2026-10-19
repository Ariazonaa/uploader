package api

import (
	"context"
	"log"
	"sync"
	"time"

	"uploader/internal/db/store"
)

const (
	// viewFlushInterval is how often batched view analytics are written.
	viewFlushInterval = 5 * time.Second
	// maxPendingViewKeys bounds the distinct (slug, referrer host, UA
	// category) buckets held between flushes. The routes are public and the
	// referrer is client-chosen, so without a bound a flood of made-up hosts
	// would grow the map until the next flush. Views of buckets already
	// pending are always counted; new buckets past the bound are dropped
	// (analytics are best-effort).
	maxPendingViewKeys = 10000
	// viewFlushChunk is how many buckets go into one database batch.
	viewFlushChunk = 500
	// viewFlushTimeout bounds one flush.
	viewFlushTimeout = 30 * time.Second
)

type viewKey struct{ slug, referrer, ua string }

type viewCount struct{ count, views int64 }

// viewBatcher aggregates view analytics in memory and writes them in
// periodic batches. Each public view used to cost two autocommit writes; now
// a popular file costs one upsert per bucket and one counter update every
// viewFlushInterval, however many requests it gets. The state is per
// instance and lost on a crash (at most one interval of views), which is fine
// for best-effort analytics; a clean shutdown flushes it (FlushViews).
type viewBatcher struct {
	mu      sync.Mutex
	pending map[viewKey]viewCount
	dropped int64

	// flushMu serialises flushes, so the ticker and the shutdown flush never
	// write the same instance's batches concurrently.
	flushMu sync.Mutex
	write   func(ctx context.Context, deltas []store.ViewDelta) error
}

func newViewBatcher(write func(ctx context.Context, deltas []store.ViewDelta) error) *viewBatcher {
	return &viewBatcher{pending: map[viewKey]viewCount{}, write: write}
}

// add records one view. countView says whether it also counts toward the
// file's public view counter.
func (b *viewBatcher) add(slug, referrer, ua string, countView bool) {
	k := viewKey{slug, referrer, ua}
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.pending[k]
	if !ok && len(b.pending) >= maxPendingViewKeys {
		b.dropped++
		return
	}
	c.count++
	if countView {
		c.views++
	}
	b.pending[k] = c
}

// flush writes everything pending. A failed chunk is logged and dropped, not
// retried: putting it back could double-count a batch that did commit before
// the error reached us.
func (b *viewBatcher) flush(ctx context.Context) {
	b.flushMu.Lock()
	defer b.flushMu.Unlock()

	b.mu.Lock()
	pending, dropped := b.pending, b.dropped
	b.pending, b.dropped = map[viewKey]viewCount{}, 0
	b.mu.Unlock()

	if dropped > 0 {
		log.Printf("view stats: dropped %d view(s) past the %d pending-bucket cap", dropped, maxPendingViewKeys)
	}
	if len(pending) == 0 {
		return
	}
	deltas := make([]store.ViewDelta, 0, len(pending))
	for k, c := range pending {
		deltas = append(deltas, store.ViewDelta{
			Slug: k.slug, ReferrerHost: k.referrer, UACategory: k.ua, Count: c.count, Views: c.views,
		})
	}
	for start := 0; start < len(deltas); start += viewFlushChunk {
		chunk := deltas[start:min(start+viewFlushChunk, len(deltas))]
		cctx, cancel := context.WithTimeout(ctx, viewFlushTimeout)
		err := b.write(cctx, chunk)
		cancel()
		if err != nil {
			log.Printf("view stats: write %d bucket(s): %v", len(chunk), err)
		}
	}
}

// run flushes every viewFlushInterval until ctx ends. It does not flush on
// exit: requests may still be draining then, see Server.FlushViews.
func (b *viewBatcher) run(ctx context.Context) {
	ticker := time.NewTicker(viewFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.flush(context.WithoutCancel(ctx))
		}
	}
}

// EnableViewBatching switches view analytics to batched writes. Call it
// before serving, run RunViewStats alongside the server, and call FlushViews
// once the HTTP server has drained. Without it (tests), every view is written
// as it happens.
func (s *Server) EnableViewBatching() {
	s.views = newViewBatcher(s.store.RecordViews)
}

// RunViewStats flushes batched view analytics periodically until ctx ends.
func (s *Server) RunViewStats(ctx context.Context) {
	if s.views != nil {
		s.views.run(ctx)
	}
}

// FlushViews writes the batched view analytics that are still pending.
func (s *Server) FlushViews(ctx context.Context) {
	if s.views != nil {
		s.views.flush(ctx)
	}
}
