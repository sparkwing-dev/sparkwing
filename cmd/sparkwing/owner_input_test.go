package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestOwnerAwardMismatchCannotReachBindingProperty(t *testing.T) {
	property := func(value string, mutation uint8, here bool) bool {
		award := localTestAward(t)
		if here {
			award = hereAward(t)
		}
		plan := json.RawMessage(`{"jobs":[]}`)
		spec, err := runspec.SealSpec(runspec.SealInput{
			Request: award.Request, Plan: plan,
			Resolved: runspec.Resolved{SecretArgs: []string{"password"}, Args: award.Request.Request.RequestedArgs},
		})
		if err != nil {
			t.Fatal(err)
		}
		award.Accepted = &controllerwire.PlanAccepted{Spec: spec, Plan: plan, PlanHash: spec.PlanHash}
		run, hash := award.RunID, award.Request.Hash
		switch mutation % 10 {
		case 0:
			award.RunID += value + "-foreign"
		case 1:
			award.Binding.RunID += value + "-foreign"
		case 2:
			award.JobID = value + "-foreign"
		case 3:
			award.Binding.NodeID = value + "-foreign"
		case 4:
			award.Request.Hash += runspec.Hash(value + "-foreign")
		case 5:
			award.Request.Request.RequestedArgs["password"] += value + "-foreign"
		case 6:
			award.Accepted.Spec.Hash += runspec.Hash(value + "-foreign")
		case 7:
			award.Accepted.Plan = json.RawMessage(`{"jobs":[],"changed":true}`)
		case 8:
			award.LeaseExpiresAt = time.Time{}
		case 9:
			award.Binding.Generation = 0
		}
		bound := false
		bind := func(context.Context, controllerwire.ClaimedUnit) (hereHostSession, error) {
			bound = true
			return hereHostSession{}, errors.New("unexpected binding")
		}
		if here {
			_, err = runHereAward(t.Context(), client.EngineOptions{}, run, award,
				func(ctx context.Context, _ client.EngineOptions, unit controllerwire.ClaimedUnit) (hereHostSession, error) {
					return bind(ctx, unit)
				})
		} else {
			runtime := localRunRuntime{bind: bind, controller: localTestController{claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				return controllerwire.ClaimResponse{Claim: &award}, nil
			}}}
			_, err = hostLocalRun(t.Context(), runtime, run, hash, "v1.2.3")
		}
		var failure *apierr.Error
		return !bound && errors.As(err, &failure) && failure.Code == apierr.ProtocolError
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 256}); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerInputMatchesPersistedTriggerFence(t *testing.T) {
	award := localTestAward(t)
	state, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Error(err)
		}
	})
	request, err := json.Marshal(award.Request)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO teams(name,created_at,updated_at) VALUES ('team',1,1)`, nil},
		{`INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days) VALUES ('team',?,1,'ci','running',?,?,1,?,'host','execute','account',1,'r30',30)`, []any{award.RunID, request, string(award.Request.Hash), string(award.Request.Request.Git.Repository.Key)}},
		{`INSERT INTO triggers(team,id,unit_kind,available_at,created_at,needs_labels,prefers_labels) VALUES ('team',?,'trigger',1,1,?,?)`, []any{award.RunID, []byte(`[]`), []byte(`[]`)}},
	} {
		if _, err := teststore.SQL(t, state).ExecContext(t.Context(), statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := teststore.SeedTriggerClaim(state, t.Context(), "team", award.RunID, runstate.Actor{CapabilityPrefix: award.Capability, NodeFence: runspec.NodeFence{RunID: runspec.RunID(award.RunID), Holder: award.Binding.Holder, Generation: runspec.ClaimGeneration(award.Binding.Generation)}}, award.LeaseExpiresAt, nil); err != nil {
		t.Fatal(err)
	}
	tenant := teststore.ForTeam(t, state, "team")
	snapshot, err := tenant.ReadSnapshot(t.Context(), award.RunID)
	if err != nil {
		t.Fatal(err)
	}
	input, err := localRunInput(award.RunID, award.Request.Hash, award)
	if err != nil || snapshot.Run.Owner == nil || input.Owner.Owner != *snapshot.Run.Owner {
		t.Fatalf("projected owner=%+v persisted owner=%+v error=%v", input.Owner.Owner, snapshot.Run.Owner, err)
	}
}
