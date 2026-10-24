package store

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestExecutionAttemptsStayInsideTheNodesTeamProperty(t *testing.T) {
	for i := range 24 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s, demand, _ := meteredUnitFixture(t, i&1 != 0)
			claim, actor := meteredUnitClaim(t, s, demand)
			tenant := &Tenant{s: s, team: claim.Team}
			ref := runspec.NodeRef{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID)}
			admitExecution(t, tenant, actor)
			started, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: ref, Actor: actor, To: runstate.NodeRunning, Cause: runstate.CauseStart, SourceAttempt: sourceAttempt(0)})
			if err != nil {
				t.Fatal(err)
			}
			own, err := tenant.listNodeExecutionAttempts(t.Context(), string(ref.RunID), string(ref.NodeID))
			if err != nil || len(own) != 1 || own[0].Attempt != int(started.Attempt) || own[0].ClaimGeneration != int64(claim.Award.Generation) || own[0].HolderID != claim.Award.Holder || own[0].FinishedAt != nil {
				t.Fatalf("started history=%+v, %v", own, err)
			}
			foreign := &Tenant{s: s, team: Team(fmt.Sprintf("tenant-'\";--%x", i))}
			if err := s.AsOperator().TestOnlyCreateTeam(t.Context(), foreign.team); err != nil {
				t.Fatal(err)
			}
			for _, target := range []runspec.NodeRef{ref, {RunID: ref.RunID + "' OR 1=1 --", NodeID: ref.NodeID}} {
				if got, err := foreign.listNodeExecutionAttempts(t.Context(), string(target.RunID), string(target.NodeID)); !errors.Is(err, ErrNotFound) || got != nil {
					t.Fatalf("foreign history=%+v, %v", got, err)
				}
			}
			if after, err := tenant.listNodeExecutionAttempts(t.Context(), string(ref.RunID), string(ref.NodeID)); err != nil || !reflect.DeepEqual(own, after) {
				t.Fatalf("refused reads changed history=%+v, %v", after, err)
			}
			if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: ref, Actor: actor, To: runstate.NodeSuccess, Cause: runstate.CauseReport, SourceAttempt: sourceAttempt(int64(started.Attempt))}); err != nil {
				t.Fatal(err)
			}
			finished, err := tenant.listNodeExecutionAttempts(t.Context(), string(ref.RunID), string(ref.NodeID))
			if err != nil || len(finished) != 1 || finished[0].FinishedAt == nil || finished[0].Outcome != "success" {
				t.Fatalf("finished history=%+v, %v", finished, err)
			}
			p, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix})
			if err != nil || finished[0].HolderID != p.Capability.Subject.ID {
				t.Fatalf("history lost enrolled identity: %+v, %v", finished, err)
			}
		})
	}
}
