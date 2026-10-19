package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileStorage stores blobs on the local filesystem under a root directory.
type FileStorage struct {
	root string
}

// NewFileStorage creates the root directory if needed and returns the backend.
func NewFileStorage(root string) (*FileStorage, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, fmt.Errorf("create storage root: %w", err)
	}
	return &FileStorage{root: abs}, nil
}

// pathFor resolves a key to an absolute path, rejecting any path that would
// escape the storage root (path-traversal defense).
func (f *FileStorage) pathFor(key string) (string, error) {
	if key == "" {
		return "", errors.New("empty storage key")
	}
	// Normalize separators and reject any traversal or absolute component
	// outright — keys are internally generated, so anything else is malformed.
	norm := strings.ReplaceAll(key, "\\", "/")
	for _, seg := range strings.Split(norm, "/") {
		if seg == ".." {
			return "", fmt.Errorf("invalid storage key %q", key)
		}
	}
	full := filepath.Join(f.root, filepath.FromSlash(norm))
	rel, err := filepath.Rel(f.root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid storage key %q", key)
	}
	return full, nil
}

// Put writes r under key.
func (f *FileStorage) Put(ctx context.Context, key string, r io.Reader) (int64, error) {
	path, err := f.pathFor(key)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return 0, err
	}
	// Write to a temp file then rename for atomicity.
	tmp, err := os.CreateTemp(filepath.Dir(path), stagingPrefix+"*")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	n, err := io.Copy(tmp, r)
	// Flush the data before the rename publishes it. Filesystems may commit
	// the rename ahead of the data blocks, so after a power cut the key could
	// point at an empty or truncated file while the row says it is complete.
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmpName)
		return 0, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return 0, err
	}
	syncDir(filepath.Dir(path))
	return n, nil
}

// syncDir makes a rename in dir durable. Best-effort: Windows cannot sync a
// directory handle, and a lost rename leaves the staging file behind, not a
// corrupt blob.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// Open returns a seekable file handle.
func (f *FileStorage) Open(ctx context.Context, key string) (io.ReadSeekCloser, error) {
	path, err := f.pathFor(key)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

// Stat returns size and modtime.
func (f *FileStorage) Stat(ctx context.Context, key string) (Stat, error) {
	path, err := f.pathFor(key)
	if err != nil {
		return Stat{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Stat{}, err
	}
	return Stat{Size: info.Size(), ModTime: info.ModTime()}, nil
}

// DeletePrefix removes everything under a key prefix (used to drop a file's HLS
// directory). A missing prefix is not an error.
func (f *FileStorage) DeletePrefix(ctx context.Context, prefix string) error {
	path, err := f.pathFor(prefix)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Walk visits every blob key under the storage root (relative,
// slash-separated) in lexical order. Dotfiles (e.g. temp uploads, the health
// probe) are skipped. It stops once ctx is cancelled.
func (f *FileStorage) Walk(ctx context.Context, fn func(key string) error) error {
	return filepath.WalkDir(f.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			// Skip dot-directories entirely (e.g. the ".tus" scratch dir for
			// in-progress resumable uploads) so they aren't treated as orphans.
			if path != f.root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		rel, err := filepath.Rel(f.root, path)
		if err != nil {
			return nil
		}
		return fn(filepath.ToSlash(rel))
	})
}

// LocalPath returns the file system path of key, for tools that need a real
// file (ffmpeg/ffprobe) and would otherwise get a temporary copy. The caller
// must only read it.
func (f *FileStorage) LocalPath(key string) (string, error) {
	return f.pathFor(key)
}

// stagingPrefix names Put's temporary files, which sit next to their key.
const stagingPrefix = ".upload-"

// SweepStaging removes Put staging files last modified before cutoff and
// returns how many it removed. Put deletes its staging file on every error
// path, but not when the process dies mid-write (OOM kill, SIGKILL, power
// loss), and List skips dotfiles, so the orphan scan never saw these and they
// accumulated in the shard directories forever. Dot-directories (the tus
// scratch dir) are left alone, as in List. Removal goes through an os.Root, so
// a directory swapped for a symlink mid-walk cannot redirect it.
func (f *FileStorage) SweepStaging(cutoff time.Time) int {
	root, err := os.OpenRoot(f.root)
	if err != nil {
		return 0
	}
	defer root.Close()

	removed := 0
	_ = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != "." && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !strings.HasPrefix(d.Name(), stagingPrefix) {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().Before(cutoff) && root.Remove(p) == nil {
			removed++
		}
		return nil
	})
	return removed
}

// Delete removes the key; a missing file is not an error.
func (f *FileStorage) Delete(ctx context.Context, key string) error {
	path, err := f.pathFor(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
