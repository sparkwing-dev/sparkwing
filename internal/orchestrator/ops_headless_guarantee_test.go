package orchestrator_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// TestHeadless_ScaffoldedModuleServesOpsAndRuns is the gate behind the product
// principle "sparkwing does not require sparkwing": a plain `go build` of a
// scaffolded .sparkwing module -- one that blank-imports its jobs package and
// calls runner.Main, with no sparkwing CLI anywhere in the loop -- must produce
// a binary that runs a pipeline and serves the operator surface (queue, stats,
// version) for itself.
//
// The binary no longer hosts the admission daemon, so with no sparkwing
// installed there is nothing to admit against. The scaffolded pipeline declares
// no resources and no concurrency groups, so this is the implicit-reservations
// case: the run says once that it is uncoordinated and proceeds. That is what
// keeps a pipeline binary shipped alone to a deploy box a working product.
//
// PATH is emptied for the run phase, deliberately: the host-binary lookup falls
// back to a `sparkwing` on PATH, and an ambient one on the developer's machine
// would make this gate pass for the wrong reason.
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

	buildEnv := append(os.Environ(), "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local")
	runGo(t, mod, buildEnv, "mod", "tidy")
	bin := filepath.Join(mod, "headlessguarantee")
	runGo(t, mod, buildEnv, "build", "-o", bin, ".")

	home := t.TempDir()
	stopHomeDaemon(t, home)
	runEnv := append(os.Environ(),
		"SPARKWING_HOME="+home,
		"SPARKWING_LOG_FORMAT=quiet",
		"PATH="+t.TempDir(),
		wingdclient.HostBinEnv+"=",
	)

	if out := runBin(t, mod, runEnv, bin, "ops", "version"); strings.TrimSpace(out) == "" {
		t.Fatal("ops version produced no output")
	}

	var empty wingwire.QueueState
	if err := json.Unmarshal([]byte(runBin(t, mod, runEnv, bin, "ops", "queue", "-o", "json")), &empty); err != nil {
		t.Fatalf("ops queue -o json is not valid QueueState JSON: %v", err)
	}

	// safety: no daemon and no host binary, so the run cannot be admitted and
	// must proceed anyway -- announcing the fact exactly once, since a silently
	// uncoordinated run on a box that usually coordinates is the failure mode
	// this warning exists to prevent.
	runOut := runBin(t, mod, runEnv, bin, "noop")
	const warning = "running without local coordination"
	if got := strings.Count(runOut, warning); got != 1 {
		t.Fatalf("headless run announced %q %d times, want exactly 1:\n%s", warning, got, runOut)
	}
	if !strings.Contains(runOut, "no sparkwing is installed to host one") {
		t.Fatalf("uncoordinated-run warning does not name the missing host:\n%s", runOut)
	}

	// The other side of "it never hosts": nothing in that run started a
	// daemon. The failure this guards against is not a pipeline binary
	// serving the wrong daemon verb -- it is a pipeline binary being asked
	// to serve one at all.
	if _, err := os.Stat(filepath.Join(home, "wingd", "d.log")); err == nil {
		t.Fatal("a pipeline binary with no host available started a daemon anyway")
	}

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

	// A pipeline binary serves no daemon verb, and the argv the client's
	// spawn builds is exactly what an accidental self-exec would run. This
	// asserts it is refused as an unknown pipeline rather than half-served
	// -- the state that broke local runs when the spawn verb and the
	// dispatcher drifted apart.
	for _, verb := range []string{"run", wingdclient.DaemonSpawnVerb} {
		cmd := exec.Command(bin, "wingd", verb, "--home", home)
		cmd.Dir = mod
		cmd.Env = runEnv
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("the pipeline binary served `wingd %s`; daemon hosting belongs to installed binaries:\n%s", verb, out)
		}
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

// daemonStopTimeout bounds the teardown drain, generously enough to cover
// a daemon still writing its final state snapshot.
const daemonStopTimeout = 15 * time.Second

// stopHomeDaemon registers the teardown that stops the admission daemon
// the binary under test spawns for home, and fails the test when one is
// still answering afterwards. Register it before anything runs against
// home: the daemon detaches from the test process and outlives it, so a
// teardown that only runs on the success path leaks a daemon exactly when
// someone is already debugging -- and a stray daemon reads as the
// machine's resident one to whoever inspects it next.
func stopHomeDaemon(t *testing.T, home string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), daemonStopTimeout)
		defer cancel()
		err := wingdclient.Stop(ctx, wingdclient.Options{Home: home})
		if err == nil || errors.Is(err, wingdclient.ErrNoDaemon) {
			return
		}
		t.Errorf("admission daemon for %s outlived the test: %v", home, err)
	})
}

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
