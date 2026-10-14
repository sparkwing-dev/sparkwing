//go:build unix

package orchestrator_test

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// safety: each parent waits on a child while holding most of a 2-core budget, a box group or a node's cores;
// the child must attach to that through the real daemon API, trigger queue and CLI, or it waits on its parent
// until the run times out. Nodes pin a core, as an unpinned node's default cost takes the whole budget.
const leaseFixtureJobs = `package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/pkg/runner"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func main() { runner.Main() }

func mark(name string) error {
	if os.Getenv("SPARKWING_SECRETS_KEY") != "" {
		return errors.New("the engine's secrets key reached a node process")
	}
	return os.WriteFile(filepath.Join(os.Getenv("C3_MARKER"), name), nil, 0o600)
}

func shared() *sparkwing.ConcurrencyGroup {
	return sparkwing.NewConcurrencyGroup("c3-shared", sparkwing.ConcurrencyLimit{Capacity: 1, Scope: sparkwing.ScopeBox, OnLimit: sparkwing.Queue})
}

type Leaf struct{ sparkwing.Base }

type leafWork struct {
	sparkwing.Base
	sparkwing.Produces[string]
}

func (leafWork) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "work", func(context.Context) (string, error) { return "done", mark("leaf.real") }).
		DryRun(func(context.Context) error { return mark("leaf.dry") }), nil
}

func (Leaf) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "work", leafWork{}).Resources(sparkwing.Cores(1))
	return nil
}

type GroupedLeaf struct{ Leaf }

func (l GroupedLeaf) Plan(ctx context.Context, plan *sparkwing.Plan, in sparkwing.NoInputs, rc sparkwing.RunContext) error {
	plan.Concurrency(shared())
	return l.Leaf.Plan(ctx, plan, in, rc)
}

func await(leaf string) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := sparkwing.RunAndAwait[string, sparkwing.NoInputs](ctx, leaf, "work")
		return err
	}
}

func nested(pipeline string) func(context.Context) error {
	return func(ctx context.Context) error {
		cmd := exec.CommandContext(ctx, os.Getenv("C3_CLI"), "run", pipeline, "--sw-no-update")
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		return cmd.Run()
	}
}

type Pinned struct{ sparkwing.Base }

func (Pinned) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	plan.Resources(sparkwing.Cores(1.5))
	sparkwing.Job(plan, "call", await("leaf"))
	return nil
}

type NodeOwned struct{ sparkwing.Base }

func (NodeOwned) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "call", await("leaf")).Resources(sparkwing.Cores(1.5))
	return nil
}

type Grouped struct{ sparkwing.Base }

func (Grouped) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	plan.Concurrency(shared())
	sparkwing.Job(plan, "call", await("grouped-leaf")).Resources(sparkwing.Cores(1))
	return nil
}

type Deep struct{ sparkwing.Base }

func (Deep) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	plan.Resources(sparkwing.Cores(1.5))
	sparkwing.Job(plan, "call", nested("middle"))
	return nil
}

type Middle struct{ sparkwing.Base }

func (Middle) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	group := sparkwing.NewConcurrencyGroup("c3-middle", sparkwing.ConcurrencyLimit{Capacity: 1, Scope: sparkwing.ScopeBox, OnLimit: sparkwing.Queue})
	sparkwing.Job(plan, "call", await("leaf")).Concurrency(group)
	return nil
}

// safety: the chain's middle run holds no group and its node no lease of its own, so the grandchild finds the
// grandparent's group only by walking the chain.
type Chain struct{ sparkwing.Base }

func (Chain) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	plan.Concurrency(shared())
	sparkwing.Job(plan, "call", await("plain-middle")).Resources(sparkwing.Cores(1))
	return nil
}

type PlainMiddle struct{ sparkwing.Base }

type relayWork struct {
	sparkwing.Base
	sparkwing.Produces[string]
}

func (relayWork) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "work", func(ctx context.Context) (string, error) {
		return sparkwing.RunAndAwait[string, sparkwing.NoInputs](ctx, "grouped-leaf", "work")
	}), nil
}

func (PlainMiddle) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "work", relayWork{})
	return nil
}

type DryParent struct{ sparkwing.Base }

type dryCall struct{ sparkwing.Base }

func (dryCall) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "call", await("leaf")).SafeWithoutDryRun(), nil
}

func (DryParent) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "call", dryCall{})
	return nil
}

func init() {
	sparkwing.Register("leaf", func() sparkwing.Pipeline[sparkwing.NoInputs] { return Leaf{} })
	sparkwing.Register("grouped-leaf", func() sparkwing.Pipeline[sparkwing.NoInputs] { return GroupedLeaf{} })
	sparkwing.Register("pinned", func() sparkwing.Pipeline[sparkwing.NoInputs] { return Pinned{} })
	sparkwing.Register("node-owned", func() sparkwing.Pipeline[sparkwing.NoInputs] { return NodeOwned{} })
	sparkwing.Register("grouped", func() sparkwing.Pipeline[sparkwing.NoInputs] { return Grouped{} })
	sparkwing.Register("deep", func() sparkwing.Pipeline[sparkwing.NoInputs] { return Deep{} })
	sparkwing.Register("middle", func() sparkwing.Pipeline[sparkwing.NoInputs] { return Middle{} })
	sparkwing.Register("dry-parent", func() sparkwing.Pipeline[sparkwing.NoInputs] { return DryParent{} })
	sparkwing.Register("chain", func() sparkwing.Pipeline[sparkwing.NoInputs] { return Chain{} })
	sparkwing.Register("plain-middle", func() sparkwing.Pipeline[sparkwing.NoInputs] { return PlainMiddle{} })
}
`

var (
	leaseFixtureOnce sync.Once
	leaseFixtureRepo string
)

func leaseFixture(t *testing.T) (repo, cli string) {
	t.Helper()
	if testing.Short() {
		t.Skip("slow: builds the CLI and a pipeline module and runs nested runs against a real daemon")
	}
	cli = wingdHostBin(t)
	leaseFixtureOnce.Do(func() {
		// safety: under the shared fixture root, which TestMain removes.
		root := filepath.Join(sharedProcessPerNodeFixture(t).onDisk, "c3-lease")
		leaseFixtureRepo = root
		dir := filepath.Join(root, ".sparkwing")
		writeMod(t, filepath.Join(dir, "go.mod"), "module leasefixture\n\ngo 1.26.0\n\n"+
			"require github.com/sparkwing-dev/sparkwing v0.0.0\n\nreplace github.com/sparkwing-dev/sparkwing => "+repoRootDir(t)+"\n")
		writeMod(t, filepath.Join(dir, "main.go"), leaseFixtureJobs)
		var yaml strings.Builder
		yaml.WriteString("pipelines:\n")
		for _, p := range []string{"leaf", "grouped-leaf", "pinned", "node-owned", "grouped", "deep", "middle", "dry-parent", "chain", "plain-middle"} {
			yaml.WriteString("  - name: " + p + "\n    entrypoint: E" + strings.ReplaceAll(p, "-", "") + "\n")
		}
		writeMod(t, filepath.Join(dir, "sparkwing.yaml"), yaml.String())
		runGo(t, dir, append(os.Environ(), "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local", "GOWORK=off"), "mod", "tidy")
		for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture"}} {
			if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
	})
	return leaseFixtureRepo, cli
}

func goEnv(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.Command("go", "env", name).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func runLeaseCase(t *testing.T, pipeline string, args ...string) map[string]bool {
	t.Helper()
	repo, cli := leaseFixture(t)
	home := t.TempDir()
	stopHomeDaemon(t, home)
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("admission:\n  budget: 2,ignore-external\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, append([]string{"run", pipeline, "--sw-no-update"}, args...)...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "HOME="+home, "SPARKWING_HOME="+home, "SPARKWING_CONFIG="+filepath.Join(home, "config.yaml"),
		"SPARKWING_WINGD_BIN="+cli, "C3_MARKER="+marker, "C3_CLI="+cli, "SPARKWING_DRY_RUN=", "SPARKWING_LOG_FORMAT=json",
		// safety: every node marks only when the engine's credential stays out of it, including a claimed child's.
		"SPARKWING_SECRETS_KEY="+base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"GOCACHE="+goEnv(t, "GOCACHE"), "GOMODCACHE="+goEnv(t, "GOMODCACHE"), "GOWORK=off", "GOTOOLCHAIN=local")
	// safety: a child that outlives a killed run holds its output open, so the wait is bounded past the kill.
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sparkwing run %s: %v (ctx %v)\n%s", pipeline, err, ctx.Err(), out)
	}
	if strings.Contains(string(out), "running without local coordination") {
		t.Fatalf("the run never reached the daemon, so it proves nothing:\n%s", out)
	}
	seen := map[string]bool{}
	entries, err := os.ReadDir(marker)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		seen[e.Name()] = true
	}
	return seen
}

func TestHostedChildrenAttachToWhatTheirParentHolds(t *testing.T) {
	for _, pipeline := range []string{
		// safety: deep's middle run holds a node semaphore lease without cores; its run's attached lease has them.
		"pinned", "node-owned", "grouped", "deep", "chain",
	} {
		t.Run(pipeline, func(t *testing.T) {
			if got := runLeaseCase(t, pipeline); !got["leaf.real"] {
				t.Fatalf("the awaited child never ran its body; markers %v", got)
			}
		})
	}
}

func TestHostedDryRunAwaitsADryChild(t *testing.T) {
	got := runLeaseCase(t, "dry-parent", "--sw-dry-run")
	if got["leaf.real"] || !got["leaf.dry"] {
		t.Fatalf("markers %v: a dry run's awaited child must run its dry-run body, not its real one", got)
	}
}
