package store

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestCapabilityClaimLivenessProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(419, 122))
	for i := range 32 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			authenticate := func(raw, method, path string) bool {
				t.Helper()
				for _, route := range capability.Routes() {
					if route.Method != method || route.Path != path {
						continue
					}
					target := capability.Target{Team: "a", RunID: "run", NodeID: "job"}
					auth, err := capability.NewAuthenticator(s.AsOperator()).Authenticate(t.Context(), capability.Presentation{Class: capability.CredentialBearer, Raw: raw, Evidence: capability.RequestEvidence{Target: target}}, route)
					if err != nil {
						return false
					}
					_, err = capability.Authorize(t.Context(), s.AsOperator(), route, auth, target)
					return err == nil
				}
				t.Fatal("missing route policy")
				return false
			}
			sensitive := func(raw string) bool { return authenticate(raw, "GET", "/api/v1/runs/{run}/jobs/{job}/metrics") }
			reporting := func(raw string) bool { return authenticate(raw, "POST", "/api/v1/runs/{run}/jobs/{job}/heartbeat") }
			result := func(raw string) bool { return authenticate(raw, "POST", "/api/v1/runs/{run}/jobs/{job}/transition") }
			raw := string(claim.Bearer)
			if !sensitive(raw) || !reporting(raw) || !result(raw) {
				t.Fatal("live award refused")
			}
			corrupt := []byte(raw)
			j := 4 + random.IntN(len(corrupt)-4)
			corrupt[j] ^= 1
			if sensitive(string(corrupt)) || reporting(string(corrupt)) || result(string(corrupt)) {
				t.Fatal("tampered bearer authenticated")
			}
			rewriteSchemaExec(t, s.db, `UPDATE runs SET cancel_requested_at=? WHERE id='run'`, s.now().UnixNano())
			if sensitive(raw) || !reporting(raw) || !result(raw) {
				t.Fatal("cancellation changed holder result/reporting rights")
			}
			switch i % 5 {
			case 0:
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET generation=generation+? WHERE run_id='run'`, 1+random.IntN(100))
			case 1:
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET claimed_by=? WHERE run_id='run'`, fmt.Sprint("foreign-", random.Uint64()))
			case 2:
				rewriteSchemaExec(t, s.db, `UPDATE nodes SET lease_expires_at=? WHERE run_id='run'`, s.now().Add(-time.Duration(random.IntN(1000))*time.Second).UnixNano())
			case 4:
				rewriteSchemaExec(t, s.db, `DELETE FROM runs WHERE id='run'`)
			case 3:
				revokeClaimRoot(t, s, "runner", "a")
			}
			if sensitive(raw) || reporting(raw) || result(raw) {
				t.Fatal("ended, superseded or revoked authority authenticated")
			}
		})
	}
}

func TestCapabilityResultCannotChangeAfterCompletionOrLossProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(442, 122))
	for i := range 24 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			tenant := &Tenant{s: s, team: "a"}
			actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
			change := nodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)}
			if i%2 == 0 {
				if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
					t.Fatal(err)
				}
			} else {
				started, err := tenant.TransitionNode(t.Context(), change)
				if err != nil {
					t.Fatal(err)
				}
				change.To, change.Cause, change.SourceAttempt = NodeSuccess, CauseReport, sourceAttempt(int64(started.Attempt))
				if _, err := tenant.TransitionNode(t.Context(), change); err != nil {
					t.Fatal(err)
				}
				applied, err := tenant.TransitionNode(t.Context(), change)
				if err != nil || !applied.Replayed {
					t.Fatalf("exact terminal replay: %+v %v", applied, err)
				}
			}
			before, err := tenant.ReadNode(t.Context(), change.Ref)
			if err != nil {
				t.Fatal(err)
			}
			var count int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			for range 4 {
				refused := change
				refused.To, refused.Cause = NodeFailed, CauseReport
				refused.Extra.Result = &NodeResult{MaskedError: fmt.Sprint(random.Uint64())}
				if _, err := tenant.TransitionNode(t.Context(), refused); err == nil {
					t.Fatal("changed or lost result committed")
				}
			}
			after, err := tenant.ReadNode(t.Context(), change.Ref)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("refusal changed node: %v", err)
			}
			var afterCount int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&afterCount); err != nil || afterCount != count {
				t.Fatalf("refusal wrote an event: %d %d %v", count, afterCount, err)
			}
		})
	}
}
