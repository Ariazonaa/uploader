package upload

import (
	"context"
	"log"

	"uploader/internal/db/store"
	"uploader/internal/storage"
)

// PurgeBlobs removes the blob, thumbnail and HLS rendition of a file whose row
// has just been deleted.
//
// The slug is free again from the moment that delete commits, and every one of
// these keys derives from it. An upload that claimed the slug in between (a
// custom slug, typically) already has its own row and writes its blob, and
// later its thumbnail and rendition, under the same keys; deleting them would
// destroy the new file. So once the slug is taken again only a storage key the
// new row does not use (another extension) is deleted. A skipped blob or
// thumbnail the new file never reuses is unreferenced, so the orphan scan
// collects it; leftover HLS segments go with the new file's own purge.
func PurgeBlobs(ctx context.Context, st *store.Store, blobs storage.Storage, f store.File) {
	ownerKey, taken, err := st.SlugStorageKey(ctx, f.Slug)
	if err != nil {
		// Keep the blobs rather than risk deleting another file's.
		log.Printf("purge %s: check whether the slug was reused: %v; blobs kept", f.Slug, err)
		return
	}
	if !taken || ownerKey != f.StorageKey {
		_ = blobs.Delete(ctx, f.StorageKey)
	}
	if taken {
		return
	}
	if f.ThumbKey != nil {
		_ = blobs.Delete(ctx, *f.ThumbKey)
	}
	// Always, not only when has_hls is set: the flag is written after the
	// rendition is published, so a file purged while its transcode is still
	// running reads false here although segments may already exist. (The HLS
	// job re-checks the row after publishing and cleans up if it lost the race.)
	_ = blobs.DeletePrefix(ctx, HLSPrefix(f.Slug, f.StorageKey))
}
