package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestChildrenSpendTheirRootsFloodBudget(t *testing.T) {
	for _, app := range []bool{false, true} {
		for _, queue := range []bool{false, true} {
			t.Run(fmt.Sprintf("App=%v/queue=%v", app, queue), func(t *testing.T) {
				e, _, claim := childIntakeFixture(t)
				tenant, err := e.Store.ForTeam(t.Context(), "a")
				if err != nil {
					t.Fatal(err)
				}
				root, err := tenant.ReadRun(t.Context(), "run")
				if err != nil {
					t.Fatal(err)
				}
				root.Request.AuthorityRef = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "root"}
				if app {
					root.Request.Request.Origin = runspec.OriginApp
				}
				body, err := json.Marshal(root.Request)
				if err != nil {
					t.Fatal(err)
				}
				engineSQL(t, e.Store, `UPDATE runs SET request_json=? WHERE team='a' AND id='run'`, body)
				s := New(e.Store, nil).WithEngineController(e).WithFloodPolicy(FloodPolicy{RunsPerPrincipalHour: 1})
				// Spend the root's token through the same accounting used by root admission.
				principal := capability.Principal{Authority: root.Request.AuthorityRef, Capability: capability.Capability{Team: "a"}}
				intake := store.IntakeRequest{Authority: principal.Authority}
				if app {
					intake.VerifiedDeliveryID = "verified-root"
				}
				if _, err := admitWithFlood(t.Context(), s, func(ctx context.Context) (bool, error) {
					return true, s.admissionFloodRefusal(ctx, nil, intake, principal)
				}); err != nil {
					t.Fatal(err)
				}
				want := http.StatusTooManyRequests
				if queue {
					s.WithFloodPolicy(FloodPolicy{ShedQueueDepth: 1})
					engineSQL(t, e.Store, `UPDATE triggers SET status='pending' WHERE team='a' AND id='run'`)
					want = http.StatusServiceUnavailable
				}
				engineClaimHTTP(t, s.Handler(), claim, "POST", "/api/v1/runs/run/children", childIntakeBody(t, "deploy", root.Request.Request.Git), want)
				var count int
				if err := teststore.SQL(t, e.Store).QueryRow(`SELECT COUNT(*) FROM runs WHERE team='a'`).Scan(&count); err != nil || count != 1 {
					t.Fatalf("refused child wrote a run: %d %v", count, err)
				}
			})
		}
	}
}
