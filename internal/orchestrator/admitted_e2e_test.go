package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type admittedRead struct {
	share sparkwing.Admission
	ok    bool
}

type admittedProbe struct{ reads chan admittedRead }

var (
	admittedProbeSink atomic.Pointer[admittedProbe]
	admittedRegister  sync.Once
)

func reportAdmitted(ctx context.Context) error {
	share, ok := sparkwing.Admitted(ctx)
	if p := admittedProbeSink.Load(); p != nil {
		p.reads <- admittedRead{share: share, ok: ok}
	}
	return nil
}

type admittedProbePipe struct {
	sparkwing.Base
	cores  float64
	locked bool
}

func (p admittedProbePipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	if p.cores > 0 {
		plan.Resources(sparkwing.Cores(p.cores))
	}
	job := sparkwing.Job(plan, "read-share", reportAdmitted)
	if p.locked {
		job.Concurrency(sparkwing.NewConcurrencyGroup("admitted-probe-lock", sparkwing.ConcurrencyLimit{
			Capacity: 1,
			Scope:    sparkwing.ScopeBox,
			OnLimit:  sparkwing.Queue,
		}))
	}
	return nil
}

type admittedProbeParentPipe struct{ sparkwing.Base }

func (admittedProbeParentPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	plan.Resources(sparkwing.Cores(2))
	sparkwing.Job(plan, "spawn-child", func(ctx context.Context) error {
		_, token, _ := localAdmissionFromContext(ctx)
		if token == "" {
			return errors.New("node context carries no local admission lease")
		}
		launch := wingdE2EChild.Load()
		childCtx, cancel := context.WithTimeout(ctx, wingdTestWait)
		defer cancel()
		res, err := Run(childCtx, launch.backends, Options{
			Pipeline: "admitted-probe-child",
			RunID:    "admitted-probe-child-run",
			Admission: &LocalAdmission{
				Home:             launch.home,
				Version:          "test",
				ParentLeaseToken: token,
				Out:              io.Discard,
				Spawn:            func(string, string) error { return errors.New("no daemon running for test home") },
			},
		})
		if err != nil {
			return fmt.Errorf("child run: %w", err)
		}
		if res.Status != "success" {
			return fmt.Errorf("child status %q: %w", res.Status, res.Error)
		}
		return nil
	})
	return nil
}

func registerAdmittedProbePipelines() {
	admittedRegister.Do(func() {
		sparkwing.Register[sparkwing.NoInputs]("admitted-probe-pinned",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return admittedProbePipe{cores: 2} })
		sparkwing.Register[sparkwing.NoInputs]("admitted-probe-unpinned",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return admittedProbePipe{} })
		sparkwing.Register[sparkwing.NoInputs]("admitted-probe-locked",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return admittedProbePipe{cores: 1, locked: true} })
		sparkwing.Register[sparkwing.NoInputs]("admitted-probe-child",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return admittedProbePipe{} })
		sparkwing.Register[sparkwing.NoInputs]("admitted-probe-parent",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return admittedProbeParentPipe{} })
	})
}

func runAdmittedProbe(t *testing.T, home string, backends Backends, pipeline, runID string) admittedRead {
	t.Helper()
	probe := &admittedProbe{reads: make(chan admittedRead, 4)}
	admittedProbeSink.Store(probe)
	t.Cleanup(func() { admittedProbeSink.Store(nil) })

	res, err := Run(context.Background(), backends, Options{
		Pipeline:  pipeline,
		RunID:     runID,
		Admission: testWingdAdmission(home, nil),
	})
	if err != nil {
		t.Fatalf("run %s: %v", pipeline, err)
	}
	if res.Status != "success" {
		t.Fatalf("run %s: status %q: %v", pipeline, res.Status, res.Error)
	}
	select {
	case read := <-probe.reads:
		return read
	default:
		t.Fatalf("run %s reported success, but the step never read its share", pipeline)
		return admittedRead{}
	}
}

func TestAdmitted_PinnedRunReadsThePin(t *testing.T) {
	registerAdmittedProbePipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 8)
	backends, _, _ := openWingdBackends(t, home)

	got := runAdmittedProbe(t, home, backends, "admitted-probe-pinned", "admitted-pinned")
	want := admittedRead{share: sparkwing.Admission{Cores: 2}, ok: true}
	if got != want {
		t.Errorf("step read %+v, want %+v", got, want)
	}
}

func TestAdmitted_UnpinnedRunReadsTheMeasuredNodeCharge(t *testing.T) {
	registerAdmittedProbePipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 8)
	backends, st, _ := openWingdBackends(t, home)
	seedNodeProfile(t, st, "admitted-probe-unpinned", "read-share", store.ProfileObservation{
		Duration: 20 * time.Second, PeakCores: 1.5, PeakMemoryBytes: 1 << 30,
	}, 3)

	got := runAdmittedProbe(t, home, backends, "admitted-probe-unpinned", "admitted-unpinned")
	want := admittedRead{share: sparkwing.Admission{Cores: 1.5, MemoryBytes: 1 << 30}, ok: true}
	if got != want {
		t.Errorf("step read %+v, want %+v", got, want)
	}
}

func TestAdmitted_NodeUnderAConcurrencyGroupReadsItsCharge(t *testing.T) {
	registerAdmittedProbePipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 8)
	backends, _, _ := openWingdBackends(t, home)

	got := runAdmittedProbe(t, home, backends, "admitted-probe-locked", "admitted-locked")
	want := admittedRead{share: sparkwing.Admission{Cores: 1}, ok: true}
	if got != want {
		t.Errorf("step read %+v, want %+v", got, want)
	}
}

func TestAdmitted_ChildOnAParentLeaseReadsTheParentShare(t *testing.T) {
	registerAdmittedProbePipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 8)
	backends, _, _ := openWingdBackends(t, home)
	wingdE2EChild.Store(&wingdChildLaunch{home: home, backends: backends, result: make(chan *Result, 1)})
	t.Cleanup(func() { wingdE2EChild.Store(nil) })

	got := runAdmittedProbe(t, home, backends, "admitted-probe-parent", "admitted-parent")
	want := admittedRead{share: sparkwing.Admission{Cores: 2}, ok: true}
	if got != want {
		t.Errorf("child step read %+v, want %+v -- a child runs inside the parent's reservation", got, want)
	}
}
