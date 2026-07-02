package boxslot

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// StallTTLEnvVar names the environment override for how long a live
// holder may sit silent before the sweep reports it stalled. The value
// is a Go duration string ("30m", "1h"). Unset or empty keeps
// [DefaultStallTTL]; anything else must parse or the caller fails
// loudly, naming the variable and value -- a typo'd override silently
// reverting to the default would hide the misconfiguration.
const StallTTLEnvVar = "SPARKWING_BOX_SLOT_STALL_TTL"

// DefaultStallTTL is the silence threshold applied when
// [StallTTLEnvVar] is unset. The envelope moves on run-level events
// and stdout tees, so half an hour of envelope silence from a live
// holder usually marks a process that is alive but doing nothing --
// the wedge shape. A run inside one output-quiet node that runs
// longer than the ttl reads the same way; operators with such nodes
// raise [StallTTLEnvVar] above the longest expected quiet stretch.
const DefaultStallTTL = 30 * time.Minute

// StallTTL resolves the stalled-holder threshold: [StallTTLEnvVar]
// when set and parseable, [DefaultStallTTL] otherwise. A
// set-but-unparseable value is an error, never a silent fallback.
func StallTTL() (time.Duration, error) {
	raw := os.Getenv(StallTTLEnvVar)
	if raw == "" {
		return DefaultStallTTL, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: want a Go duration such as \"30m\"", StallTTLEnvVar, raw)
	}
	return d, nil
}

// envelopeFileName is the run-level event log's basename under
// <runsDir>/<runID>/. Owned by the orchestrator's paths layout; the
// guard test in this package pins the two against drifting apart.
const envelopeFileName = "_envelope.ndjson"

// stallProbeInterval is how often Acquire's wait loop re-runs the
// stalled-holder sweep. The first waiting iteration probes
// immediately so a queued run names its blocker without a half-minute
// of silence; the interval then caps the extra directory scans on a
// long wait.
const stallProbeInterval = 30 * time.Second

// StalledHolder describes one live holder whose run has gone silent
// past the ttl, as reported by [SweepStalled].
type StalledHolder struct {
	// Holder is the underlying marker descriptor; Live is always true
	// for a stalled holder.
	Holder
	// Age is how long the holder has been silent: since the envelope's
	// last write when a run is annotated, since the claim otherwise.
	Age time.Duration
	// Evidence is the human-readable reason the holder counts as
	// stalled, naming the file and timestamp the verdict rests on.
	Evidence string
	// NewestFile is the most recently written file under the annotated
	// run's directory -- corroborating operator evidence, never part of
	// the stall verdict. A truly wedged run's files all sit old; a run
	// inside one long output-quiet node keeps writing node files while
	// its envelope sits still. Empty when no run is annotated or the
	// run directory is unreadable.
	NewestFile string
	// NewestFileAge is how long ago NewestFile was written; meaningful
	// only when NewestFile is non-empty.
	NewestFileAge time.Duration
}

// SweepStalled reports every live holder in lockDir that looks wedged,
// reading only the filesystem and flock state -- usable while the
// state database is locked by the very process being diagnosed. A live
// holder is stalled when its annotated run's envelope under runsDir
// has an mtime older than ttl (the envelope moves on run-level events
// and stdout tees, so envelope silence usually means no progress --
// though one long output-quiet node reads the same way; NewestFile
// corroborates), or, with no run annotated, when
// the claim time in its filename is older than ttl (admitted 30+
// minutes ago and never started a run). Dead holders and markers whose
// filename carries no claim time are skipped -- admission GC and
// box-slots release own those. SweepStalled never signals or removes
// anything; pass a result to [ReapStalled] for that.
func SweepStalled(lockDir, runsDir string, ttl time.Duration) ([]StalledHolder, error) {
	if ttl <= 0 {
		return nil, fmt.Errorf("boxslot: stall ttl must be positive, got %s", ttl)
	}
	holders, err := Holders(lockDir)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var stalled []StalledHolder
	for _, h := range holders {
		if !h.Live {
			continue
		}
		s, ok := classifyStalled(h, runsDir, ttl, now)
		if ok {
			stalled = append(stalled, s)
		}
	}
	return stalled, nil
}

// classifyStalled applies the staleness rules to one live holder.
func classifyStalled(h Holder, runsDir string, ttl time.Duration, now time.Time) (StalledHolder, bool) {
	if h.RunID != "" {
		envelope := filepath.Join(runsDir, h.RunID, envelopeFileName)
		if fi, err := os.Stat(envelope); err == nil {
			age := now.Sub(fi.ModTime())
			if age <= ttl {
				return StalledHolder{}, false
			}
			s := StalledHolder{
				Holder: h,
				Age:    age,
				Evidence: fmt.Sprintf("run %s live but its envelope %s last written %s ago (ttl %s)",
					h.RunID, envelope, age.Round(time.Second), ttl),
			}
			s.NewestFile, s.NewestFileAge = newestRunFile(runsDir, h.RunID, now)
			return s, true
		}
		if h.ClaimedAt.IsZero() {
			return StalledHolder{}, false
		}
		age := now.Sub(h.ClaimedAt)
		if age <= ttl {
			return StalledHolder{}, false
		}
		s := StalledHolder{
			Holder: h,
			Age:    age,
			Evidence: fmt.Sprintf("run %s annotated but envelope %s is missing; slot claimed %s ago (ttl %s)",
				h.RunID, envelope, age.Round(time.Second), ttl),
		}
		s.NewestFile, s.NewestFileAge = newestRunFile(runsDir, h.RunID, now)
		return s, true
	}
	if h.ClaimedAt.IsZero() {
		return StalledHolder{}, false
	}
	age := now.Sub(h.ClaimedAt)
	if age <= ttl {
		return StalledHolder{}, false
	}
	return StalledHolder{
		Holder: h,
		Age:    age,
		Evidence: fmt.Sprintf("no run annotated; slot claimed %s ago (ttl %s)",
			age.Round(time.Second), ttl),
	}, true
}

// newestRunFileWalkLimit caps how many directory entries
// [newestRunFile] visits, so a pathological run directory cannot
// stall a sweep that exists to diagnose stalls.
const newestRunFileWalkLimit = 4096

// newestRunFile reports the most recently written regular file under
// <runsDir>/<runID>/ and its mtime age at now. Best-effort operator
// evidence: unreadable entries are skipped and a missing run
// directory reports empty, never an error.
func newestRunFile(runsDir, runID string, now time.Time) (string, time.Duration) {
	var newest string
	var newestMod time.Time
	visited := 0
	root := filepath.Join(runsDir, runID)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fs.SkipDir
		}
		visited++
		if visited > newestRunFileWalkLimit {
			return fs.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		if newest == "" || fi.ModTime().After(newestMod) {
			newest, newestMod = path, fi.ModTime()
		}
		return nil
	})
	if newest == "" {
		return "", 0
	}
	return newest, now.Sub(newestMod)
}

// DefaultReapGrace is how long [ReapStalled] gives a holder to exit on
// SIGTERM before escalating to SIGKILL.
const DefaultReapGrace = 10 * time.Second

// ErrHolderReleased is returned by [ReapStalled] when the fresh flock
// probe ahead of a signal finds the owner already released its marker
// between the sweep and the reap -- nothing is signaled.
var ErrHolderReleased = errors.New("boxslot: holder already released its flock")

// ReapStalled kills the owner of one stalled holder: SIGTERM, a grace
// window (non-positive grace means [DefaultReapGrace]), then SIGKILL
// if the owner still holds its flock. Every signal is guarded against
// pid recycling: the marker must still exist at the swept path, its
// filename must parse to the descriptor's pid, and a fresh flock
// probe taken immediately before each signal -- SIGTERM and SIGKILL
// alike -- must show the owner live. A renamed or vanished marker
// refuses; a flock released before the first signal refuses with
// [ErrHolderReleased]; a flock released after SIGTERM means the owner
// exited, which is success. ReapStalled never removes the marker
// file: the kernel drops the flock when the owner dies, and
// admission's stale-marker GC (or box-slots release) clears the file.
// Reaping is an explicit operator act; the admission wait path only
// reports.
func ReapStalled(h StalledHolder, grace time.Duration) error {
	if grace <= 0 {
		grace = DefaultReapGrace
	}
	name := filepath.Base(h.Path)
	pid, _, ok := parseHolderName(name)
	if !ok || pid != h.PID {
		return fmt.Errorf("boxslot: %s does not parse to pid %d; refusing to signal (pid recycled?)", name, h.PID)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("boxslot: find pid %d: %w", pid, err)
	}
	live, err := probeHolderLive(h.Path)
	if err != nil {
		return fmt.Errorf("boxslot: %s no longer probeable; refusing to signal: %w", name, err)
	}
	if !live {
		return fmt.Errorf("%w: %s; admission GC or `sparkwing box-slots release %s` clears the marker", ErrHolderReleased, name, name)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("boxslot: SIGTERM pid %d: %w", pid, err)
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		live, err := probeHolderLive(h.Path)
		if err != nil || !live {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	live, err = probeHolderLive(h.Path)
	if err != nil || !live {
		return nil
	}
	if err := proc.Kill(); err != nil {
		return fmt.Errorf("boxslot: SIGKILL pid %d: %w", pid, err)
	}
	return nil
}

// probeHolderLive opens path and takes a non-blocking flock probe:
// failure to lock means the owner still holds the file (live), success
// means the kernel released it on owner death (stale). The probe lock
// is dropped immediately. Errors (including a missing file) propagate
// so the caller can refuse to act on a marker it can no longer see.
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
