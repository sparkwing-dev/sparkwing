package boxslot

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrHolderLive is returned by ReleaseHolder when the named marker's
// owner still holds its flock and force was not set.
var ErrHolderLive = errors.New("boxslot: holder is live")

// Holder describes one holder marker in the lock dir, as reported by
// [Holders]. Zero PID / ClaimedAt mean the filename didn't carry the
// pid<PID>-<unixNano> shape (a hand-made or truncated marker) -- the
// flock is the authority, the name is metadata.
type Holder struct {
	// PID is the owner process id parsed from the marker filename.
	PID int
	// ClaimedAt is the slot claim time parsed from the marker filename.
	ClaimedAt time.Time
	// RunID is the run recorded by [AnnotateHolder]; empty until the
	// owner annotates. The last run= line wins when the owner ran
	// several pipelines under one slot.
	RunID string
	// Path is the marker's absolute location.
	Path string
	// Live reports whether the owner still holds its flock: a failed
	// non-blocking flock probe means the owner is alive, a successful
	// one means the kernel released the lock on process death.
	Live bool
}

// Holders reports every holder marker in lockDir without mutating it,
// filesystem and flock only -- usable while the state backend is
// unavailable. An absent lockDir reports no holders. Markers are
// ordered oldest claim first.
func Holders(lockDir string) ([]Holder, error) {
	entries, err := os.ReadDir(lockDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var holders []Holder
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), holderPrefix) {
			continue
		}
		path := filepath.Join(lockDir, e.Name())
		h := Holder{Path: path}
		h.PID, h.ClaimedAt, _ = parseHolderName(e.Name())
		if b, err := os.ReadFile(path); err == nil {
			h.RunID = lastRunLine(b)
		}
		live, err := probeHolderLive(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		h.Live = live
		holders = append(holders, h)
	}
	sort.Slice(holders, func(i, j int) bool {
		if !holders[i].ClaimedAt.Equal(holders[j].ClaimedAt) {
			return holders[i].ClaimedAt.Before(holders[j].ClaimedAt)
		}
		return holders[i].Path < holders[j].Path
	})
	return holders, nil
}

// lastRunLine extracts the run id from the last run= line of a holder
// marker's contents; empty when the marker was never annotated.
func lastRunLine(b []byte) string {
	run := ""
	for _, line := range strings.Split(string(b), "\n") {
		if v, found := strings.CutPrefix(line, "run="); found {
			run = strings.TrimSpace(v)
		}
	}
	return run
}

// ReleaseHolder removes the holder marker named name (a basename, not a
// path) from lockDir, serialized against admission via coord.lock and
// touching only the filesystem/flock layer -- it works while the state
// backend is wedged. A stale marker (owner's flock released by the
// kernel on death) is removed outright. A live marker is refused with
// [ErrHolderLive] unless force is set; with force the owner pid parsed
// from the filename is SIGKILLed first and the marker then removed. The
// kill is guarded against pid recycling: it fires only when the named
// marker is byte-identical to that pid's newest marker, so a marker
// left by a dead process whose pid was reused never targets the reuser.
func ReleaseHolder(lockDir, name string, force bool) error {
	if name != filepath.Base(name) || !strings.HasPrefix(name, holderPrefix) || !strings.HasSuffix(name, ".lock") {
		return fmt.Errorf("boxslot: %q is not a holder marker basename", name)
	}
	coord, err := openCoord(lockDir)
	if err != nil {
		return err
	}
	defer coord.Close()
	if err := flockExclusive(coord); err != nil {
		return fmt.Errorf("boxslot: coord flock: %w", err)
	}
	defer func() { _ = flockUnlock(coord) }()

	path := filepath.Join(lockDir, name)
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := flockExclusiveNonblock(f); err == nil {
		_ = flockUnlock(f)
		return os.Remove(path)
	}
	if !force {
		return fmt.Errorf("%w: %s", ErrHolderLive, name)
	}
	pid, _, ok := parseHolderName(name)
	if !ok {
		return fmt.Errorf("boxslot: cannot parse an owner pid from %q; refusing to kill", name)
	}
	named, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	newest, found, err := newestHolderNameForPID(lockDir, pid)
	if err != nil {
		return err
	}
	if !found || newest != name {
		return fmt.Errorf("boxslot: %s is not pid %d's newest marker; refusing to kill (pid recycled?)", name, pid)
	}
	current, err := os.ReadFile(filepath.Join(lockDir, newest))
	if err != nil {
		return err
	}
	if !bytes.Equal(named, current) {
		return fmt.Errorf("boxslot: %s changed under us; refusing to kill (pid recycled?)", name)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("boxslot: find pid %d: %w", pid, err)
	}
	if err := proc.Kill(); err != nil {
		return fmt.Errorf("boxslot: kill pid %d: %w", pid, err)
	}
	return os.Remove(path)
}

// AnnotateHolder appends a "run=<runID>" line to the calling process's
// holder marker in lockDir, so a wedged holder can be traced to its run
// by reading the marker file. Admission happens before the orchestrator
// mints the run id, hence the two-step write: Acquire creates the marker
// with the pid/start line, AnnotateHolder adds the run line once the id
// exists. When this process owns several markers the newest is
// annotated; a process that runs several pipelines under one slot
// appends one line per run, and readers take the last. Callers treat
// failure as diagnostics-only -- an unannotated marker still admits and
// releases normally.
func AnnotateHolder(lockDir, runID string) error {
	if runID == "" {
		return errors.New("boxslot: runID required")
	}
	name, ok, err := newestHolderNameForPID(lockDir, os.Getpid())
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("boxslot: no holder marker for pid %d in %s", os.Getpid(), lockDir)
	}
	f, err := os.OpenFile(filepath.Join(lockDir, name), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := fmt.Fprintf(f, "run=%s\n", runID)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// parseHolderName extracts the owner pid and claim time from a holder
// marker filename of the shape holder-pid<PID>-<unixNano>-<seq>.lock.
// ok is false for markers that don't carry the shape (hand-made or
// truncated files), which still hold or free a slot -- the flock is the
// authority, the name is metadata.
func parseHolderName(name string) (pid int, claimedAt time.Time, ok bool) {
	pid, nano, _, ok := parseMarkerName(name, holderPrefix)
	if !ok {
		return 0, time.Time{}, false
	}
	return pid, time.Unix(0, nano), true
}

// parseMarkerName decomposes a lock-file name of the shape
// <prefix>pid<PID>-<unixNano>-<seq>.lock into its parts, shared by the holder
// and waiter markers createLockFile emits. ok is false for a name that
// doesn't carry the shape; the flock is the authority, the name is metadata.
func parseMarkerName(name, prefix string) (pid int, nano int64, seq uint64, ok bool) {
	body, found := strings.CutPrefix(name, prefix+"pid")
	if !found {
		return 0, 0, 0, false
	}
	body, found = strings.CutSuffix(body, ".lock")
	if !found {
		return 0, 0, 0, false
	}
	pidPart, rest, found := strings.Cut(body, "-")
	if !found {
		return 0, 0, 0, false
	}
	nanoPart, seqPart, found := strings.Cut(rest, "-")
	if !found {
		return 0, 0, 0, false
	}
	pid, err := strconv.Atoi(pidPart)
	if err != nil || pid <= 0 {
		return 0, 0, 0, false
	}
	nano, err = strconv.ParseInt(nanoPart, 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	seq, err = strconv.ParseUint(seqPart, 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	return pid, nano, seq, true
}

// newestHolderNameForPID scans lockDir for holder markers owned by pid
// and returns the one with the latest claim time in its name. ok is
// false when pid owns no marker.
func newestHolderNameForPID(lockDir string, pid int) (name string, ok bool, err error) {
	entries, err := os.ReadDir(lockDir)
	if err != nil {
		return "", false, err
	}
	prefix := fmt.Sprintf("%spid%d-", holderPrefix, pid)
	var newest string
	var newestAt time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		_, claimedAt, parsed := parseHolderName(e.Name())
		if !parsed {
			continue
		}
		if newest == "" || claimedAt.After(newestAt) {
			newest = e.Name()
			newestAt = claimedAt
		}
	}
	return newest, newest != "", nil
}
