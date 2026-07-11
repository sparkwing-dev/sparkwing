// Package boxslot inspects the host-local box-slot lock directory left
// on disk by older sparkwing binaries. Host admission is owned by the
// local admission daemon; nothing in the current run path acquires a box
// slot. What remains is a read-only view of any lock files an
// older-pinned pipeline binary is still holding, plus [PurgeIfIdle] to
// clear the directory once no owner is live.
//
// A holder owns a lock file named holder-<nonce>.lock under
// [Paths.BoxSlotDir] and keeps an exclusive flock on it for its
// lifetime. The OS releases the flock when the process exits, even on
// crash or SIGKILL, so a free flock proves the original owner is gone.
// [Holders] reports every marker with that liveness verdict without
// mutating the directory; [PurgeIfIdle] removes the directory only when
// no marker's flock is still held.
package boxslot

import (
	"errors"
	"os"
	"path/filepath"
)

// PurgeIfIdle removes the box-slot lock directory when no holder is
// live, and returns the live holders instead when any remain. A held
// flock means an older-pinned pipeline binary is still admitting outside
// the daemon, so its marker -- and the directory -- are left untouched
// and reported. With no live holder every file is provably dead: the
// stale markers, the coordination file, and any control file are
// removed and the directory is deleted. An absent directory is a no-op.
// removed counts the files deleted.
func PurgeIfIdle(lockDir string) (removed int, live []Holder, err error) {
	holders, err := Holders(lockDir)
	if err != nil {
		return 0, nil, err
	}
	for _, h := range holders {
		if h.Live {
			live = append(live, h)
		}
	}
	if len(live) > 0 {
		return 0, live, nil
	}
	entries, err := os.ReadDir(lockDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil, nil
		}
		return 0, nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(lockDir, e.Name())); err == nil {
			removed++
		}
	}
	_ = os.Remove(lockDir)
	return removed, nil, nil
}

// probeHolderLive opens path and takes a non-blocking flock probe:
// failure to lock means the owner still holds the file (live), success
// means the kernel released it on owner death (stale). The probe lock
// is dropped immediately. Errors (including a missing file) propagate so
// the caller can refuse to act on a marker it can no longer see.
func probeHolderLive(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if err := flockExclusiveNonblock(f); err != nil {
		return true, nil
	}
	_ = flockUnlock(f)
	return false, nil
}
