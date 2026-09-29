package orchestrator

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type wingdBurnerPipe struct{ sparkwing.Base }

func (wingdBurnerPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "burn", func(ctx context.Context) error {
		const script = "for i in 0 1; do awk 'BEGIN{s=0;for(i=0;i<20000000;i++)s+=i}' & done; wait"
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

func TestWingd_EmbeddedBurnerRetainsCommandUsageWithoutLearning(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1.5s of real work; the fast class runs under -short")
	}
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

	prof, err := st.GetPipelineProfile(context.Background(), currentProfileKey("wingd-e2e-burner"), "")
	if err != nil {
		t.Fatal(err)
	}
	if prof != nil && (prof.SampleCount != 0 || prof.CPUMeasured) {
		t.Fatalf("embedded estimates qualified as measured profile: %+v", prof)
	}
	samples, err := st.ListNodeMetrics(t.Context(), "burner-run", "burn")
	if err != nil {
		t.Fatal(err)
	}
	var commands int
	for _, sample := range samples {
		if sample.Kind == store.MetricInterval {
			t.Fatalf("embedded execution reported exclusive interval: %+v", sample)
		}
		if sample.Kind == store.MetricCommand && sample.CPUAvailable && sample.CPUTime > 0 {
			commands++
		}
	}
	if commands == 0 {
		t.Fatal("busy subprocess has no retained command CPU measurement")
	}

}

func TestWingd_OversizedMeasuredCostRunsAloneNeverBricks(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	registerWingdE2EPipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 8)
	backends, st, _ := openWingdBackends(t, home)
	seedNodeProfile(t, st, "wingd-e2e-unpinned", "hold", store.ProfileObservation{
		Duration: 10 * time.Second, PeakCores: 18.9, SustainedCores: new(18.9), PeakMemoryBytes: 1 << 30, CPUMeasured: true,
	}, capacity.MinSamples)

	gate := newWingdGate()
	wingdE2EGate.Store(gate)

	ctx, cancel := context.WithCancel(context.Background())
	var runs sync.WaitGroup
	var released sync.Once
	release := func() { released.Do(func() { close(gate.release) }) }
	t.Cleanup(func() { cancel(); release(); runs.Wait(); wingdE2EGate.Store(nil) })
	runA := make(chan *Result, 1)
	runs.Add(1)
	go func() {
		defer runs.Done()
		res, _ := Run(ctx, backends, Options{
			Pipeline:  "wingd-e2e-unpinned",
			RunID:     "oversized-a",
			Admission: testWingdAdmission(home, nil),
		})
		runA <- res
	}()
	gate.awaitStarted(t, "oversized-a")

	h := findWingdHolder(t, home, nodeHostRunID("oversized-a", "hold"))
	if h.CostSource != "measured" {
		t.Errorf("CostSource = %q, want measured", h.CostSource)
	}
	if h.Resources.Cores <= 0 || h.Resources.Cores > 8 {
		t.Errorf("admitted cores = %v, want a run-alone charge clamped within host capacity", h.Resources.Cores)
	}

	runB := make(chan *Result, 1)
	runs.Add(1)
	go func() {
		defer runs.Done()
		res, _ := Run(ctx, backends, Options{
			Pipeline:  "wingd-e2e-unpinned",
			RunID:     "oversized-b",
			Admission: testWingdAdmission(home, nil),
		})
		runB <- res
	}()
	awaitWaiter(t, home, nodeHostRunID("oversized-b", "hold"))

	release()
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
