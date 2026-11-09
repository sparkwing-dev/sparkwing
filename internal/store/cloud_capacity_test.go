package store

import hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

var capacityFailures = []struct {
	reason, message string
	observe         func(capability.LeaseRef) capability.LauncherObservation
}{
	{capability.CloudCapacityUnconfigured, capability.CloudCapacityUnconfiguredMessage, func(fence capability.LeaseRef) capability.LauncherObservation {
		return capability.LauncherObservation{Award: fence, Reservation: true, CapacityUnconfigured: true}
	}},
	{capability.CloudCapacityUnavailable, capability.CloudCapacityUnavailableMessage, func(fence capability.LeaseRef) capability.LauncherObservation {
		return capability.LauncherObservation{Award: fence, Reservation: true, CapacityUnavailable: true}
	}},
}

func TestCloudCapacityFailureFailsAndRefundsOnce(t *testing.T) {
	for _, failure := range capacityFailures {
		for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
			t.Run(failure.reason+"/"+string(kind), func(t *testing.T) {
				testCapacityFailureRefund(t, kind, failure.reason, failure.message, failure.observe)
			})
		}
	}
}

func testCapacityFailureRefund(t *testing.T, kind capability.UnitKind, wantReason, wantMessage string, observe func(capability.LeaseRef) capability.LauncherObservation) {
	s, demand, claim, _ := launcherSnapshotFixture(t, kind)
	root, fence := launcherMeteredFence(t, s, demand, claim, kind)
	if chargedMicro(t, s) <= 0 {
		t.Fatal("award did not reserve credits")
	}
	observation := observe(fence)
	for range 2 {
		awards, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), root, []capability.LauncherObservation{observation}, []capability.LeaseRef{fence}, nil, nil, nil)
		if err != nil || len(awards) != 1 || !awards[0].Gone {
			t.Fatalf("capacity failure did not retire the award: %+v %v", awards, err)
		}
		unitBillingTotal(t, s, 0, true)
		rewriteSchemaCount(t, s.db, "billable_intervals", 0)
	}
	var message, reason string
	if kind == capability.UnitPlan {
		if err := s.DB().QueryRow(`SELECT error FROM runs WHERE id='run' AND status='failed'`).Scan(&message); err != nil {
			t.Fatal(err)
		}
		var payload []byte
		if err := s.DB().QueryRow(`SELECT payload FROM events WHERE run_id='run' AND kind='run_failed'`).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		var event map[string]string
		if err := json.Unmarshal(payload, &event); err != nil {
			t.Fatal(err)
		}
		reason = event["reason"]
	} else if err := s.DB().QueryRow(`SELECT error,failure_reason FROM nodes WHERE run_id='run' AND node_id='job'`).Scan(&message, &reason); err != nil {
		t.Fatal(err)
	}
	if reason != wantReason || !strings.Contains(message, wantMessage) || !strings.Contains(message, "Support reference: run") || !strings.Contains(message, "Requested class: sparkwing-2cpu") {
		t.Fatalf("customer failure: reason=%q message=%q", reason, message)
	}
}

func TestCloudCapacityNoticeEndsAtActivation(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			s, demand, claim, now := launcherSnapshotFixture(t, kind)
			root, fence := launcherMeteredFence(t, s, demand, claim, kind)
			*now = now.Add(30 * time.Second)
			tenant := &Tenant{s: s, team: "a"}
			view, err := tenant.ReadRunView(t.Context(), "run")
			if err != nil || view.CloudCapacitySince == nil || now.Sub(*view.CloudCapacitySince) != 30*time.Second {
				t.Fatalf("waiting capacity missing: %+v %v", view.CloudCapacitySince, err)
			}
			if err := s.sealLauncherHost(t.Context(), root, capability.LauncherActivation{Award: fence, NodeUID: "node", ReservationID: "reservation", Capacity: &capability.CloudCapacityGrant{RequestedClass: "sparkwing-2cpu", GrantedClass: "sparkwing-2cpu"}}); err != nil {
				t.Fatal(err)
			}
			view, err = tenant.ReadRunView(t.Context(), "run")
			if err != nil || view.CloudCapacitySince != nil {
				t.Fatalf("activated award still waiting: %+v %v", view.CloudCapacitySince, err)
			}
		})
	}
}

func TestCapacityFailureCannotFailAnActivatedAward(t *testing.T) {
	for _, failure := range capacityFailures {
		for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
			t.Run(failure.reason+"/"+string(kind), func(t *testing.T) {
				s, demand, claim, _ := launcherSnapshotFixture(t, kind)
				root, fence := launcherMeteredFence(t, s, demand, claim, kind)
				if err := s.sealLauncherHost(t.Context(), root, capability.LauncherActivation{Award: fence, NodeUID: "node", ReservationID: "reservation", Capacity: &capability.CloudCapacityGrant{RequestedClass: "sparkwing-2cpu", GrantedClass: "sparkwing-2cpu"}}); err != nil {
					t.Fatal(err)
				}
				reserved := chargedMicro(t, s)
				if _, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), root, []capability.LauncherObservation{failure.observe(fence)}, nil, nil, nil, nil); err == nil {
					t.Fatal("activated award accepted a capacity failure")
				}
				if got := chargedMicro(t, s); reserved <= 0 || got != reserved {
					t.Fatalf("activated award charge changed from %d to %d", reserved, got)
				}
				awards, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), root, nil, nil, nil, nil, nil)
				if err != nil || len(awards) != 1 || awards[0].Gone || awards[0].Claim.Award != claim.Award {
					t.Fatalf("capacity report changed activated award: %+v %v", awards, err)
				}
			})
		}
	}
}

func TestSelfHostedLauncherCannotReportCloudCapacityFailure(t *testing.T) {
	for _, failure := range capacityFailures {
		t.Run(failure.reason, func(t *testing.T) {
			s, demand := poolLauncherFixture(t)
			claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			root, fence := launcherMeteredFence(t, s, demand, claim, capability.UnitNode)
			_, _, err = s.AsOperator().ReconcileLauncherAwards(t.Context(), root, []capability.LauncherObservation{failure.observe(fence)}, nil, nil, nil, nil)
			var refusal *apierr.Error
			if !errors.As(err, &refusal) || refusal.Code != hosterr.Forbidden {
				t.Fatalf("self-hosted capacity report must be refused: %v", err)
			}
			awards, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), root, nil, nil, nil, nil, nil)
			if err != nil || len(awards) != 1 || awards[0].Gone || awards[0].Claim.Award != claim.Award {
				t.Fatalf("refused report changed self-hosted award: %+v %v", awards, err)
			}
			if got := chargedMicro(t, s); got != 0 {
				t.Fatalf("self-hosted award charged %d", got)
			}
		})
	}
}
