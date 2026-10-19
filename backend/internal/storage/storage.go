// Package storage abstracts blob persistence. The local filesystem backend is
// the only implementation and the only one that will be shipped (object-store
// backends are deliberately out of scope); the interface exists so tests can
// substitute their own.
package storage

import (
	"context"
	"io"
	"time"
)

// Stat describes a stored blob.
type Stat struct {
	Size    int64
	ModTime time.Time
}

// Storage persists and serves opaque blobs addressed by a string key.
//
// Keys are caller-controlled relative paths (e.g. "ab/cd/abcdef.png").
// Implementations MUST reject keys that escape their root.
type Storage interface {
	// Put writes r under key, creating intermediate structure as needed, and
	// returns the number of bytes written.
	Put(ctx context.Context, key string, r io.Reader) (int64, error)
	// Open returns a seekable reader for serving (Range requests).
	Open(ctx context.Context, key string) (io.ReadSeekCloser, error)
	// Stat returns size and modtime for the key.
	Stat(ctx context.Context, key string) (Stat, error)
	// Delete removes the key. Missing keys are not an error.
	Delete(ctx context.Context, key string) error
	// DeletePrefix removes all keys under a prefix (e.g. a file's HLS directory).
	DeletePrefix(ctx context.Context, prefix string) error
	// Walk calls fn for every stored key (relative, slash-separated) in
	// lexical order, so the keys of one directory arrive together, and stops
	// at the first error fn returns. Used by the orphan-blob janitor, which
	// must not hold the whole tree in memory. Dotfiles (temp/probe files) and
	// dot-directories are excluded.
	Walk(ctx context.Context, fn func(key string) error) error
}
