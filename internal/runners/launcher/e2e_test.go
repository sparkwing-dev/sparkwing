package launcher_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const e2eJobs = `package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Out struct {
	Digest string ` + "`json:\"digest\"`" + `
}

type Build struct {
	sparkwing.Base
	sparkwing.Produces[Out]
}

func (j *Build) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(ctx context.Context) (Out, error) { return Out{Digest: "sha-e2e"}, nil }), nil
}

type Check struct {
	sparkwing.Base
	Build sparkwing.Ref[Out]
}

func (j *Check) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(ctx context.Context) error {
		if got := j.Build.Get(ctx); got.Digest != "sha-e2e" {
			return fmt.Errorf("upstream digest %q", got.Digest)
		}
		if s := sparkwing.PipelineSecrets[E2ESecrets](ctx); s == nil || s.Token != "tok-e2e-value" {
			return fmt.Errorf("pipeline secrets %+v", s)
		}
		sparkwing.Info(ctx, "token=%s", sparkwing.MustSecret(ctx, "E2E_TOKEN"))
		sub, err := sparkwing.RunAndAwait[Out, sparkwing.NoInputs](ctx, "e2e-sub", "leaf")
		if err != nil {
			return err
		}
		if sub.Digest != "sha-sub" {
			return fmt.Errorf("child digest %q", sub.Digest)
		}
		return nil
	}), nil
}

type Leaf struct {
	sparkwing.Base
	sparkwing.Produces[Out]
}

func (j *Leaf) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(ctx context.Context) (Out, error) { return Out{Digest: "sha-sub"}, nil }), nil
}

type E2E struct{ sparkwing.Base }

type E2ESecrets struct {
	Token string ` + "`sw:\"E2E_TOKEN,required\"`" + `
}

func (E2E) Secrets() any { return &E2ESecrets{} }

func (p *E2E) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	fmt.Println("plan-time stdout must not reach the plan document")
	plan.Checkout(sparkwing.Checkout{Depth: 5, Tags: true})
	b := sparkwing.Job(plan, "build", &Build{})
	sparkwing.Job(plan, "check", &Check{Build: sparkwing.RefTo[Out](b)}).Needs(b)
	return nil
}

type Sub struct{ sparkwing.Base }

func (p *Sub) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "leaf", &Leaf{})
	return nil
}

type Slow struct{ sparkwing.Base }

func (p *Slow) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "sleep", func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Minute):
			return nil
		}
	})
	return nil
}

func init() {
	sparkwing.Register("e2e-slow", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Slow{} })
	sparkwing.Register("e2e", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &E2E{} })
	sparkwing.Register("e2e-sub", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Sub{} })
}
`

const e2eMain = `package main

import (
	_ "e2e/jobs"

	"github.com/sparkwing-dev/sparkwing/pkg/runner"
)

func main() { runner.Main() }
`

func goRun(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// safety: this stands in for the init container's checkout, with a module
// that builds against this repository's SDK.
func e2eCheckout(t *testing.T, repoRoot string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src")
	mod := filepath.Join(src, ".sparkwing")
	for path, body := range map[string]string{
		"go.mod": "module e2e\n\ngo 1.26.0\n\nrequire github.com/sparkwing-dev/sparkwing v0.0.0\n\n" +
			"replace github.com/sparkwing-dev/sparkwing => " + repoRoot + "\n",
		"main.go":      e2eMain,
		"jobs/jobs.go": e2eJobs,
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(mod, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(mod, path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	goRun(t, mod, append(os.Environ(), "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local", "GOWORK=off"), "mod", "tidy")
	return src
}

// The whole controller-dispatched path, with the cluster replaced by a fake
// API server and each Job's pipeline container run as a process with the
// Job's own command and environment: the launcher claims the planning node,
// the pod plans through its claim token, the launcher claims each planned
// node, a node reads its dependency's output and awaits a child run that
// the controller plans and dispatches the same way, and the run succeeds.
// The init container's GitHub fetch is replaced by a prepared checkout.
func TestE2E_AControllerDispatchedRunPlansRunsAndAwaitsAChild(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the runner and a pipeline binary; run without -short")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the pod runs a POSIX process tree")
	}
	ctx := context.Background()
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "sparkwing-runner")
	hostEnv := append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
	goRun(t, repoRoot, hostEnv, "build", "-o", bin, "./cmd/sparkwing-runner")
	src := e2eCheckout(t, repoRoot)
	goCache, modCache := goRun(t, repoRoot, hostEnv, "env", "GOCACHE"), goRun(t, repoRoot, hostEnv, "env", "GOMODCACHE")

	// safety: every state change reaches the controller as a POST, and every
	// pod exit follows one, so the driver waits on these instead of a clock.
	changed := make(chan struct{}, 1)
	signal := func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	f := newLaunchFixture(t, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.ServeHTTP(w, r)
			if r.Method == http.MethodPost {
				signal()
			}
		})
	})
	f.optedInRun(t, "run-e2e")
	if _, err := f.st.DB().ExecContext(ctx, `UPDATE runs SET pipeline = 'e2e' WHERE id = 'run-e2e'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DB().ExecContext(ctx, `UPDATE triggers SET pipeline = 'e2e' WHERE id = 'run-e2e'`); err != nil {
		t.Fatal(err)
	}
	if err := f.st.CreateOrReplaceSecret(store.Secret{Name: "E2E_TOKEN", Value: "tok-e2e-value", Pipeline: "e2e", Masked: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	l := f.launcher(f.token(t, controller.ScopeClaimsLaunch))
	l.Config.ControllerURL = f.url
	home := t.TempDir()

	var wg sync.WaitGroup
	var mu sync.Mutex
	logs := map[string]*bytes.Buffer{}
	started := map[string]bool{}
	pod := func(c corev1.Container, job string) {
		defer wg.Done()
		env := []string{
			"PATH=" + os.Getenv("PATH"), "GOCACHE=" + goCache, "GOMODCACHE=" + modCache,
			"GOTOOLCHAIN=local", "GOWORK=off", "HOME=" + home, "SPARKWING_HOME=" + filepath.Join(home, "sparkwing"),
			"TMPDIR=" + os.TempDir(), "SPARKWING_SOURCE_DIR=" + src,
		}
		for _, e := range c.Env {
			switch e.Name {
			case "HOME", "SPARKWING_HOME", "GOCACHE", "GOMODCACHE", "SPARKWING_SOURCE_DIR":
			default:
				env = append(env, e.Name+"="+e.Value)
			}
		}
		var out bytes.Buffer
		cmd := exec.Command(bin, c.Args...)
		cmd.Env, cmd.Stdout, cmd.Stderr = env, &out, &out
		err := cmd.Run()
		if err != nil {
			out.WriteString("\nexit: " + err.Error())
		}
		mu.Lock()
		logs[job] = &out
		mu.Unlock()
		signal()
	}
	drive := func(until func() bool) {
		t.Helper()
		for {
			for {
				launched, err := l.LaunchOne(ctx)
				if err != nil {
					t.Fatalf("launch: %v", err)
				}
				if !launched {
					break
				}
			}
			jobs, err := f.kube.BatchV1().Jobs("sparkwing-jobs").List(ctx, metav1.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for _, j := range jobs.Items {
				if !started[j.Name] {
					started[j.Name] = true
					wg.Add(1)
					go pod(j.Spec.Template.Spec.Containers[0], j.Name)
				}
			}
			if until() {
				return
			}
			<-changed
		}
	}
	finished := func(runID string) func() bool {
		return func() bool {
			r, err := f.st.GetRun(ctx, runID)
			if err != nil {
				t.Fatal(err)
			}
			return r.Status == "success" || r.Status == "failed" || r.Status == "cancelled"
		}
	}
	drive(finished("run-e2e"))
	wg.Wait()
	parent, err := f.st.GetRun(ctx, "run-e2e")
	if err != nil {
		t.Fatal(err)
	}
	dump := func() {
		for job, out := range logs {
			t.Logf("--- %s ---\n%s", job, out)
		}
	}
	if parent.Status != "success" {
		dump()
		t.Fatalf("run-e2e = %s (%s), want success", parent.Status, parent.Error)
	}

	var doc struct {
		Source *struct {
			Depth int  `json:"depth"`
			Tags  bool `json:"tags"`
		} `json:"source"`
		Nodes []struct {
			ID       string `json:"id"`
			SpecHash string `json:"spec_hash"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(parent.PlanSnapshot, &doc); err != nil {
		dump()
		t.Fatalf("accepted plan: %v", err)
	}
	if doc.Source == nil || doc.Source.Depth != 5 || !doc.Source.Tags || len(doc.Nodes) != 2 {
		t.Fatalf("accepted plan = %+v", doc)
	}
	nodes, err := f.st.ListNodes(ctx, "run-e2e")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if n.Outcome != "success" || n.ExecutionStartedAt == nil {
			t.Errorf("node %s = %s/%s, started %v", n.NodeID, n.Status, n.Outcome, n.ExecutionStartedAt)
		}
	}
	var childID string
	if err := f.st.DB().QueryRowContext(ctx, `SELECT child_run_id FROM child_invocations WHERE parent_run_id = 'run-e2e'`).Scan(&childID); err != nil {
		t.Fatalf("child invocation: %v", err)
	}
	child, err := f.st.GetRun(ctx, childID)
	if err != nil || child.Status != "success" || child.Pipeline != "e2e-sub" {
		t.Fatalf("child run = %+v %v", child, err)
	}
	if _, err := f.st.GetNode(ctx, childID, store.PlanNodeID); err != nil {
		t.Fatalf("the child was not planned by the controller: %v", err)
	}
	for job, out := range logs {
		if strings.Contains(out.String(), "tok-e2e-value") {
			t.Errorf("%s logged the secret's value", job)
		}
		if strings.Contains(job, "check") && !strings.Contains(out.String(), "token=***") {
			t.Errorf("check's log has no masked token line:\n%s", out)
		}
	}
	if n := len(started); n != 5 {
		t.Errorf("%d Jobs ran, want 5: the parent's plan, build and check, and the child's plan and leaf", n)
	}

	// safety: the cancel reaches the running node only through its claim's
	// beat, and the launcher's next sync deletes the run's Jobs.
	f.intake(t, "run-slow", "korey/probe")
	if _, err := f.st.DB().ExecContext(ctx, `UPDATE runs SET pipeline = 'e2e-slow' WHERE id = 'run-slow'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DB().ExecContext(ctx, `UPDATE triggers SET pipeline = 'e2e-slow' WHERE id = 'run-slow'`); err != nil {
		t.Fatal(err)
	}
	drive(func() bool {
		n, err := f.st.GetNode(ctx, "run-slow", "sleep")
		return err == nil && n.Status == "running"
	})
	if err := f.st.RequestCancel(ctx, "run-slow"); err != nil {
		t.Fatal(err)
	}
	drive(finished("run-slow"))
	wg.Wait()
	if slow, err := f.st.GetRun(ctx, "run-slow"); err != nil || slow.Status != "cancelled" {
		dump()
		t.Fatalf("run-slow = %+v %v, want cancelled", slow, err)
	}
	if n, err := f.st.GetNode(ctx, "run-slow", "sleep"); err != nil || n.Outcome != "cancelled" {
		t.Fatalf("sleep node = %+v %v, want cancelled", n, err)
	}
	if _, err := l.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range f.jobNames(t) {
		if strings.Contains(name, "sleep") {
			t.Fatalf("the cancelled run's Job %s survived the sync", name)
		}
	}
	if testing.Verbose() {
		dump()
	}
}
