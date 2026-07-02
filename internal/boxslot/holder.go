package boxslot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

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
	body, found := strings.CutPrefix(name, holderPrefix+"pid")
	if !found {
		return 0, time.Time{}, false
	}
	body, found = strings.CutSuffix(body, ".lock")
	if !found {
		return 0, time.Time{}, false
	}
	pidPart, rest, found := strings.Cut(body, "-")
	if !found {
		return 0, time.Time{}, false
	}
	nanoPart, _, found := strings.Cut(rest, "-")
	if !found {
		return 0, time.Time{}, false
	}
	pid, err := strconv.Atoi(pidPart)
	if err != nil || pid <= 0 {
		return 0, time.Time{}, false
	}
	nano, err := strconv.ParseInt(nanoPart, 10, 64)
	if err != nil {
		return 0, time.Time{}, false
	}
	return pid, time.Unix(0, nano), true
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
