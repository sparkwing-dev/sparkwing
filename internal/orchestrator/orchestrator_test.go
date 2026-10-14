package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/testhome"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var registerOnce sync.Map

func register(name string, factory func() sparkwing.Pipeline[sparkwing.NoInputs]) {
	if _, loaded := registerOnce.LoadOrStore(name, struct{}{}); loaded {
		return
	}
	sparkwing.Register[sparkwing.NoInputs](name, factory)
}

type okPipe struct{ sparkwing.Base }

func (okPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, func(ctx context.Context) error {
		sparkwing.Info(ctx, "work complete")
		return nil
	})
	return nil
}

type failPipe struct{ sparkwing.Base }

func (failPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, func(ctx context.Context) error { return errors.New("boom") })
	return nil
}

type fanOutOK struct{ sparkwing.Base }

func (fanOutOK) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	setup := sparkwing.Job(plan, "setup", func(ctx context.Context) error { return nil })
	a := sparkwing.Job(plan, "a", func(ctx context.Context) error { return nil }).Needs(setup)
	b := sparkwing.Job(plan, "b", func(ctx context.Context) error { return nil }).Needs(setup)
	sparkwing.Job(plan, "fin", func(ctx context.Context) error { return nil }).Needs(a, b)
	return nil
}

type middleFails struct{ sparkwing.Base }

func (middleFails) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	a := sparkwing.Job(plan, "a", func(ctx context.Context) error { return nil })
	b := sparkwing.Job(plan, "b", func(ctx context.Context) error { return errors.New("mid fail") }).Needs(a)
	sparkwing.Job(plan, "c", func(ctx context.Context) error { return nil }).Needs(b)
	return nil
}

var inflightCancelStarted chan struct{}

type cancelInflight struct{ sparkwing.Base }

func (cancelInflight) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "victim", func(ctx context.Context) error {
		close(inflightCancelStarted)
		<-ctx.Done()
		return ctx.Err()
	})
	return nil
}

type refBuildOut struct {
	Tag string `json:"tag"`
}
type refBuild struct {
	sparkwing.Base
	sparkwing.Produces[refBuildOut]
}

func (j *refBuild) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", j.run), nil
}

func (refBuild) run(ctx context.Context) (refBuildOut, error) {
	return refBuildOut{Tag: "v9"}, nil
}

type refDeploy struct {
	sparkwing.Base
	Build sparkwing.Ref[refBuildOut]
}

func (d *refDeploy) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", d.run)
	return nil, nil
}

func (d *refDeploy) run(ctx context.Context) error {
	got := d.Build.Get(ctx)
	if got.Tag != "v9" {
		return fmt.Errorf("ref got %q, want v9", got.Tag)
	}
	return nil
}

type refPipe struct{ sparkwing.Base }

func (refPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	build := sparkwing.Job(plan, "build", &refBuild{})
	sparkwing.Job(plan, "deploy", &refDeploy{Build: sparkwing.RefTo[refBuildOut](build)}).Needs(build)
	return nil
}

type sharedMemoryOut struct {
	Tag    string  `json:"tag"`
	Handle *string `json:"-"`
}

type sharedMemoryBuild struct {
	sparkwing.Base
	sparkwing.Produces[sharedMemoryOut]
}

func (j *sharedMemoryBuild) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", j.run), nil
}

func (sharedMemoryBuild) run(ctx context.Context) (sharedMemoryOut, error) {
	live := "only-in-this-process"
	return sharedMemoryOut{Tag: "v9", Handle: &live}, nil
}

type sharedMemoryDeploy struct {
	sparkwing.Base
	Build sparkwing.Ref[sharedMemoryOut]
}

func (d *sharedMemoryDeploy) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", d.run)
	return nil, nil
}

func (d *sharedMemoryDeploy) run(ctx context.Context) error {
	got := d.Build.Get(ctx)
	if got.Tag != "v9" {
		return fmt.Errorf("ref got tag %q, want v9", got.Tag)
	}
	if got.Handle == nil {
		return fmt.Errorf("handle did not survive the ref")
	}
	return nil
}

type sharedMemoryPipe struct{ sparkwing.Base }

func (sharedMemoryPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	build := sparkwing.Job(plan, "build", &sharedMemoryBuild{})
	sparkwing.Job(plan, "deploy", &sharedMemoryDeploy{Build: sparkwing.RefTo[sharedMemoryOut](build)}).Needs(build)
	return nil
}

func init() {
	register("orch-ok", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &okPipe{} })
	register("orch-fail", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &failPipe{} })
	register("orch-fanout-ok", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &fanOutOK{} })
	register("orch-middle-fails", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &middleFails{} })
	register("orch-ref", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &refPipe{} })
	register("orch-ref-shared-memory", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &sharedMemoryPipe{} })
	register("orch-cancel-inflight", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &cancelInflight{} })
}

func newPaths(t *testing.T) orchestrator.Paths {
	t.Helper()
	isolateProfiles(t)
	root := t.TempDir()
	p := orchestrator.PathsAt(root)
	if err := p.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	return p
}

func newPathsWithStore(t *testing.T) orchestrator.Paths {
	t.Helper()
	p := newPaths(t)
	st, err := teststore.Open(p.StateDB())
	if err != nil {
		t.Fatalf("seed state: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close seed state: %v", err)
	}
	return p
}

func TestNewPathsLeavesStateDatabaseAbsent(t *testing.T) {
	p := newPaths(t)
	if _, err := os.Stat(p.StateDB()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state database exists: %v", err)
	}
}

func isolateProfiles(t *testing.T) {
	t.Helper()
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	testhome.Set(t, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
}
