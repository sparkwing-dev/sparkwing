package orchestrator

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// wingdBurnerPipe runs a make -j-style parallel burst: eight child processes
// each churning CPU, reaped together. It exercises the real
// measurement -> profile chain so the stored peak can be checked against host
// capacity end to end.
type wingdBurnerPipe struct{ sparkwing.Base }

func (wingdBurnerPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "burn", func(ctx context.Context) error {
		const script = "for i in 0 1 2 3 4 5 6 7; do awk 'BEGIN{s=0;for(i=0;i<20000000;i++)s+=i}' & done; wait"
		_, err := sparkwing.Exec(ctx, "sh", "-c", script).Run()
		return err
	})
	return nil
}

var wingdCapacityE2ERegister sync.Once

func registerWingdCapacityE2EPipelines() {
	wingdCapacityE2ERegister.Do(func() {
		sparkwing.Register[sparkwing.NoInputs]("wingd-e2e-burner",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return wingdBurnerPipe{} })
	})
}

// TestWingd_ParallelBurnerProfilePeakStaysWithinHost drives the measurement
// fix end to end: a parallel child burst is measured, folded into the stored
// profile, and its recorded peak stays within host capacity -- the reap-burst
// overshoot that recorded 18.9 cores on a 14-core box can no longer happen.
func TestWingd_ParallelBurnerProfilePeakStaysWithinHost(t *testing.T) {
	registerWingdCapacityE2EPipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 64)
	backends, st, _ := openWingdBackends(t, home)

	res, err := Run(context.Background(), backends, Options{
		Pipeline:  "wingd-e2e-burner",
		RunID:     "burner-run",
		Admission: testWingdAdmission(home, nil),
	})
	if err != nil || res == nil || res.Status != "success" {
		t.Fatalf("burner run: status=%v err=%v", res, err)
	}

	prof, err := st.GetPipelineProfile(context.Background(), "wingd-e2e-burner", "")
	if err != nil || prof == nil {
		t.Fatalf("burner profile missing: %v", err)
	}
	host := float64(runtime.NumCPU())
	if prof.PeakCores <= 0 {
		t.Fatalf("profile peak = %v, want a positive measured peak from the burst", prof.PeakCores)
	}
	if prof.PeakCores > host {
		t.Fatalf("profile peak = %v exceeds host %v; the reap-burst overshoot was not clamped", prof.PeakCores, host)
	}
}

// TestWingd_OversizedMeasuredCostRunsAloneNeverBricks reproduces the field-
// down state -- a measured peak far above host capacity (18.9 cores) -- and
// proves the fix: the run is admitted alone at a clamped charge rather than
// rejected as never-admissible, and a second run of the same oversized
// pipeline serializes behind it instead of bricking the pipeline.
func TestWingd_OversizedMeasuredCostRunsAloneNeverBricks(t *testing.T) {
	registerWingdE2EPipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 8)
	backends, st, _ := openWingdBackends(t, home)
	profilePlan := sparkwing.NewPlan()
	_ = (wingdUnpinnedHoldPipe{}).Plan(context.Background(), profilePlan, sparkwing.NoInputs{}, sparkwing.RunContext{})
	seedProfile(t, st, "wingd-e2e-unpinned", profilePlan.Job("hold"), store.ProfileObservation{
		Duration: 10 * time.Second, PeakCores: 18.9, PeakMemoryBytes: 1 << 30, CPUMeasured: true,
	}, capacity.MinSamples)

	gate := newWingdGate()
	wingdE2EGate.Store(gate)

	runA := make(chan *Result, 1)
	go func() {
		res, _ := Run(context.Background(), backends, Options{
			Pipeline:  "wingd-e2e-unpinned",
			RunID:     "oversized-a",
			Admission: testWingdAdmission(home, nil),
		})
		runA <- res
	}()
	gate.awaitStarted(t, "oversized-a")

	h := findWingdHolder(t, home, "oversized-a")
	if h.CostSource != "measured" {
		t.Errorf("CostSource = %q, want measured", h.CostSource)
	}
	if h.Resources.Cores <= 0 || h.Resources.Cores > 8 {
		t.Errorf("admitted cores = %v, want a run-alone charge clamped within host capacity", h.Resources.Cores)
	}

	runB := make(chan *Result, 1)
	go func() {
		res, _ := Run(context.Background(), backends, Options{
			Pipeline:  "wingd-e2e-unpinned",
			RunID:     "oversized-b",
			Admission: testWingdAdmission(home, nil),
		})
		runB <- res
	}()
	awaitWaiter(t, home, "oversized-b")

	close(gate.release)
	for _, ch := range []chan *Result{runA, runB} {
		select {
		case res := <-ch:
			if res == nil || res.Status != "success" {
				t.Fatalf("run result = %+v, want success (never never_admissible)", res)
			}
		case <-time.After(wingdTestWait):
			t.Fatal("run did not finish")
		}
	}
	if got := gate.peak.Load(); got != 1 {
		t.Fatalf("peak concurrent holds = %d, want run-alone serialization", got)
	}
}
