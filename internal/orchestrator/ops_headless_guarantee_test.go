package orchestrator_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// TestHeadless_ScaffoldedModuleServesOpsAndRuns is the gate behind the product
// principle "sparkwing does not require sparkwing": a plain `go build` of a
// scaffolded .sparkwing module -- one that blank-imports its jobs package and
// calls runner.Main, with no sparkwing CLI anywhere in the loop -- must produce
// a binary that both runs a pipeline through daemon admission and serves the
// operator surface (queue, stats, version) for itself.
//
// It generates the module against the working tree (a replace directive), so
// the guarantee is checked for the code under test, not a released SDK.
func TestHeadless_ScaffoldedModuleServesOpsAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("headless guarantee build is slow; run without -short")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	repoRoot := repoRootDir(t)

	mod := t.TempDir()
	writeMod(t, filepath.Join(mod, "go.mod"), ""+
		"module headlessguarantee\n\n"+
		"go 1.26.0\n\n"+
		"require github.com/sparkwing-dev/sparkwing v0.0.0\n\n"+
		"replace github.com/sparkwing-dev/sparkwing => "+repoRoot+"\n")
	writeMod(t, filepath.Join(mod, "jobs", "jobs.go"), scaffoldJobs)
	writeMod(t, filepath.Join(mod, "main.go"), scaffoldMain)

	// hack: cache-only module resolution keeps the build hermetic -- every
	// dependency is already in the shared module cache from building this test.
	buildEnv := append(os.Environ(),
		"GOFLAGS=-mod=mod", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
	runGo(t, mod, buildEnv, "mod", "tidy")
	bin := filepath.Join(mod, "headlessguarantee")
	runGo(t, mod, buildEnv, "build", "-o", bin, ".")

	home := t.TempDir()
	runEnv := append(os.Environ(), "SPARKWING_HOME="+home, "SPARKWING_LOG_FORMAT=quiet")

	if out := runBin(t, mod, runEnv, bin, "ops", "version"); strings.TrimSpace(out) == "" {
		t.Fatal("ops version produced no output")
	}

	var empty wingwire.QueueState
	if err := json.Unmarshal([]byte(runBin(t, mod, runEnv, bin, "ops", "queue", "-o", "json")), &empty); err != nil {
		t.Fatalf("ops queue -o json is not valid QueueState JSON: %v", err)
	}

	// safety: the binary spawns its own daemon and runs the job through it --
	// no CLI and no test-started daemon, so this exercises headless admission.
	runBin(t, mod, runEnv, bin, "noop")

	var qs wingwire.QueueState
	if err := json.Unmarshal([]byte(runBin(t, mod, runEnv, bin, "ops", "queue", "-o", "json")), &qs); err != nil {
		t.Fatalf("post-run ops queue json: %v", err)
	}
	if len(qs.Holders) != 0 || len(qs.Waiters) != 0 {
		t.Fatalf("headless run left the queue non-empty: %d holders, %d waiters", len(qs.Holders), len(qs.Waiters))
	}
	if err := json.Unmarshal([]byte(runBin(t, mod, runEnv, bin, "ops", "stats", "-o", "json")), new(any)); err != nil {
		t.Fatalf("ops stats json: %v", err)
	}
}

const scaffoldJobs = `package jobs

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Noop struct{ sparkwing.Base }

func (p *Noop) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "noop", func(ctx context.Context) error {
		sparkwing.Info(ctx, "noop ok")
		return nil
	})
	return nil
}

func init() {
	sparkwing.Register("noop", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Noop{} })
}
`

const scaffoldMain = `package main

import (
	_ "headlessguarantee/jobs"

	"github.com/sparkwing-dev/sparkwing/pkg/runner"
)

func main() { runner.Main() }
`

func repoRootDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve this test's source path")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root %q has no go.mod: %v", root, err)
	}
	return root
}

func writeMod(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func runGo(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func runBin(t *testing.T, dir string, env []string, bin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", filepath.Base(bin), strings.Join(args, " "), err, out)
	}
	return string(out)
}
