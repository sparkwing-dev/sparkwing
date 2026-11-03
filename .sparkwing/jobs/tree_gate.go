package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: bump when the record's meaning changes, so older passes stop matching.
const gateRecordFormat = "gate-record-v2"

// safety: the job whose output records a pass; the proven reference reads it by this id.
const recordTreeJob = "record-tree"

type gateMode string

const (
	gateAffected gateMode = "affected"
	gateFull     gateMode = "full"
)

// safety: a pass proves only this tree, under this gate definition, judged by this engine.
type gateKey struct {
	Tree        string `json:"tree"`
	Fingerprint string `json:"fingerprint"`
	Engine      string `json:"engine"`
}

// safety: Runner and Host are what the producing run reported. The controller
// decides which passes count: a clean run proven for this repository.
type gatePass struct {
	gateKey
	Recorded bool     `json:"recorded"`
	Reason   string   `json:"reason,omitempty"`
	Mode     gateMode `json:"mode,omitempty"`
	Commit   string   `json:"commit,omitempty"`
	Runner   string   `json:"runner,omitempty"`
	Host     string   `json:"host,omitempty"`
}

// safety: the controller answers only with a run at this run's own commit,
// proven for its repository, that selected every job without dry run.
func provenGatePass(pipeline string) sparkwing.Ref[gatePass] {
	return sparkwing.RefToLastRun[gatePass](pipeline, recordTreeJob, sparkwing.FromProvenRunAtCommit())
}

// safety: a checkout without a key can neither skip a gate nor record a pass.
type errNoGateKey struct{ reason string }

func (e errNoGateKey) Error() string { return e.reason }

// safety: a dirty checkout has no key, because the gate judges the working
// tree and HEAD's tree would not name what it judged.
func currentGateKey(ctx context.Context) (gateKey, error) {
	engine := os.Getenv("SPARKWING_ENGINE_VERSION")
	if engine == "" {
		return gateKey{}, errNoGateKey{"the engine does not report SPARKWING_ENGINE_VERSION"}
	}
	status, err := sparkwing.Bash(ctx, "git status --porcelain --untracked-files=normal").String()
	if err != nil {
		return gateKey{}, fmt.Errorf("read the worktree status: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return gateKey{}, errNoGateKey{"the worktree has uncommitted changes, so HEAD's tree is not what the gate judges"}
	}
	tree, err := gitRevParse(ctx, "HEAD^{tree}")
	if err != nil {
		return gateKey{}, err
	}
	definition, err := gitRevParse(ctx, "HEAD:.sparkwing")
	if err != nil {
		return gateKey{}, err
	}
	return gateKey{Tree: tree, Fingerprint: gateFingerprint(definition), Engine: engine}, nil
}

// safety: the pipeline module's tree holds every job and step the gate
// declares. The tree key already covers it; keeping it apart lets a lookup
// say that the gate itself changed.
func gateFingerprint(definitionTree string) string {
	sum := sha256.Sum256([]byte(gateRecordFormat + "\x00" + definitionTree))
	return hex.EncodeToString(sum[:16])
}

func gitRevParse(ctx context.Context, rev string) (string, error) {
	out, err := sparkwing.Bash(ctx, "git rev-parse --verify --quiet "+shellQuote(rev)).String()
	out = strings.TrimSpace(out)
	if err != nil || out == "" {
		return "", fmt.Errorf("resolve %s: %w", rev, errors.Join(err, errors.New("no object")))
	}
	return out, nil
}

// safety: a lookup the controller refuses or cannot answer runs the gate.
func readGatePass(ctx context.Context, ref sparkwing.Ref[gatePass]) (pass gatePass, found bool, why string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			pass, found, why = gatePass{}, false, fmt.Sprintf("the proven gate lookup failed: %v", recovered)
		}
	}()
	pass, found = ref.TryGet(ctx)
	if !found {
		return pass, false, "no clean proven gate run exists at this commit"
	}
	return pass, true, ""
}

// safety: only a full pass proves a tree. An affected pass judged the change
// against a baseline the reader cannot check, so it helps its own developer only.
func passProves(pass gatePass, key gateKey) (bool, string) {
	switch {
	case !pass.Recorded:
		return false, "the gate run at this commit recorded no pass: " + pass.Reason
	case pass.Tree != key.Tree:
		return false, fmt.Sprintf("that pass judged tree %s, not %s", short(pass.Tree), short(key.Tree))
	case pass.Fingerprint != key.Fingerprint:
		return false, fmt.Sprintf("that pass ran gate %s, not %s", pass.Fingerprint, key.Fingerprint)
	case pass.Engine != key.Engine:
		return false, fmt.Sprintf("that pass ran at engine %s, not %s", pass.Engine, key.Engine)
	case pass.Mode != gateFull:
		return false, fmt.Sprintf("that pass ran in %s mode; only a full pass proves a tree", pass.Mode)
	}
	return true, ""
}

// safety: the skip names the runner and host that proved the tree, so a pass from an unexpected machine is visible.
func skipWhenTreePassed(ref sparkwing.Ref[gatePass]) sparkwing.SkipPredicate {
	return func(ctx context.Context) bool {
		key, err := currentGateKey(ctx)
		if err != nil {
			sparkwing.Info(ctx, "tree-gate: running: %v", err)
			return false
		}
		pass, found, why := readGatePass(ctx, ref)
		if found {
			var proves bool
			proves, why = passProves(pass, key)
			if proves {
				sparkwing.Info(ctx, "tree-gate: skipping: tree %s passed gate %s at engine %s in %s mode on runner %s (host %s, commit %s)",
					short(pass.Tree), pass.Fingerprint, pass.Engine, pass.Mode, pass.Runner, pass.Host, short(pass.Commit))
				return true
			}
		}
		sparkwing.Info(ctx, "tree-gate: running: %s", why)
		return false
	}
}

type recordTree struct {
	sparkwing.Base
	sparkwing.Produces[gatePass]
	Proven sparkwing.Ref[gatePass]
	Gate   sparkwing.Ref[bool]
	Race   sparkwing.Ref[bool]
	mode   gateMode
}

func (r *recordTree) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", r.record), nil
}

func (r *recordTree) record(ctx context.Context) (gatePass, error) {
	if why := skippedGateJob(ctx, r.Gate, r.Race); why != "" {
		sparkwing.Info(ctx, "tree-gate: recording no pass: %s", why)
		return gatePass{Reason: why}, nil
	}
	return recordTreePass(ctx, r.mode)
}

// safety: a skipped upstream satisfies Needs, and each job's proven lookup runs
// on its own, so record-tree can run after jobs that tested nothing. Such a run
// must not record a pass naming its own runner.
func skippedGateJob(ctx context.Context, jobs ...sparkwing.Ref[bool]) string {
	for _, job := range jobs {
		if !gateJobRan(ctx, job) {
			return job.Job() + " did not run in this run"
		}
	}
	return ""
}

// safety: an output that cannot be read counts as a job that did not run.
func gateJobRan(ctx context.Context, job sparkwing.Ref[bool]) (ran bool) {
	defer func() {
		if recover() != nil {
			ran = false
		}
	}()
	value, found := job.TryGet(ctx)
	return found && value
}

// safety: a checkout that moved during the run records no pass, because the
// tree it ended on is not the tree the jobs judged.
func recordTreePass(ctx context.Context, mode gateMode) (gatePass, error) {
	if sparkwing.IsDryRun(ctx) {
		sparkwing.Info(ctx, "tree-gate: recording no pass: a dry run judges nothing")
		return gatePass{Reason: "dry run"}, nil
	}
	key, err := currentGateKey(ctx)
	var noKey errNoGateKey
	if errors.As(err, &noKey) {
		sparkwing.Info(ctx, "tree-gate: recording no pass: %s", noKey.reason)
		return gatePass{Reason: noKey.reason}, nil
	}
	if err != nil {
		return gatePass{}, err
	}
	commit, err := gitRevParse(ctx, "HEAD")
	if err != nil {
		return gatePass{}, err
	}
	if started := os.Getenv("SPARKWING_COMMIT"); started != "" && started != commit {
		reason := fmt.Sprintf("HEAD moved from %s to %s during the run", short(started), short(commit))
		sparkwing.Info(ctx, "tree-gate: recording no pass: %s", reason)
		return gatePass{Reason: reason}, nil
	}
	host, err := os.Hostname()
	if err != nil {
		host = "unknown: " + err.Error()
	}
	pass := gatePass{gateKey: key, Recorded: true, Mode: mode, Commit: commit, Runner: os.Getenv("SPARKWING_RUNNER_NAME"), Host: host}
	sparkwing.Info(ctx, "tree-gate: recorded tree %s passing gate %s at engine %s in %s mode; it lets another run at commit %s skip "+
		"only when this run was proven for its repository, selected every job, and ran in full mode",
		short(key.Tree), key.Fingerprint, key.Engine, mode, short(commit))
	return pass, nil
}
