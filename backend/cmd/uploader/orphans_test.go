package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"uploader/internal/storage"
	"uploader/internal/upload"
)

type fakeOrphanStore struct {
	keys  map[string]struct{}
	slugs map[string]struct{} // slugs that still have a file row
	// keyQueries counts ReferencedKeys calls and the most keys any one asked.
	keyQueries, maxKeys *int
}

func (f fakeOrphanStore) ReferencedKeys(_ context.Context, keys []string) (map[string]struct{}, error) {
	if f.keyQueries != nil {
		*f.keyQueries++
		*f.maxKeys = max(*f.maxKeys, len(keys))
	}
	out := map[string]struct{}{}
	for _, k := range keys {
		if _, ok := f.keys[k]; ok {
			out[k] = struct{}{}
		}
	}
	return out, nil
}

func (f fakeOrphanStore) ExistingSlugs(_ context.Context, slugs []string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	for _, s := range slugs {
		if _, ok := f.slugs[s]; ok {
			out[s] = struct{}{}
		}
	}
	return out, nil
}

// blobTree is a FileStorage whose blobs get explicit modification times.
type blobTree struct {
	t    *testing.T
	root string
	st   *storage.FileStorage
}

func newBlobTree(t *testing.T) *blobTree {
	t.Helper()
	root := t.TempDir()
	st, err := storage.NewFileStorage(root)
	if err != nil {
		t.Fatal(err)
	}
	return &blobTree{t: t, root: root, st: st}
}

func (b *blobTree) put(key string, mtime time.Time) {
	b.t.Helper()
	p := filepath.Join(b.root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		b.t.Fatal(err)
	}
	touch(b.t, p, mtime)
}

func (b *blobTree) has(key string) bool {
	return exists(filepath.Join(b.root, filepath.FromSlash(key)))
}

// The grace period is measured against the moment the database was read, not
// when the scan reaches a blob: a scan that runs for hours must not collect
// blobs written shortly before (or after) its snapshot.
func TestScanOrphansMeasuresGraceAtSnapshot(t *testing.T) {
	b := newBlobTree(t)
	now := time.Now()
	// The snapshot was read two hours ago; the walk has been running since.
	snapshot := now.Add(-2 * time.Hour)
	b.put("ab/cd/known.png", now.Add(-48*time.Hour))
	b.put("ab/cd/orphan.png", snapshot.Add(-orphanGrace-time.Minute))
	b.put("ab/cd/young.png", snapshot.Add(-orphanGrace/2)) // an hour and a half old by now
	b.put("ab/cd/after.png", snapshot.Add(time.Minute))

	scanOrphans(context.Background(), fakeOrphanStore{keys: map[string]struct{}{"ab/cd/known.png": {}}}, b.st, snapshot)

	if b.has("ab/cd/orphan.png") {
		t.Error("orphan older than the grace period at snapshot time survived")
	}
	for _, k := range []string{"ab/cd/known.png", "ab/cd/young.png", "ab/cd/after.png"} {
		if !b.has(k) {
			t.Errorf("%s was collected", k)
		}
	}
}

// An HLS rendition goes once its slug has no row and all of its files are past
// the grace period; renditions of live (or trashed) files, fresh renditions and
// anything under hls/ that is not laid out like a rendition stay.
func TestScanOrphansCollectsRenditionsOfMissingFiles(t *testing.T) {
	b := newBlobTree(t)
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	hls := func(slug string) string { return upload.HLSPrefix(slug, upload.StorageKey(slug, ".mp4")) }
	// A date slug is sharded by its random tail now, and by its first four
	// characters under the scheme before; renditions keep their old shard.
	const dated = "2026-07-19-aB3kdq"
	for _, dir := range []string{
		hls("livevid"), hls("gonevid"), hls("newvid"), hls("abc"), hls(dated), legacyHLSPrefix(dated),
	} {
		b.put(dir+"/index.m3u8", old)
		b.put(dir+"/seg000.ts", old)
	}
	b.put(hls("newvid")+"/seg001.ts", now) // still being published
	b.put("hls/stray.ts", old)
	b.put("hls/ab/cd/notaslugdir/x/seg.ts", old)
	b.put("hls/ab/seg.ts", old) // inside a shard directory, not a rendition

	st := fakeOrphanStore{slugs: map[string]struct{}{"livevid": {}}}
	scanOrphans(context.Background(), st, b.st, now)

	for _, gone := range []string{
		hls("gonevid") + "/index.m3u8", hls("abc") + "/seg000.ts",
		hls(dated) + "/index.m3u8", legacyHLSPrefix(dated) + "/index.m3u8",
	} {
		if b.has(gone) {
			t.Errorf("%s survived although its file is gone", gone)
		}
	}
	for _, kept := range []string{
		hls("livevid") + "/index.m3u8",
		hls("newvid") + "/index.m3u8",
		"hls/stray.ts",
		"hls/ab/cd/notaslugdir/x/seg.ts",
		"hls/ab/seg.ts",
	} {
		if !b.has(kept) {
			t.Errorf("%s was collected", kept)
		}
	}
}

func TestScanOrphansStopsWhenCancelled(t *testing.T) {
	b := newBlobTree(t)
	b.put("ab/cd/orphan.png", time.Now().Add(-48*time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	scanOrphans(ctx, fakeOrphanStore{}, b.st, time.Now())
	if !b.has("ab/cd/orphan.png") {
		t.Fatal("a cancelled scan still deleted blobs")
	}
}

// The scan checks keys a batch at a time and cuts rendition batches only
// between directories: with tiny batches, the outcome is the same as one pass.
func TestScanOrphansInBatches(t *testing.T) {
	oldKeys, oldDirs := orphanKeyBatch, orphanDirBatch
	orphanKeyBatch, orphanDirBatch = 2, 1
	t.Cleanup(func() { orphanKeyBatch, orphanDirBatch = oldKeys, oldDirs })

	b := newBlobTree(t)
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	known := map[string]struct{}{}
	for i := 0; i < 7; i++ {
		k := fmt.Sprintf("ab/cd/known%d.png", i)
		b.put(k, old)
		known[k] = struct{}{}
		b.put(fmt.Sprintf("ab/cd/orphan%d.png", i), old)
	}
	hls := func(slug string) string { return upload.HLSPrefix(slug, upload.StorageKey(slug, ".mp4")) }
	for _, slug := range []string{"livevid", "gonevid"} {
		for i := 0; i < 3; i++ {
			b.put(fmt.Sprintf("%s/seg%03d.ts", hls(slug), i), old)
		}
	}
	b.put(hls("gonevid")+"/seg003.ts", now) // one fresh segment keeps the rendition

	var queries, maxKeys int
	st := fakeOrphanStore{keys: known, slugs: map[string]struct{}{"livevid": {}}, keyQueries: &queries, maxKeys: &maxKeys}
	scanOrphans(context.Background(), st, b.st, now)

	for i := 0; i < 7; i++ {
		if !b.has(fmt.Sprintf("ab/cd/known%d.png", i)) {
			t.Errorf("known%d was collected", i)
		}
		if b.has(fmt.Sprintf("ab/cd/orphan%d.png", i)) {
			t.Errorf("orphan%d survived", i)
		}
	}
	if !b.has(hls("livevid")+"/seg000.ts") || !b.has(hls("gonevid")+"/seg000.ts") {
		t.Error("a rendition was judged on part of its segments")
	}
	if queries < 7 || maxKeys > 2 {
		t.Errorf("%d key queries of up to %d keys, want batches of at most 2", queries, maxKeys)
	}
}
