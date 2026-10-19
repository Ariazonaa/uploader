// Package jobs provides a small in-process worker pool for best-effort
// background work (thumbnail generation, HLS transcoding) so the upload request
// path doesn't block on CPU-heavy media processing. It deliberately has no
// external dependency (no Redis/queue broker) — the unit of work is a closure
// and the queue lives for the lifetime of the process.
package jobs

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// dropReportEvery bounds how often a queue logs dropped jobs. One line per
// drop let a burst of public traffic (every /raw hit queues a view-stats job)
// bury everything else in the log; drops are counted and reported in bulk.
const dropReportEvery = time.Minute

// Job is a unit of background work. It receives a context tied to the queue's
// lifetime (cancelled on Shutdown).
type Job func(ctx context.Context)

// Queue is a bounded worker pool. Submit is non-blocking: if every worker is
// busy and the buffer is full the job is dropped (and counted) rather than
// stalling an upload — media processing is always best-effort.
type Queue struct {
	name   string
	ch     chan Job
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	dropped    atomic.Int64 // drops not yet reported
	lastReport atomic.Int64 // unix nanos of the last drop report
}

// New creates a queue with the given number of workers and buffer capacity.
// Both are floored at 1. name identifies the queue in log lines.
func New(name string, workers, buffer int) *Queue {
	if workers < 1 {
		workers = 1
	}
	if buffer < 1 {
		buffer = 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	q := &Queue{
		name:   name,
		ch:     make(chan Job, buffer),
		ctx:    ctx,
		cancel: cancel,
	}
	for i := 0; i < workers; i++ {
		q.wg.Add(1)
		go q.worker()
	}
	return q
}

func (q *Queue) worker() {
	defer q.wg.Done()
	for {
		select {
		case <-q.ctx.Done():
			return
		case job, ok := <-q.ch:
			if !ok {
				return
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("jobs: worker recovered from panic: %v", r)
					}
				}()
				job(q.ctx)
			}()
		}
	}
}

// Submit enqueues a job. It returns false when the buffer is full or the queue
// is shutting down; callers treat a dropped job as best-effort.
func (q *Queue) Submit(job Job) bool {
	if job == nil {
		return false
	}
	select {
	case <-q.ctx.Done():
		return false
	case q.ch <- job:
		return true
	default:
		q.noteDrop()
		return false
	}
}

// noteDrop counts a dropped job and logs the count at most once per
// dropReportEvery. The first drop is reported right away.
func (q *Queue) noteDrop() {
	q.dropped.Add(1)
	now := time.Now().UnixNano()
	last := q.lastReport.Load()
	if now-last < int64(dropReportEvery) || !q.lastReport.CompareAndSwap(last, now) {
		return
	}
	q.reportDrops()
}

func (q *Queue) reportDrops() {
	if n := q.dropped.Swap(0); n > 0 {
		log.Printf("jobs: %s queue full, dropped %d job(s) since the last report", q.name, n)
	}
}

// Shutdown stops accepting work, cancels in-flight job contexts and waits for
// workers to exit. Drops not yet reported are logged.
func (q *Queue) Shutdown() {
	q.cancel()
	q.wg.Wait()
	q.reportDrops()
}
