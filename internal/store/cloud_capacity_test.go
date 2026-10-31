package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestUnconfiguredCloudCapacityFailsAndRefundsOnce(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			s, demand, claim, _ := launcherSnapshotFixture(t, kind)
			root, fence := launcherMeteredFence(t, s, demand, claim, kind)
			if chargedMicro(t, s) <= 0 {
				t.Fatal("award did not reserve credits")
			}
			observation := capability.LauncherObservation{Award: fence, Reservation: true, CapacityUnconfigured: true}
			for range 2 {
				awards, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), root, []capability.LauncherObservation{observation}, []capability.LeaseRef{fence}, nil, nil, nil)
				if err != nil || len(awards) != 1 || !awards[0].Gone {
					t.Fatalf("configuration failure did not retire the award: %+v %v", awards, err)
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
			if reason != capability.CloudCapacityUnconfigured || !strings.Contains(message, capability.CloudCapacityUnconfiguredMessage) || !strings.Contains(message, "Support reference: run") {
				t.Fatalf("customer failure: reason=%q message=%q", reason, message)
			}
		})
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
			if err := s.sealLauncherHost(t.Context(), root, capability.LauncherActivation{Award: fence, NodeUID: "node", ReservationID: "reservation"}); err != nil {
				t.Fatal(err)
			}
			view, err = tenant.ReadRunView(t.Context(), "run")
			if err != nil || view.CloudCapacitySince != nil {
				t.Fatalf("activated award still waiting: %+v %v", view.CloudCapacitySince, err)
			}
		})
	}
}

func TestUnconfiguredCapacityCannotFailAnActivatedAward(t *testing.T) {
	s, demand, claim, _ := launcherSnapshotFixture(t, capability.UnitNode)
	root, fence := launcherMeteredFence(t, s, demand, claim, capability.UnitNode)
	if err := s.sealLauncherHost(t.Context(), root, capability.LauncherActivation{Award: fence, NodeUID: "node", ReservationID: "reservation"}); err != nil {
		t.Fatal(err)
	}
	observation := capability.LauncherObservation{Award: fence, Reservation: true, CapacityUnconfigured: true}
	if _, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), root, []capability.LauncherObservation{observation}, nil, nil, nil, nil); err == nil {
		t.Fatal("activated award accepted a missing-pool diagnosis")
	}
	if chargedMicro(t, s) <= 0 {
		t.Fatal("activated award refunded")
	}
}
