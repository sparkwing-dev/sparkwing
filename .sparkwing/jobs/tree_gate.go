package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: a note travels with the repository rather than with a branch or a
// cache scope, so a pass recorded on an agent's branch is visible to main's
// run on the same machine, and anywhere else this ref is fetched.
const gateNotesRef = "refs/notes/sparkwing-gate"

// safety: bump when the record's meaning changes, so older notes stop matching.
const gateRecordFormat = "gate-record-v1"

type gateMode string

const (
	gateAffected gateMode = "affected"
	gateFull     gateMode = "full"
)

// safety: a full pass runs every test an affected run would, so it proves both.
func (m gateMode) covers(want gateMode) bool {
	return m == want || m == gateFull
}

// safety: a pass proves only this tree, under this gate definition, judged by this engine.
type gateKey struct {
	Tree        string `json:"tree"`
	Fingerprint string `json:"fingerprint"`
	Engine      string `json:"engine"`
}

type gateRecord struct {
	gateKey
	Mode       gateMode  `json:"mode"`
	Commit     string    `json:"commit"`
	Branch     string    `json:"branch,omitempty"`
	Runner     string    `json:"runner,omitempty"`
	RunnerType string    `json:"runner_type,omitempty"`
	Host       string    `json:"host,omitempty"`
	RunID      string    `json:"run_id,omitempty"`
	RecordedAt time.Time `json:"recorded_at"`
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

func readGateRecords(ctx context.Context, tree string) ([]gateRecord, error) {
	res, err := sparkwing.Bash(ctx, "git notes --ref="+gateNotesRef+" show "+tree+" 2>/dev/null || true").Capture()
	if err != nil {
		return nil, fmt.Errorf("read gate notes for tree %s: %w", tree, err)
	}
	var records []gateRecord
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r gateRecord
		// safety: a line that does not parse proves nothing, so it is a miss.
		if json.Unmarshal([]byte(line), &r) == nil {
			records = append(records, r)
		}
	}
	return records, nil
}

func matchingPass(records []gateRecord, key gateKey, mode gateMode) *gateRecord {
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].gateKey == key && records[i].Mode.covers(mode) {
			return &records[i]
		}
	}
	return nil
}

func provenPass(ctx context.Context, mode gateMode) (*gateRecord, string) {
	key, err := currentGateKey(ctx)
	if err != nil {
		return nil, err.Error()
	}
	records, err := readGateRecords(ctx, key.Tree)
	if err != nil {
		return nil, err.Error()
	}
	if pass := matchingPass(records, key, mode); pass != nil {
		return pass, ""
	}
	if len(records) == 0 {
		return nil, fmt.Sprintf("tree %s has no recorded %s gate pass", short(key.Tree), mode)
	}
	return nil, fmt.Sprintf("tree %s has %d recorded pass(es), none for gate %s at engine %s in %s mode",
		short(key.Tree), len(records), key.Fingerprint, key.Engine, mode)
}

// safety: the skip names the run that proved the tree, so a pass from an unexpected machine is visible.
func skipWhenTreePassed(mode gateMode) sparkwing.SkipPredicate {
	return func(ctx context.Context) bool {
		pass, reason := provenPass(ctx, mode)
		if pass == nil {
			sparkwing.Info(ctx, "tree-gate: running: %s", reason)
			return false
		}
		sparkwing.Info(ctx, "tree-gate: skipping: tree %s passed gate %s at engine %s in %s mode on runner %s (host %s, run %s, commit %s, %s)",
			short(pass.Tree), pass.Fingerprint, pass.Engine, pass.Mode, pass.Runner, pass.Host, pass.RunID, short(pass.Commit),
			pass.RecordedAt.Format(time.RFC3339))
		return true
	}
}

// safety: a checkout that moved during the run records nothing, because the
// tree it ended on is not the tree the jobs judged.
func recordTreePass(mode gateMode) func(context.Context) error {
	return func(ctx context.Context) error {
		key, err := currentGateKey(ctx)
		var noKey errNoGateKey
		if errors.As(err, &noKey) {
			sparkwing.Info(ctx, "tree-gate: recording nothing: %s", noKey.reason)
			return nil
		}
		if err != nil {
			return err
		}
		commit, err := gitRevParse(ctx, "HEAD")
		if err != nil {
			return err
		}
		if started := os.Getenv("SPARKWING_COMMIT"); started != "" && started != commit {
			sparkwing.Info(ctx, "tree-gate: recording nothing: HEAD moved from %s to %s during the run", short(started), short(commit))
			return nil
		}
		host, err := os.Hostname()
		if err != nil {
			host = "unknown: " + err.Error()
		}
		record := gateRecord{
			gateKey:    key,
			Mode:       mode,
			Commit:     commit,
			Branch:     os.Getenv("SPARKWING_BRANCH"),
			Runner:     os.Getenv("SPARKWING_RUNNER_NAME"),
			RunnerType: os.Getenv("SPARKWING_RUNNER_TYPE"),
			Host:       host,
			RunID:      os.Getenv("SPARKWING_RUN_ID"),
			RecordedAt: time.Now().UTC(),
		}
		line, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if _, err := sparkwing.Bash(ctx, "git notes --ref="+gateNotesRef+" append -m "+shellQuote(string(line))+" "+key.Tree).Run(); err != nil {
			return fmt.Errorf("record the gate pass for tree %s: %w", short(key.Tree), err)
		}
		sparkwing.Info(ctx, "tree-gate: recorded tree %s passing gate %s at engine %s in %s mode; publish with `git push origin %s`",
			short(key.Tree), key.Fingerprint, key.Engine, mode, gateNotesRef)
		return nil
	}
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
