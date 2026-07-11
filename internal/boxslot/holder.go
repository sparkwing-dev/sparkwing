package boxslot

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// holderPrefix scopes the per-process lock files a holder creates, so a
// scan doesn't trip on the coordination file or any other file an older
// binary dropped into the directory.
const holderPrefix = "holder-"

// Holder describes one holder marker in the lock dir, as reported by
// [Holders]. Zero PID / ClaimedAt mean the filename didn't carry the
// pid<PID>-<unixNano> shape (a hand-made or truncated marker) -- the
// flock is the authority, the name is metadata.
type Holder struct {
	// PID is the owner process id parsed from the marker filename.
	PID int
	// ClaimedAt is the slot claim time parsed from the marker filename.
	ClaimedAt time.Time
	// RunID is the run the owner recorded in the marker; empty until the
	// owner annotated one. The last run= line wins when the owner ran
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
// filesystem and flock only. An absent lockDir reports no holders.
// Markers are ordered oldest claim first.
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
// <prefix>pid<PID>-<unixNano>-<seq>.lock into its parts. ok is false for
// a name that doesn't carry the shape; the flock is the authority, the
// name is metadata.
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
