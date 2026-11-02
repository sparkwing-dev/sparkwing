package launcher

import (
	"context"
	"errors"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestRunAndRetryPlanHandoffCreateOnlyOneNodeClaim(t *testing.T) {
	for _, retry := range []bool{false, true} {
		for _, restart := range []bool{false, true} {
			t.Run(fmt.Sprintf("retry=%v/restart=%v", retry, restart), func(t *testing.T) {
				l, kube, _, control := fixture()
				boundary, _ := boundaryFixture()
				boundary.Kube, l.Boundary = kube, boundary
				p := &pool{t: t, b: boundary, kube: kube}
				for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitNode} {
					unit := wireAward(kind)
					unit.UnitID = string(kind)
					unit.CloudResources.Cores = 2
					if retry {
						unit.Request.RunID = "original"
						if unit.Accepted != nil {
							unit.Accepted.Spec.Request = unit.Request
						}
					}
					projector, err := NewControllerControl(launcherWireFake{claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
						return controllerwire.ClaimResponse{Claim: &unit}, nil
					}}, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{kind}})
					if err != nil {
						t.Fatal(err)
					}
					control.award, err = projector.Claim(t.Context(), "cloud", nil)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = l.LaunchOne(t.Context()); !errors.Is(err, ErrNoDedicatedNode) {
						t.Fatalf("%s did not wait for placement: %v", kind, err)
					}
					control.award = nil
					p.schedule(false)
					if p.nodes != 1 {
						t.Fatalf("%s requested %d NodeClaims; plan and work need only one", kind, p.nodes)
					}
					if kind == capability.UnitPlan {
						if err := l.placeWaiting(t.Context()); err != nil {
							t.Fatal(err)
						}
						if restart {
							l = &Launcher{Kube: kube, Control: control, Config: l.Config, Boundary: boundary}
						}
					}
				}
				control.remove = true
				if err := l.Sync(t.Context()); err != nil {
					t.Fatal(err)
				}
				control.remove = false
				if err := l.placeWaiting(t.Context()); err != nil {
					t.Fatal(err)
				}
				jobs, err := kube.BatchV1().Jobs("cloud").List(t.Context(), metav1.ListOptions{})
				if err != nil || len(jobs.Items) != 1 {
					t.Fatalf("work Job missing after plan teardown: %+v %v", jobs, err)
				}
				binding, reservation, err := jobOwnership(jobs.Items[0])
				if err != nil || binding.Kind != CloudWork || reservation.NodeName != "fresh-1" || reservation.WaitedForFreshNode {
					t.Fatalf("work did not reuse the plan node: %+v %+v %v", binding, reservation, err)
				}
				if pods := capacityPods(t, kube); len(pods) != 0 || p.nodes != 1 {
					t.Fatalf("handoff left demand or excess nodes: pods=%d nodes=%d", len(pods), p.nodes)
				}
			})
		}
	}
}
