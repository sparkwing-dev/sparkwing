package sparkwing

import (
	"context"
	"math"
	"testing"
)

func resourcesTestJob(ctx context.Context) error { return nil }

func TestPlanResources_StoresCoresAndMemoryBytes(t *testing.T) {
	p := NewPlan()
	p.Resources(Cores(2.5), MemoryGB(4))
	h := p.resourceHints()
	if h == nil {
		t.Fatal("ResourceHints() = nil after Resources()")
	}
	if h.Cores != 2.5 {
		t.Errorf("Cores = %v, want 2.5", h.Cores)
	}
	if want := int64(4 * (1 << 30)); h.MemoryBytes != want {
		t.Errorf("MemoryBytes = %d, want %d", h.MemoryBytes, want)
	}
}

func TestPlanResources_AbsentByDefault(t *testing.T) {
	if h := NewPlan().resourceHints(); h != nil {
		t.Fatalf("ResourceHints() = %+v, want nil for an undeclared plan", h)
	}
}

func TestPlanResources_RepeatedCallsMergePerDimension(t *testing.T) {
	p := NewPlan()
	p.Resources(Cores(1), MemoryGB(2))
	p.Resources(Cores(8))
	h := p.resourceHints()
	if h.Cores != 8 {
		t.Errorf("Cores = %v, want 8 (second call overwrites)", h.Cores)
	}
	if want := int64(2 * (1 << 30)); h.MemoryBytes != want {
		t.Errorf("MemoryBytes = %d, want %d (first call preserved)", h.MemoryBytes, want)
	}
}

func TestPlanResources_NoArgsClears(t *testing.T) {
	p := NewPlan()
	p.Resources(Cores(2))
	p.Resources()
	if h := p.resourceHints(); h != nil {
		t.Fatalf("ResourceHints() = %+v, want nil after clearing", h)
	}
}

func TestPlanResourceHints_ReturnsCopy(t *testing.T) {
	p := NewPlan()
	p.Resources(Cores(2))
	p.resourceHints().Cores = 99
	if got := p.resourceHints().Cores; got != 2 {
		t.Errorf("Cores = %v after mutating a returned copy, want 2", got)
	}
}

func TestNodeResources_StoresHints(t *testing.T) {
	p := NewPlan()
	n := Job(p, "build", resourcesTestJob).Resources(Cores(0.5), MemoryGB(1.5))
	h := n.resourceHints()
	if h == nil {
		t.Fatal("ResourceHints() = nil after Resources()")
	}
	if h.Cores != 0.5 {
		t.Errorf("Cores = %v, want 0.5", h.Cores)
	}
	if want := int64(math.Round(1.5 * float64(1<<30))); h.MemoryBytes != want {
		t.Errorf("MemoryBytes = %d, want %d", h.MemoryBytes, want)
	}
}

func TestNodeResources_AbsentByDefault(t *testing.T) {
	p := NewPlan()
	if h := Job(p, "build", resourcesTestJob).resourceHints(); h != nil {
		t.Fatalf("ResourceHints() = %+v, want nil for an undeclared node", h)
	}
}

func TestNodeResources_NoArgsClears(t *testing.T) {
	p := NewPlan()
	n := Job(p, "build", resourcesTestJob).Resources(Cores(2))
	n.Resources()
	if h := n.resourceHints(); h != nil {
		t.Fatalf("ResourceHints() = %+v, want nil after clearing", h)
	}
}

func TestGroupResources_AppliesToEveryMember(t *testing.T) {
	p := NewPlan()
	a := Job(p, "a", resourcesTestJob)
	b := Job(p, "b", resourcesTestJob)
	GroupJobs(p, "pair", a, b).Resources(Cores(3))
	for _, n := range []*JobNode{a, b} {
		h := n.resourceHints()
		if h == nil || h.Cores != 3 {
			t.Errorf("node %q hints = %+v, want Cores 3", n.ID(), h)
		}
	}
}

func TestCores_RejectsNegativeAndNonFinite(t *testing.T) {
	for _, bad := range []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Cores(%v) did not panic", bad)
				}
			}()
			Cores(bad)
		}()
	}
}

func TestMemoryGB_RejectsNegativeAndNonFinite(t *testing.T) {
	for _, bad := range []float64{-0.5, math.NaN(), math.Inf(1)} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("MemoryGB(%v) did not panic", bad)
				}
			}()
			MemoryGB(bad)
		}()
	}
}

func TestResources_PreserveExplicitZero(t *testing.T) {
	plan := NewPlan().Resources(Cores(0), MemoryGB(0))
	if hints := plan.resourceHints(); hints == nil || !hints.coresSet || !hints.memorySet || hints.Cores != 0 || hints.MemoryBytes != 0 {
		t.Fatalf("explicit zero resource pins lost: %+v", hints)
	}
}
