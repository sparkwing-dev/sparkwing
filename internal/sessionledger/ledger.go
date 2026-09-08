// Package sessionledger records the step sessions a node has started so a
// sweep outside the node can end them once the node is gone. The node
// writes a record when a command starts and removes it when the command is
// reaped; a node that dies without reaping leaves its records behind, which
// is exactly the signal the sweep acts on.
package sessionledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Handle names what the sweep must kill to end the session.
type Handle struct {
	// Kind is the sweep boundary: "session" on unix (the step's own
	// session, keyed by its leader), "job" on Windows (a named Job
	// Object), or "docker" (a container the step started), which is
	// ended the same way on every platform.
	Kind string `json:"kind"`
	// LeaderPID is the session leader's pid; the step command itself.
	LeaderPID int `json:"leader_pid,omitempty"`
	// SessionID is the unix session id, equal to LeaderPID for a leader.
	SessionID int `json:"session_id,omitempty"`
	// LeaderBirth pins the leader's incarnation so a reused pid is never
	// signalled.
	LeaderBirth string `json:"leader_birth,omitempty"`
	// JobName names the Windows Job Object the step runs inside.
	JobName string `json:"job_name,omitempty"`
	// Container names the docker container the step started, ended with
	// `docker rm -f` regardless of platform.
	Container string `json:"container,omitempty"`
}

// Record is one running step command and the node that owns it.
type Record struct {
	Run        string    `json:"run"`
	Node       string    `json:"node"`
	OwnerPID   int       `json:"owner_pid"`
	OwnerBirth string    `json:"owner_birth"`
	Handle     Handle    `json:"handle"`
	Command    string    `json:"command"`
	StartedAt  time.Time `json:"started_at"`

	path string
}

// Path is the file the record lives in, set on records read back by List.
func (r Record) Path() string { return r.path }

// Probe answers the two platform questions a sweep asks.
type Probe interface {
	// OwnerAlive reports whether the node that wrote the record still runs:
	// the pid exists and its birth token matches.
	OwnerAlive(rec Record) (bool, error)
	// Terminate ends the session the handle names. A handle whose members
	// are already gone is not an error.
	Terminate(ctx context.Context, h Handle) error
}

// Ledger is the directory of records for one sparkwing home.
type Ledger struct {
	dir   string
	probe Probe
}

// Open returns the ledger at dir with the platform probe.
func Open(dir string) *Ledger {
	return &Ledger{dir: dir, probe: dispatchProbe{platform: platformProbe{}}}
}

// dispatchProbe owns the cross-platform handle kinds (docker) and delegates
// the rest to the platform probe, so a session or job is ended the way its
// OS requires while a container is ended the same way everywhere.
type dispatchProbe struct{ platform Probe }

func (d dispatchProbe) OwnerAlive(rec Record) (bool, error) { return d.platform.OwnerAlive(rec) }

func (d dispatchProbe) Terminate(ctx context.Context, h Handle) error {
	if h.Kind == "docker" {
		return terminateContainer(ctx, h.Container)
	}
	return d.platform.Terminate(ctx, h)
}

// OpenWithProbe returns the ledger at dir with a caller-supplied probe.
func OpenWithProbe(dir string, probe Probe) *Ledger { return &Ledger{dir: dir, probe: probe} }

// Dir is the ledger's directory.
func (l *Ledger) Dir() string { return l.dir }

func recordName(rec Record) string {
	key := rec.Handle.Container
	if key == "" {
		key = rec.Handle.JobName
	}
	if key == "" {
		key = fmt.Sprintf("%d", rec.Handle.LeaderPID)
	}
	return fmt.Sprintf("%d-%s.json", rec.OwnerPID, sanitize(key))
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, s)
}

// Record writes rec and returns the release that removes it. The write is
// atomic so a sweep never reads a half-written record.
func (l *Ledger) Record(rec Record) (func(), error) {
	if rec.Run == "" || rec.Node == "" {
		return nil, errors.New("sessionledger: record needs a run and a node")
	}
	if rec.OwnerPID <= 1 {
		return nil, fmt.Errorf("sessionledger: owner pid %d is not a process this ledger can track", rec.OwnerPID)
	}
	if rec.StartedAt.IsZero() {
		rec.StartedAt = time.Now()
	}
	dir := filepath.Join(l.dir, sanitize(rec.Run), sanitize(rec.Node))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	final := filepath.Join(dir, recordName(rec))
	tmp, err := os.CreateTemp(dir, ".rec-*")
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(rec)
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return nil, err
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		_ = os.Remove(tmp.Name())
		return nil, err
	}
	return func() { _ = os.Remove(final) }, nil
}

// List reads every record. A file that is not a record is skipped, never
// fatal, so one bad write cannot hide the rest.
func (l *Ledger) List() ([]Record, error) {
	var out []Record
	err := filepath.WalkDir(l.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var rec Record
		if json.Unmarshal(data, &rec) != nil || rec.Run == "" {
			return nil
		}
		rec.path = path
		out = append(out, rec)
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}

// Verdict is what the sweep decided for one record.
type Verdict string

const (
	// VerdictLive means the owner still runs; the record stays.
	VerdictLive Verdict = "live"
	// VerdictReaped means the owner is gone and the session was ended.
	VerdictReaped Verdict = "reaped"
	// VerdictWouldReap is VerdictReaped under a dry run: nothing was killed.
	VerdictWouldReap Verdict = "would-reap"
	// VerdictFailed means the owner is gone but the session could not be
	// ended; the record stays for the next sweep.
	VerdictFailed Verdict = "failed"
)

// Outcome pairs a record with the sweep's verdict.
type Outcome struct {
	Record  Record
	Verdict Verdict
	Err     error
}

// SweepOptions narrows a sweep.
type SweepOptions struct {
	// Run limits the sweep to one run's records.
	Run string
	// DryRun reports what would be reaped without killing or removing.
	DryRun bool
}

// Sweep ends every session whose owner is gone and removes its record.
// Records whose owner still runs are left alone.
func (l *Ledger) Sweep(ctx context.Context, opts SweepOptions) ([]Outcome, error) {
	records, err := l.List()
	if err != nil {
		return nil, err
	}
	var out []Outcome
	for _, rec := range records {
		if opts.Run != "" && rec.Run != opts.Run {
			continue
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
		alive, err := l.probe.OwnerAlive(rec)
		if err != nil {
			out = append(out, Outcome{Record: rec, Verdict: VerdictFailed, Err: err})
			continue
		}
		if alive {
			out = append(out, Outcome{Record: rec, Verdict: VerdictLive})
			continue
		}
		if opts.DryRun {
			out = append(out, Outcome{Record: rec, Verdict: VerdictWouldReap})
			continue
		}
		if err := l.probe.Terminate(ctx, rec.Handle); err != nil {
			out = append(out, Outcome{Record: rec, Verdict: VerdictFailed, Err: err})
			continue
		}
		_ = os.Remove(rec.path)
		out = append(out, Outcome{Record: rec, Verdict: VerdictReaped})
	}
	return out, nil
}
