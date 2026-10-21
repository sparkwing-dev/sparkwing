package launcher

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestCloudClaimAndPublicationAllowUnexpiringServiceRoot(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			unit := wireAward(kind)
			unit.ParentCapabilityExpiry = nil
			control, err := NewControllerControl(launcherWireFake{claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				return controllerwire.ClaimResponse{Claim: &unit}, nil
			}}, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{kind}})
			if err != nil {
				t.Fatal(err)
			}
			award, err := control.Claim(t.Context(), "cloud", nil)
			if err != nil {
				t.Fatal(err)
			}
			l, _, boundary, _ := fixture()
			r, err := BuildJob(t.Context(), time.Now(), l.Config, *award, boundary)
			if err != nil {
				t.Fatal(err)
			}
			if r.JobDeadline.IsZero() || !award.Binding.ParentCapabilityExpiry.IsZero() {
				t.Fatal("ancestor expiry changed execution budget")
			}
		})
	}
}
