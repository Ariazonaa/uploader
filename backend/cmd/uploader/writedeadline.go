package main

import (
	"io"
	"net/http"
	"time"
)

// writeIdleTimeout is how long a single response write may block before the
// connection is given up. It bounds stalls, not response size: the deadline
// moves forward as the client keeps reading.
const writeIdleTimeout = time.Minute

// readFromChunk is how much one ReadFrom call hands to the connection before
// the write deadline is pushed again. It keeps the sendfile path worthwhile
// while a client reading at a few KiB/s still finishes a chunk in time.
const readFromChunk = 256 << 10

// withWriteDeadline gives every response a rolling write deadline. A fixed
// http.Server.WriteTimeout caps the whole response, which would cut off large
// downloads, so it is 0 — and then a client that simply stopped reading (zero
// TCP window) held its handler, connection and open blob forever wherever no
// proxy sat in front. Now every write may block for at most timeout.
func withWriteDeadline(next http.Handler, timeout time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dw := &deadlineWriter{ResponseWriter: w, rc: http.NewResponseController(w), timeout: timeout}
		// Forced at the start because a keep-alive connection still carries
		// the previous request's deadline, and again at the end because the
		// server flushes buffered output (or bare headers) after the handler
		// returns, possibly long after its last write.
		dw.extend(true)
		next.ServeHTTP(dw, r)
		dw.extend(true)
	})
}

// deadlineWriter pushes the connection's write deadline forward whenever the
// handler writes. Unwrap keeps http.ResponseController (the upload paths'
// SetReadDeadline) working through it.
type deadlineWriter struct {
	http.ResponseWriter
	rc       *http.ResponseController
	timeout  time.Duration
	extended time.Time
}

// extend sets the deadline to timeout from now. Unforced calls skip the timer
// reset while the last one is recent, so every write still gets at least
// 15/16 of the timeout.
func (w *deadlineWriter) extend(force bool) {
	now := time.Now()
	if !force && now.Sub(w.extended) < w.timeout/16 {
		return
	}
	_ = w.rc.SetWriteDeadline(now.Add(w.timeout))
	w.extended = now
}

func (w *deadlineWriter) WriteHeader(code int) {
	w.extend(false)
	w.ResponseWriter.WriteHeader(code)
}

func (w *deadlineWriter) Write(b []byte) (int, error) {
	w.extend(false)
	return w.ResponseWriter.Write(b)
}

// FlushError is what http.ResponseController.Flush calls.
func (w *deadlineWriter) FlushError() error {
	w.extend(false)
	return w.rc.Flush()
}

// Flush keeps the http.Flusher interface for callers that type-assert it.
func (w *deadlineWriter) Flush() { _ = w.FlushError() }

func (w *deadlineWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// ReadFrom preserves the server's sendfile path, which io.Copy (and so
// http.ServeContent) prefers, while still extending the deadline as the copy
// progresses: a single unbounded ReadFrom would stream a whole file under one
// deadline.
func (w *deadlineWriter) ReadFrom(src io.Reader) (int64, error) {
	rf, ok := w.ResponseWriter.(io.ReaderFrom)
	if !ok {
		return io.Copy(writerOnly{w}, src)
	}
	// ServeContent passes the file inside an *io.LimitedReader. Narrow that
	// one instead of wrapping it again: the kernel fast path only looks
	// through a single LimitedReader to find the *os.File.
	outer, limited := src.(*io.LimitedReader)
	var total int64
	for {
		w.extend(false)
		chunk := &io.LimitedReader{R: src, N: readFromChunk}
		if limited {
			if outer.N <= 0 {
				return total, nil
			}
			chunk.R, chunk.N = outer.R, min(outer.N, readFromChunk)
		}
		want := chunk.N
		n, err := rf.ReadFrom(chunk)
		total += n
		if limited {
			outer.N -= n
		}
		if err != nil || n < want {
			return total, err
		}
	}
}

// writerOnly hides deadlineWriter's ReadFrom from io.Copy.
type writerOnly struct{ io.Writer }
