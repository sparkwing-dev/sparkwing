//go:build unix

package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const guardPipeline = "hosted-guard-fixture"

type guardInputs struct {
	Stage string `flag:"stage" default:"prod" desc:"A value only guards read"`
}

type guardFixture struct{ sparkwing.Base }

func (guardFixture) Plan(_ context.Context, plan *sparkwing.Plan, _ guardInputs, _ sparkwing.RunContext) error {
	marker := os.Getenv("HOSTED_GUARD_MARKER")
	sparkwing.Job(plan, "body", func(context.Context) error { return os.WriteFile(marker, []byte("ran"), 0o600) })
	return nil
}

func init() {
	if os.Getenv(nodeProtocolEntryEnv) == "1" {
		sparkwing.Register(guardPipeline, func() sparkwing.Pipeline[guardInputs] { return guardFixture{} })
	}
}

func runGuardFixture(t *testing.T, guard string, git *sparkwing.Git) (marker string, err error) {
	t.Helper()
	dir := t.TempDir()
	yaml := "pipelines:\n  - name: " + guardPipeline + "\n    entrypoint: GuardFixture\n    guards:\n      reject: [\"" + guard + "\"]\n"
	if err := os.MkdirAll(filepath.Join(dir, ".sparkwing"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".sparkwing", "sparkwing.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := PathsAt(filepath.Join(dir, "home"))
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := teststore.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	marker = filepath.Join(dir, "marker")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	_, err = RunHosted(ctx, paths, Options{RunID: "guarded", Pipeline: guardPipeline, State: st, Delegate: &storedLog{}, Git: git}, HostedEntry{
		Command: pipelineentry.Binary(os.Args[0]), Dir: dir,
		Env: append(os.Environ(), nodeProtocolEntryEnv+"=1", "HOSTED_GUARD_MARKER="+marker, "GORACE=atexit_sleep_ms=0"),
	})
	return marker, err
}

// safety: a local run's guard judges the arguments it plans with, describe's defaults included, as a triggered
// run's does.
func TestHostedRun_AGuardJudgesTheSDKsDefaults(t *testing.T) {
	marker, err := runGuardFixture(t, "arg:stage=prod", nil)
	if err == nil || !strings.Contains(err.Error(), `rejected by guard "arg:stage=prod"`) {
		t.Fatalf("run = %v, want the guard's refusal of the default stage", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the body ran past the guard: %v", err)
	}
}

// safety: a local run whose checkout names no default branch is refused by a default-branch guard, not let past it.
func TestHostedRun_AnUnresolvedDefaultBranchRefusesItsGuard(t *testing.T) {
	marker, err := runGuardFixture(t, "git:branch=default", &sparkwing.Git{Branch: "main"})
	if err == nil || !strings.Contains(err.Error(), "could not resolve the repository's default branch") {
		t.Fatalf("run = %v, want the unresolved default branch refused", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the body ran past the guard: %v", err)
	}
}
