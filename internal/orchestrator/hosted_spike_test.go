package orchestrator_test

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestEngineHosted(t *testing.T) {
	bin, mod := buildHostedFixture(t)
	t.Run("EngineSchedulesNodesTheBinaryRuns", func(t *testing.T) { engineSchedulesNodesTheBinaryRuns(t, bin, mod) })
	t.Run("FailedDependencySkipsItsDependent", func(t *testing.T) { failedDependencySkipsItsDependent(t, bin, mod) })
	t.Run("RefusesWhatTheSpikeDoesNotHost", func(t *testing.T) { refusesWhatTheSpikeDoesNotHost(t, bin, mod) })
}

func engineSchedulesNodesTheBinaryRuns(t *testing.T, bin, mod string) {
	home, probe := hostedSandbox(t)
	describe := describeBinary(t, mod, bin)
	logs := &recordingLogger{}

	res, err := orchestrator.RunHosted(t.Context(), orchestrator.HostedRun{
		Describe: describe,
		Binary:   bin,
		Pipeline: "hosted",
		WorkDir:  mod,
		Paths:    orchestrator.PathsAt(home),
		Logger:   slog.New(slog.DiscardHandler),
		Delegate: logs,
	})
	if err != nil {
		t.Fatalf("hosted run: %v", err)
	}
	if want := []string{"build", "publish"}; !slices.Equal(res.Order, want) {
		t.Fatalf("start order = %v, want %v: publish needs build", res.Order, want)
	}
	for _, id := range res.Order {
		if got := res.Nodes[id].Outcome; got != sparkwing.Success {
			t.Errorf("node %s outcome = %s (err %v), want success", id, got, res.Nodes[id].Err)
		}
	}
	if !logs.contains("published digest=sha-hosted") {
		t.Errorf("publish did not read build's output through the engine:\n%s", logs.dump())
	}

	buildPID, publishPID := readHostedPID(t, probe, "build"), readHostedPID(t, probe, "publish")
	if buildPID == os.Getpid() || publishPID == os.Getpid() || buildPID == publishPID {
		t.Errorf("pids build=%d publish=%d engine=%d: each node needs its own process", buildPID, publishPID, os.Getpid())
	}

	st, err := store.Open(orchestrator.PathsAt(home).StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	run, err := st.GetRun(t.Context(), res.RunID)
	if err != nil || run.Status != "success" {
		t.Fatalf("run row = %+v (err %v), want success", run, err)
	}
	if len(run.PlanSnapshot) == 0 || run.LastHeartbeatAt == nil {
		t.Errorf("run row has snapshot %d bytes, heartbeat %v; the dispatcher records both", len(run.PlanSnapshot), run.LastHeartbeatAt)
	}
	for _, id := range res.Order {
		n, err := st.GetNode(t.Context(), res.RunID, id)
		if err != nil || n.CPUNanos <= 0 || n.MaxRSSBytes <= 0 {
			t.Errorf("node %s usage = %+v (err %v), want the process's measurement", id, n, err)
		}
	}
}

func failedDependencySkipsItsDependent(t *testing.T, bin, mod string) {
	home, probe := hostedSandbox(t)

	res, err := orchestrator.RunHosted(t.Context(), orchestrator.HostedRun{
		Describe: describeBinary(t, mod, bin),
		Binary:   bin,
		Pipeline: "hostedfail",
		WorkDir:  mod,
		Paths:    orchestrator.PathsAt(home),
		Logger:   slog.New(slog.DiscardHandler),
		Delegate: &recordingLogger{},
	})
	if err != nil {
		t.Fatalf("hosted run: %v", err)
	}
	if got := res.Nodes["build"].Outcome; got != sparkwing.Failed {
		t.Errorf("build outcome = %s, want failed", got)
	}
	if got := res.Nodes["publish"].Outcome; got != sparkwing.Skipped {
		t.Errorf("publish outcome = %s, want skipped", got)
	}
	if _, err := os.Stat(filepath.Join(probe, "publish.pid")); err == nil {
		t.Error("publish ran although the node it needs failed")
	}
}

func refusesWhatTheSpikeDoesNotHost(t *testing.T, bin, mod string) {
	home, _ := hostedSandbox(t)

	_, err := orchestrator.RunHosted(t.Context(), orchestrator.HostedRun{
		Describe: describeBinary(t, mod, bin),
		Binary:   bin,
		Pipeline: "hostedretry",
		WorkDir:  mod,
		Paths:    orchestrator.PathsAt(home),
		Logger:   slog.New(slog.DiscardHandler),
	})
	if err == nil || !strings.Contains(err.Error(), "retries") {
		t.Fatalf("err = %v, want a refusal naming the retry", err)
	}
}

func hostedSandbox(t *testing.T) (home, probe string) {
	t.Helper()
	home, probe = t.TempDir(), t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("HOSTED_PROBE_DIR", probe)
	return home, probe
}

func describeBinary(t *testing.T, dir, bin string) []byte {
	t.Helper()
	cmd := exec.Command(bin, "--describe")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s --describe: %v", bin, err)
	}
	return out
}

func readHostedPID(t *testing.T, dir, name string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name+".pid"))
	if err != nil {
		t.Fatalf("node %s left no pid: %v", name, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

type recordingLogger struct {
	mu   sync.Mutex
	msgs []string
}

func (l *recordingLogger) Log(_, msg string) { l.Emit(sparkwing.LogRecord{Msg: msg}) }

func (l *recordingLogger) Emit(rec sparkwing.LogRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, rec.JobID+": "+rec.Msg)
}

func (l *recordingLogger) contains(s string) bool {
	return strings.Contains(l.dump(), s)
}

func (l *recordingLogger) dump() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.msgs, "\n")
}

func buildHostedFixture(t *testing.T) (bin, mod string) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a pipeline binary; run without -short")
	}
	if runtime.GOOS == "windows" {
		t.Skip("node processes are POSIX sessions")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	root := t.TempDir()
	mod = filepath.Join(root, "pipeline")
	writeMod(t, filepath.Join(mod, "go.mod"), ""+
		"module hostedspike\n\ngo 1.26.0\n\n"+
		"require github.com/sparkwing-dev/sparkwing v0.0.0\n\n"+
		"replace github.com/sparkwing-dev/sparkwing => "+repoRootDir(t)+"\n")
	writeMod(t, filepath.Join(mod, "main.go"), hostedFixtureMain)
	env := append(os.Environ(), "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local", "GOWORK=off")
	runGo(t, mod, env, "mod", "tidy")
	bin = filepath.Join(root, "hostedspike")
	runGo(t, mod, env, "build", "-o", bin, ".")
	return bin, mod
}

const hostedFixtureMain = `package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/pkg/runner"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func stamp(name string) {
	_ = os.WriteFile(filepath.Join(os.Getenv("HOSTED_PROBE_DIR"), name+".pid"), []byte(strconv.Itoa(os.Getpid())), 0o644)
}

type BuildOut struct {
	Digest string ` + "`json:\"digest\"`" + `
}

type Build struct {
	sparkwing.Base
	sparkwing.Produces[BuildOut]
	fail bool
}

func (j *Build) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(ctx context.Context) (BuildOut, error) {
		stamp("build")
		if j.fail {
			return BuildOut{}, errors.New("build broke")
		}
		return BuildOut{Digest: "sha-hosted"}, nil
	}), nil
}

type Publish struct {
	sparkwing.Base
	Build sparkwing.Ref[BuildOut]
}

func (j *Publish) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(ctx context.Context) error {
		stamp("publish")
		sparkwing.Info(ctx, "published digest=%s", j.Build.Get(ctx).Digest)
		return nil
	}), nil
}

type Hosted struct {
	sparkwing.Base
	fail bool
}

func (p *Hosted) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	build := sparkwing.Job(plan, "build", &Build{fail: p.fail})
	sparkwing.Job(plan, "publish", &Publish{Build: sparkwing.RefTo[BuildOut](build)}).Needs(build)
	return nil
}

type HostedRetry struct{ sparkwing.Base }

func (p *HostedRetry) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "flaky", func(context.Context) error { return nil }).Retry(2)
	return nil
}

func main() {
	sparkwing.Register("hosted", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Hosted{} })
	sparkwing.Register("hostedfail", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Hosted{fail: true} })
	sparkwing.Register("hostedretry", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &HostedRetry{} })
	runner.Main()
}
`
