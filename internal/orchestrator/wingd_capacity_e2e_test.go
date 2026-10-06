package orchestrator

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/nodemetrics"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type wingdCommandOnlyPipe struct{ sparkwing.Base }

func (wingdCommandOnlyPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "command", func(ctx context.Context) error {
		_, err := sparkwing.Exec(ctx, "sh", "-c", ":").Run()
		return err
	})
	return nil
}

var wingdCapacityE2ERegister sync.Once

func registerWingdCapacityE2EPipelines() {
	wingdCapacityE2ERegister.Do(func() {
		sparkwing.Register[sparkwing.NoInputs]("wingd-e2e-command-only",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return wingdCommandOnlyPipe{} })
	})
}

func TestWingd_UnownedCommandRunDoesNotLearnResourceProfile(t *testing.T) {
	t.Cleanup(nodemetrics.SetIntervalForTest(time.Hour))
	registerWingdCapacityE2EPipelines()
	home := wingdTestHome(t)
	startWingd(t, home, 64)
	backends, st, _ := openWingdBackends(t, home)

	res, err := Run(context.Background(), backends, Options{
		Pipeline:  "wingd-e2e-command-only",
		RunID:     "command-only-run",
		Admission: testWingdAdmission(home, nil),
	})
	if err != nil || res == nil || res.Status != "success" {
		t.Fatalf("command run: status=%v err=%v", res, err)
	}

	samples, err := st.ListNodeMetrics(t.Context(), "command-only-run", "command")
	if err != nil || len(samples) == 0 {
		t.Fatalf("command evidence missing: %+v, %v", samples, err)
	}
	command, unknown := false, false
	for _, sample := range samples {
		switch sample.Kind {
		case store.MetricCommand:
			command = true
		case store.MetricUnknown:
			unknown = true
		default:
			t.Fatalf("unowned execution contains interval evidence: %+v", sample)
		}
	}
	wantCommand := runtime.GOOS != "windows"
	if command != wantCommand || !unknown {
		t.Fatalf("command=%v unknown=%v, want command=%v and unknown attribution", command, unknown, wantCommand)
	}
	for _, node := range []string{"", "command"} {
		prof, err := st.GetPipelineProfile(t.Context(), currentProfileKey("wingd-e2e-command-only"), node)
		if err != nil {
			t.Fatal(err)
		}
		if prof != nil && (prof.SampleCount != 0 || prof.CPUMeasured || prof.PeakCores != 0 || prof.PeakMemoryBytes != 0) {
			t.Fatalf("command-only %q learned a resource profile: %+v", node, prof)
		}
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

	h := findWingdHolder(t, home, nodeHostRunID("oversized-a", "hold"))
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
	awaitWaiter(t, home, nodeHostRunID("oversized-b", "hold"))

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
