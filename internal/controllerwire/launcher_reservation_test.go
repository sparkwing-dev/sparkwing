package controllerwire

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestLauncherObservationRequiresOneCleanupIdentity(t *testing.T) {
	observation := LauncherObservation{Award: capability.LeaseRef{Kind: capability.UnitPlan, ID: "unit", Holder: "launcher", Generation: 1}, NodeUID: "node", ReservationID: "reservation"}
	if err := observation.Validate(); err != nil {
		t.Fatal(err)
	}
	observation.Job = LauncherJobRef{Namespace: "cloud", Name: "job", UID: "uid"}
	if err := observation.Validate(); err == nil {
		t.Fatal("ambiguous Job and reservation cleanup accepted")
	}
	observation.ReservationID = ""
	if err := observation.Validate(); err != nil {
		t.Fatal(err)
	}
	observation.Job = LauncherJobRef{}
	if err := observation.Validate(); err == nil {
		t.Fatal("observation without cleanup identity accepted")
	}
}
