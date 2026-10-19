//go:build linux

package main

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

const runUID, runGID = 10001, 10001

// dropPrivileges, when the process starts as root, ensures the data directory is
// writable by the unprivileged runtime user and then permanently drops to that
// uid/gid for the rest of the process. It is a no-op when already non-root.
//
// This replaces the external su-exec dependency (which required apk at build
// time) so the runtime image needs no extra packages.
func dropPrivileges(dataDir string) error {
	if syscall.Geteuid() != 0 {
		return nil // already unprivileged
	}
	// #nosec G703 -- dataDir is the operator-configured storage root
	// (UPLOADER_STORAGE_FILE_ROOT), never request data.
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	// Only chown when ownership is actually wrong — a recursive chown over a
	// large data set on every boot would be slow. The root's owner is the
	// marker, which is why chownTree hands it over last.
	fi, err := os.Stat(dataDir) // #nosec G703 -- operator-configured, see above
	if err != nil {
		return fmt.Errorf("stat data dir: %w", err)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != runUID {
		fmt.Printf("uploader: %s is not owned by uid %d, changing ownership recursively "+
			"(this can take a while on a large data set)\n", dataDir, runUID)
		if err := chownTree(dataDir); err != nil {
			return fmt.Errorf("chown data dir: %w", err)
		}
	}
	// Order matters: supplementary groups and the group before the user (after
	// setuid we can no longer change either). Setgid alone leaves root's
	// supplementary groups (root, bin, daemon, ... from /etc/group) attached to
	// the process, so uid 10001 would still pass group-permission checks meant
	// for them.
	if err := syscall.Setgroups([]int{runGID}); err != nil {
		return fmt.Errorf("setgroups: %w", err)
	}
	if err := syscall.Setgid(runGID); err != nil {
		return fmt.Errorf("setgid: %w", err)
	}
	if err := syscall.Setuid(runUID); err != nil {
		return fmt.Errorf("setuid: %w", err)
	}
	// Prove the outcome as the user that will actually serve. A data dir this
	// user cannot write to used to boot anyway and only surfaced as a 500 on
	// the first upload, while the container reported itself healthy.
	if err := checkWritable(dataDir); err != nil {
		return fmt.Errorf("data dir %s is not writable as uid %d: %w", dataDir, runUID, err)
	}
	return nil
}

// checkWritable creates and removes a scratch file in dir. The dot prefix keeps
// it out of the orphan scan should the process die in between.
func checkWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".writecheck-*")
	if err != nil {
		return err
	}
	closeErr := f.Close()
	// #nosec G703 -- the name CreateTemp just generated inside the
	// operator-configured data dir.
	if err := os.Remove(f.Name()); err != nil {
		return err
	}
	return closeErr
}

// chownTree hands every entry under dataDir to the runtime user.
//
// This walk runs as ROOT over a directory that the unprivileged service itself
// writes to, so it must not be talked into touching anything outside it. The
// previous version used filepath.WalkDir plus os.Chown, and os.Chown follows
// symlinks: a compromised uploader process could drop a symlink into the data
// directory and have the next restart hand the link's target — any file the
// container can see, including through a bind mount — to uid 10001.
//
// Two defences now. The tree is opened as an os.Root, which refuses to resolve
// a path that escapes it, and ownership is changed with Lchown, which acts on
// the symlink itself rather than what it points at. fs.WalkDir does not descend
// into symlinks either, so a planted link is a leaf that gets its own metadata
// changed and nothing else.
//
// The root itself goes last. dropPrivileges only walks when the root is owned
// by someone else, so chowning it first (as fs.WalkDir's pre-order did) meant a
// walk cut short — the container stopped during a long first boot — was never
// resumed and left the rest of the tree root-owned for good.
//
// Per-entry failures stay best-effort — one unreadable file must not stop the
// service from booting — but are counted and logged. Failing to open the tree
// or to chown the root is fatal, like the MkdirAll above: without a usable data
// directory every upload would 500.
func chownTree(dataDir string) error {
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return err
	}
	defer root.Close()

	failed := 0
	var firstErr error
	note := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
		failed++
	}
	_ = fs.WalkDir(root.FS(), ".", func(p string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			note(walkErr)
			return nil
		}
		if p == "." {
			return nil
		}
		if err := root.Lchown(p, runUID, runGID); err != nil {
			note(err)
		}
		return nil
	})
	if failed > 0 {
		fmt.Printf("uploader: warning — could not change ownership of %d entries under %s (first error: %v)\n",
			failed, dataDir, firstErr)
	}
	return root.Lchown(".", runUID, runGID)
}
