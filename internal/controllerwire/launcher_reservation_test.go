package controllerwire

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestLauncherObservationChecksAwardAndState(t *testing.T) {
	observation := capability.LauncherObservation{Award: capability.LeaseRef{Kind: capability.UnitPlan, ID: "unit", Holder: "launcher", Generation: 1}, Reservation: true}
	if err := observation.Validate(); err != nil {
		t.Fatal(err)
	}
	observation.Rejected = true
	if err := observation.Validate(); err == nil {
		t.Fatal("reservation reported Job rejection")
	}
	observation.Reservation = false
	if err := observation.Validate(); err != nil {
		t.Fatal(err)
	}
	observation.Award.Generation = 0
	if err := observation.Validate(); err == nil {
		t.Fatal("unfenced award accepted")
	}
}
