//go:build integration

package store_test

import "testing"

// Trashed files still own their slug (and their HLS rendition); purged ones
// do not.
func TestExistingSlugs(t *testing.T) {
	e := newEnv(t)
	u := e.user("owner", false)
	e.file(u.ID, "live")
	trashed := e.file(u.ID, "trashed")
	purged := e.file(u.ID, "purged")
	if _, err := e.st.DeleteFile(e.ctx, trashed.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.st.PurgeFileByID(e.ctx, purged.ID); err != nil {
		t.Fatal(err)
	}

	got, err := e.st.ExistingSlugs(e.ctx, []string{"live", "trashed", "purged", "never"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ExistingSlugs = %v, want live and trashed", got)
	}
	for _, s := range []string{"live", "trashed"} {
		if _, ok := got[s]; !ok {
			t.Errorf("%s missing from %v", s, got)
		}
	}
	if got, err := e.st.ExistingSlugs(e.ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("ExistingSlugs(nil) = %v, %v", got, err)
	}
}
