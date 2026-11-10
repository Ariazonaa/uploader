package main

import (
	"context"
	"errors"
	"log"
	"path"
	"strings"
	"time"

	"uploader/internal/storage"
	"uploader/internal/upload"
)

// orphanGrace is how long a blob with no database reference is left alone before
// the janitor collects it, so a write that is still being recorded is not raced.
const orphanGrace = time.Hour

// orphanScanEvery is how often the janitor walks the storage tree for orphans.
// Orphans only come from crashes and interrupted purges, so they are rare, and
// the walk touches every blob on disk: far too much work for the 15-minute
// purge cadence.
const orphanScanEvery = 6 * time.Hour

// Batch sizes of the orphan scan's database checks. Variables so tests can
// make several batches out of a small tree.
var (
	// orphanKeyBatch is how many blob keys are checked per query.
	orphanKeyBatch = 500
	// orphanDirBatch is how many HLS rendition directories (with all their
	// segment keys) are held before their slugs are checked.
	orphanDirBatch = 100
)

// The orphan scan deletes what the database does not know, so a database that
// knows nothing (a fresh one, started before the dump was restored) or too
// little (an older dump) makes the whole blob store look orphaned. Real orphans
// come from crashes and interrupted purges and are few. So a pass that finds
// at least orphanMassMin orphans making up more than half of everything it
// looked at deletes nothing and says so; UPLOADER_ORPHAN_SCAN_FORCE=true lifts
// the check for the rare legitimate case. Variables so tests can lower them.
var (
	orphanMassMin = 50
	// orphanHoldMax bounds the orphans one pass remembers before it decides
	// (deletion waits for the end of the walk, when the ratio is known). More
	// than this many is refused like a majority.
	orphanHoldMax = 20000
)

// orphanStore is the part of *store.Store the orphan scan reads.
type orphanStore interface {
	ReferencedKeys(ctx context.Context, keys []string) (map[string]struct{}, error)
	ExistingSlugs(ctx context.Context, slugs []string) (map[string]struct{}, error)
}

// errOrphanOverflow ends the walk once more orphans were found than a guarded
// pass holds.
var errOrphanOverflow = errors.New("too many orphans")

// orphanSweep collects what one pass found orphaned. A blob and an HLS
// rendition directory each count as one unit.
type orphanSweep struct {
	force bool
	blobs storage.Storage
	total int      // units looked at
	found int      // of those, orphaned and past the grace period
	keys  []string // orphaned blobs, held until the pass decides
	dirs  []string // orphaned rendition directories, likewise
}

// orphan records one orphaned blob (dir false) or rendition directory. Under
// force it is deleted at once; otherwise it is held for the decision at the
// end of the pass.
func (o *orphanSweep) orphan(ctx context.Context, key string, dir bool) error {
	o.found++
	switch {
	case o.force && dir:
		_ = o.blobs.DeletePrefix(ctx, key)
	case o.force:
		_ = o.blobs.Delete(ctx, key)
	case o.found > orphanHoldMax:
		return errOrphanOverflow
	case dir:
		o.dirs = append(o.dirs, key)
	default:
		o.keys = append(o.keys, key)
	}
	return nil
}

// suspicious reports whether the orphans found look like a database that does
// not belong to this blob store rather than like leftovers.
func (o *orphanSweep) suspicious() bool {
	return o.found > orphanHoldMax || (o.found >= orphanMassMin && o.found*2 > o.total)
}

// scanOrphans removes blobs on disk that no DB row references anymore (e.g.
// after a crash between blob write and row insert), and HLS renditions whose
// file row is gone. Best-effort; a pass that is cancelled or whose database
// check fails deletes nothing. snapshotAt must be taken before the database is
// read. Unless force is set, a pass that finds most of the store orphaned
// deletes nothing either (see orphanMassMin).
//
// The tree is streamed, not listed: keys are checked against the database a
// batch at a time, and only the few that no row references are stat'ed. The
// scan used to load every key of the table and of the disk into memory and
// stat every HLS segment on each pass.
func scanOrphans(ctx context.Context, st orphanStore, blobs storage.Storage, snapshotAt time.Time, force bool) {
	// A freshly written blob may belong to a row that is about to reference
	// it: media post-processing stores the thumbnail first and only then sets
	// thumb_key. Scanning in that window used to delete the new thumbnail and
	// leave the database pointing at a missing file. So only blobs that were
	// already older than the grace period when the snapshot was taken are
	// collected. Measuring the age when each blob is reached instead let a
	// scan that outlasted the grace period (a large tree on slow disks) delete
	// blobs written after the snapshot, whose rows it could not contain. The
	// batched checks below all read the database after snapshotAt, so they
	// see every row a blob older than the cutoff could belong to.
	cutoff := snapshotAt.Add(-orphanGrace)
	sweep := &orphanSweep{force: force, blobs: blobs}
	var batch []string
	renditions := map[string][]string{} // rendition dir → its keys

	checkBlobs := func() error {
		if len(batch) == 0 {
			return nil
		}
		referenced, err := st.ReferencedKeys(ctx, batch)
		if err != nil {
			return err
		}
		for _, k := range batch {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			sweep.total++
			if _, ok := referenced[k]; ok {
				continue
			}
			if olderThan(ctx, blobs, k, cutoff) {
				if err := sweep.orphan(ctx, k, false); err != nil {
					return err
				}
			}
		}
		batch = batch[:0]
		return nil
	}
	checkRenditions := func() error {
		err := scanOrphanRenditions(ctx, st, blobs, renditions, cutoff, sweep)
		renditions = map[string][]string{}
		return err
	}

	err := blobs.Walk(ctx, func(k string) error {
		if dir, ok := renditionDir(k); ok {
			// The walk is in lexical order, so a directory's segments arrive
			// together. A batch is only cut between directories, never inside
			// one, or a rendition would be judged on part of its files.
			if _, seen := renditions[dir]; !seen && len(renditions) >= orphanDirBatch {
				if err := checkRenditions(); err != nil {
					return err
				}
			}
			renditions[dir] = append(renditions[dir], k)
			return nil
		}
		if strings.HasPrefix(k, "hls/") {
			return nil // not laid out like a rendition; not ours to judge
		}
		batch = append(batch, k)
		if len(batch) >= orphanKeyBatch {
			return checkBlobs()
		}
		return nil
	})
	if err == nil {
		err = checkBlobs()
	}
	if err == nil {
		err = checkRenditions()
	}
	if force {
		return // deleted along the way
	}
	if errors.Is(err, errOrphanOverflow) || (err == nil && sweep.suspicious()) {
		log.Printf("orphan scan: REFUSING to delete: %d of the %d blobs checked so far have no database row. "+
			"That looks like an empty, older or wrong database rather than leftovers, so nothing was deleted. "+
			"If the database is the right one, start once with UPLOADER_ORPHAN_SCAN_FORCE=true to collect them.",
			sweep.found, sweep.total)
		return
	}
	if err != nil {
		return // an incomplete pass cannot judge the ratio
	}
	for _, k := range sweep.keys {
		if ctx.Err() != nil {
			return
		}
		_ = blobs.Delete(ctx, k)
	}
	for _, dir := range sweep.dirs {
		if ctx.Err() != nil {
			return
		}
		_ = blobs.DeletePrefix(ctx, dir)
	}
}

// scanOrphanRenditions reports to sweep the HLS rendition directories whose
// slug has no file row (trashed files keep theirs) once every file in them is
// past cutoff.
// Renditions are derived from a file and referenced by slug, not storage key.
// Every purge deletes them, but a purge cut short between deleting the rows and
// their blobs, or a transcode whose final row check failed, left them behind
// for good while the scan skipped hls/ entirely. Only the segments of dead
// renditions are stat'ed.
func scanOrphanRenditions(ctx context.Context, st orphanStore, blobs storage.Storage, renditions map[string][]string, cutoff time.Time, sweep *orphanSweep) error {
	if len(renditions) == 0 {
		return nil
	}
	slugs := make([]string, 0, len(renditions))
	for dir := range renditions {
		slugs = append(slugs, path.Base(dir))
	}
	live, err := st.ExistingSlugs(ctx, slugs)
	if err != nil {
		return err
	}
	for dir, keys := range renditions {
		sweep.total++
		if _, ok := live[path.Base(dir)]; ok {
			continue
		}
		stale := true
		for _, k := range keys {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !olderThan(ctx, blobs, k, cutoff) {
				stale = false
				break
			}
		}
		if stale {
			if err := sweep.orphan(ctx, dir, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// renditionDir returns the HLS rendition directory key sits in, but only when
// that directory is exactly where a rendition of its own name lives: under the
// shard of a storage key created with the current scheme, or with the one
// before slugs were sharded by their random tail (renditions keep the shard
// their file was created with). Anything else under hls/ is never deleted by
// prefix: a stray hls/x.ts must not make the whole hls/ tree, or one shard
// directory, look like one orphaned rendition.
func renditionDir(key string) (string, bool) {
	if !strings.HasPrefix(key, "hls/") {
		return "", false
	}
	dir := path.Dir(key)
	slug := path.Base(dir)
	return dir, dir == upload.HLSPrefix(slug, upload.StorageKey(slug, "")) || dir == legacyHLSPrefix(slug)
}

// legacyHLSPrefix is upload.HLSPrefix under the original shard scheme: the
// slug's first four characters, whatever kind of slug it is.
func legacyHLSPrefix(slug string) string {
	if len(slug) < 4 {
		return path.Join("hls", "_", slug)
	}
	return path.Join("hls", slug[0:2], slug[2:4], slug)
}

// olderThan reports whether key was last modified before cutoff. A blob that
// cannot be stat'ed counts as new, so it is never deleted on a guess.
func olderThan(ctx context.Context, blobs storage.Storage, key string, cutoff time.Time) bool {
	st, err := blobs.Stat(ctx, key)
	return err == nil && st.ModTime.Before(cutoff)
}
