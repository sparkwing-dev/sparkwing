package capability

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestLauncherCapacityFailureValidateRefusesBothReasons(t *testing.T) {
	for _, kind := range []UnitKind{UnitPlan, UnitEval, UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			observation := LauncherObservation{Award: LeaseRef{Kind: kind, ID: "unit", Holder: "holder", Generation: 1}, Reservation: true}
			for _, unavailable := range []bool{false, true} {
				observation.CapacityUnconfigured, observation.CapacityUnavailable = !unavailable, unavailable
				if err := observation.Validate(); err != nil {
					t.Fatal(err)
				}
			}
			observation.CapacityUnconfigured, observation.CapacityUnavailable = true, true
			requireCode(t, observation.Validate(), apierr.BadRequest)
		})
	}
}

func TestLauncherCapacityCeilingNamesRequestCeilingAndRemedy(t *testing.T) {
	got := CloudCapacityCeilingMessage(match.Resources{Cores: 4, MemoryBytes: 13743685632}, match.Resources{Cores: 2, MemoryBytes: 8 << 30}, "run-1")
	want := "Requested class sparkwing-4cpu needs 4 CPU and 12.8 GiB memory, above this Cloud pool's ceiling of 2 CPU and 8 GiB memory. Request a smaller hardware class, or contact support@sparkwing.dev to raise the ceiling (Support reference: run-1)"
	if got != want {
		t.Fatalf("ceiling message:\n got %q\nwant %q", got, want)
	}
	fence := LeaseRef{Kind: UnitNode, ID: "unit", Holder: "holder", Generation: 1}
	ceiling := &match.Resources{Cores: 2, MemoryBytes: 8 << 30}
	for name, observation := range map[string]LauncherObservation{
		"without failure": {Award: fence, Reservation: true, CapacityCeiling: ceiling},
		"breaker":         {Award: fence, Reservation: true, CapacityUnavailable: true, CapacityCeiling: ceiling},
		"empty ceiling":   {Award: fence, Reservation: true, CapacityUnconfigured: true, CapacityCeiling: &match.Resources{}},
	} {
		if err := observation.Validate(); err == nil {
			t.Fatalf("%s: accepted a misplaced ceiling", name)
		}
	}
	if err := (LauncherObservation{Award: fence, Reservation: true, CapacityUnconfigured: true, CapacityCeiling: ceiling}).Validate(); err != nil {
		t.Fatal(err)
	}
}
