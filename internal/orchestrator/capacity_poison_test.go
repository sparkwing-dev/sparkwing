package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func contendedRun(t *testing.T, st *store.Store, ctx context.Context, key, runID string, peakCores float64, charge runCharge) {
	t.Helper()
	contendedRunPeaking(t, st, ctx, key, runID, peakCores, 1<<30, charge)
}

func contendedRunPeaking(t *testing.T, st *store.Store, ctx context.Context, key, runID string, peakCores float64, peakMemory int64, charge runCharge) {
	t.Helper()
	start := time.Now()
	if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: "ci", Status: "running", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddNodeMetricSample(ctx, runID, "build", store.MetricSample{
		Kind: store.MetricInterval, CPUAvailable: true, MemoryAvailable: true,
		TS: start, CPUMillicores: int64(peakCores * 1000), MemoryBytes: peakMemory,
	}); err != nil {
		t.Fatal(err)
	}
	recordRunProfile(ctx, localState{st: st}, key, runID, nil, "B", charge, true, start, start.Add(time.Second))
}

func gitRepoDir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPoisonedFloorRecoversWithoutManualReset(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	const grantable = 1.8
	charge := func() float64 {
		prof, err := st.GetPipelineProfile(ctx, "ci", "")
		if err != nil {
			t.Fatal(err)
		}
		res := capacity.Resolve(nil, prof, 2, "B")
		res, _ = capacity.ApplyHostCeiling(res, "ci", 2, grantable, 30<<30)
		return res.Cores
	}

	if got := charge(); got != 1 {
		t.Fatalf("initial charge=%v, want 1", got)
	}
	for i := range 4 {
		c := charge()
		contendedRun(t, st, ctx, "ci", fmt.Sprintf("hot%d", i), c, runCharge{Cores: c})
	}
	if c := charge(); c != grantable {
		t.Fatalf("charge = %v cores, want the ratchet to reach the grantable ceiling %v first", c, grantable)
	}

	for i := range 6 {
		contendedRun(t, st, ctx, "ci", fmt.Sprintf("calm%d", i), 0.1, runCharge{Cores: charge()})
	}

	if c := charge(); c != 0.2 {
		t.Errorf("charge = %v cores after six 0.1-core contended runs, want %v (floor decays toward evidence instead of poisoning the profile)",
			c, 0.2)
	}
}

func TestRatchetedFloorRecoversAtGrantableMemoryCeiling(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	const (
		machineCores = 8.0
		grantCores   = 7.2
		machineMem   = int64(16 << 30)
		grantMem     = int64(14 << 30)
		realDemand   = int64(2 << 30)
	)
	l, err := admission.New(admission.Config{TotalCores: machineCores, TotalMemoryBytes: uint64(machineMem)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.SetHeadroom(2.0, uint64(grantMem)); err != nil {
		t.Fatal(err)
	}

	charge := func() capacity.Resolution {
		t.Helper()
		prof, err := st.GetPipelineProfile(ctx, "repo/ci", "")
		if err != nil {
			t.Fatal(err)
		}
		res := capacity.Resolve(nil, prof, 8, "B")
		res, _ = capacity.ApplyHostCeiling(res, "repo/ci", machineCores, grantCores, grantMem)
		return res
	}

	contendedRunPeaking(t, st, ctx, "repo/ci", "hot", 1.0, 7500*(1<<20), runCharge{Cores: 1.0, MemoryBytes: 8 << 30})
	ratcheted := charge()
	if ratcheted.Source != store.CostSourceFloor {
		t.Fatalf("charge source = %q, want the demand floor to be pricing this pipeline", ratcheted.Source)
	}
	if ratcheted.MemoryBytes > grantMem {
		t.Fatalf("charge = %d bytes, want it capped at the grantable %d: an uncapped charge is a permanent refusal",
			ratcheted.MemoryBytes, grantMem)
	}

	for i := range 4 {
		res := charge()
		dec, _, err := l.Submit(admission.Request{
			ID:          fmt.Sprintf("cold%d", i),
			Cores:       res.Cores,
			SoftCores:   true,
			MemoryBytes: uint64(res.MemoryBytes),
		})
		if err != nil {
			t.Fatalf("cycle %d: Submit: %v", i, err)
		}
		if dec.Kind != admission.DecisionGranted {
			t.Fatalf("cycle %d: charge %d bytes was %s against the grantable memory ceiling. A run that never starts never measures, so the floor can never come down",
				i, res.MemoryBytes, dec.Kind)
		}
		contendedRunPeaking(t, st, ctx, "repo/ci", fmt.Sprintf("cold%d", i), 1.0, realDemand,
			runCharge{Cores: res.Cores, MemoryBytes: res.MemoryBytes})
		if _, err := l.Release(dec.Lease.ID, fmt.Sprintf("cold%d", i)); err != nil {
			t.Fatalf("cycle %d: Release: %v", i, err)
		}
	}

	if got := float64(charge().MemoryBytes); got > capacity.SafetyMultiple*float64(realDemand) {
		t.Errorf("charge = %v bytes after four measured runs, want <= %v: the floor must converge on measured demand, not stay parked at the ceiling",
			got, capacity.SafetyMultiple*float64(realDemand))
	}
}

func TestContentionInOneRepoLeavesAnothersPricingAlone(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	const grantable = 1.8

	resolve := func() capacity.Resolution {
		t.Helper()
		key := currentProfileKey("ci")
		prof, err := st.GetPipelineProfile(ctx, key, "")
		if err != nil {
			t.Fatal(err)
		}
		res := capacity.Resolve(nil, prof, 2, "B")
		res, _ = capacity.ApplyHostCeiling(res, key, 2, grantable, 30<<30)
		return res
	}

	previousWorkDir := sparkwing.CurrentRuntime().WorkDir
	t.Cleanup(func() { sparkwing.SetWorkDir(previousWorkDir) })
	alpha := gitRepoDir(t, "alpha")
	sparkwing.SetWorkDir(alpha)
	t.Chdir(alpha)
	if got := resolve().Cores; got != 1 {
		t.Fatalf("initial charge=%v, want 1", got)
	}
	for i := range 4 {
		c := resolve().Cores
		contendedRun(t, st, ctx, currentProfileKey("ci"), fmt.Sprintf("alpha%d", i), c, runCharge{Cores: c})
	}
	if res := resolve(); res.Source != store.CostSourceFloor || res.Cores != grantable {
		t.Fatalf("alpha priced %v cores from %q, want the contended floor ratcheted to the grantable ceiling %v first",
			res.Cores, res.Source, grantable)
	}

	beta := gitRepoDir(t, "beta")
	sparkwing.SetWorkDir(beta)
	t.Chdir(beta)
	res := resolve()
	if res.Source != store.CostSourceDefault {
		t.Errorf("beta priced %v cores from %q, want the cold-start default: alpha's contention poisoned an unmeasured pipeline in another repo",
			res.Cores, res.Source)
	}
	if res.Cores != 1 {
		t.Errorf("beta charged %v cores, want the 1-core cold-start half of a 2-core machine", res.Cores)
	}
}
