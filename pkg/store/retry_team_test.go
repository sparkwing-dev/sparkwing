package store_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

type retrySourcePolicy struct {
	location runspec.SourceLocation
	delivery *store.VerifiedRunDelivery
}

func (p retrySourcePolicy) ResolveIntake(ctx context.Context, reader capability.AuthorityResolver, in store.IntakeRequest) (store.IntakePolicyFacts, error) {
	facts, err := (sealIntakePolicy{}).ResolveIntake(ctx, reader, in)
	facts.SourceLocation, facts.RepositoryAuthorized = &p.location, true
	if in.VerifiedDeliveryID != "" {
		facts.Delivery = p.delivery
	}
	return facts, err
}

func TestManualRetryPreservesOnlyItsTeamsSealedSourceProperty(t *testing.T) {
	s := storetest.Open(t)
	own, plan, err := storePlanSealRequest(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	foreign := tenant(t, s, signIn(t, s, "foreign", "foreign@example.test").PersonalTeam)
	random := rand.New(rand.NewPCG(281, 61))
	for i := range 12 {
		location := runspec.SourceLocation{RemoteURL: "https://github.com/acme/repo", PipelineDir: ".sparkwing"}
		s.SetIntakePolicy(retrySourcePolicy{location: location})
		request := plan.Seal.Request.Request
		request.Source = runspec.Source{Ref: fmt.Sprintf("%040x", random.Uint64()), RequestedRef: fmt.Sprintf("pipeline-%x", random.Uint64()), Entrypoint: "Fixture", Location: location}
		request.Git.SHA = fmt.Sprintf("%040x", random.Uint64())
		request.RequestedArgs = map[string]string{"message": fmt.Sprintf("secret-%x", random.Uint64())}
		request.SecretArgs = runspec.Optional[[]string]{Set: true, Value: []string{"message"}}
		source, err := own.AdmitRun(t.Context(), store.IntakeRequest{Authority: plan.Seal.Request.AuthorityRef, Request: request})
		if err != nil {
			t.Fatal(err)
		}
		input := store.IntakeRequest{Authority: plan.Seal.Request.AuthorityRef, IdempotencyKey: fmt.Sprint(i), Request: runspec.Request{Team: string(foreign.Team()), Pipeline: request.Pipeline, Lineage: runspec.Lineage{RetryOf: source.RunID}, Source: runspec.Source{Ref: "moved-branch"}, Git: runspec.Git{SHA: "new-head"}, RequestedArgs: map[string]string{"message": "replacement"}}}
		if _, err := foreign.AdmitRun(t.Context(), input); err == nil {
			t.Fatal("foreign tenant retried another team's source")
		}
		if _, err := own.AdmitRun(t.Context(), input); err == nil {
			t.Fatal("retry accepted a forged team selector")
		}
		input.Request.Team = string(own.Team())
		retry, err := own.AdmitRun(t.Context(), input)
		if err != nil {
			t.Fatal(err)
		}
		row, err := own.ReadRun(t.Context(), retry.RunID)
		if err != nil || row.Team != own.Team() || !reflect.DeepEqual(row.Request, source.Request) || row.Lineage.RetryOf != source.RunID || !row.Lineage.Retrier.Set {
			t.Fatalf("retry changed the sealed source: %+v, %v", row, err)
		}
		if !row.Request.Request.SecretArgs.Set || !reflect.DeepEqual(row.Request.Request.SecretArgs.Value, []string{"message"}) || runspec.DisplayRequest(row.Request).RequestedArgs["message"] == request.RequestedArgs["message"] {
			t.Fatal("retry lost its secret argument classification or exposed its value")
		}
		if _, err := foreign.ReadRun(t.Context(), retry.RunID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("foreign retry read: %v", err)
		}
		if i == 0 {
			owner, err := s.ResolveAuthority(t.Context(), plan.Actor.Authority)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := s.ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: owner.Capability.Parent.ID, Target: &store.UnitRef{Kind: capability.UnitTrigger, RunID: retry.RunID}, Available: match.Resources{Cores: 8}})
			if err != nil || claim.Team != own.Team() {
				t.Fatalf("own retry claim: %+v, %v", claim, err)
			}
		}
	}
}

func TestManualRetryRefusesMissingSource(t *testing.T) {
	s := storetest.Open(t)
	own, plan, err := storePlanSealRequest(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	_, err = own.AdmitRun(t.Context(), store.IntakeRequest{Authority: plan.Seal.Request.AuthorityRef, Request: runspec.Request{Lineage: runspec.Lineage{RetryOf: "missing"}}})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing source: %v", err)
	}
}

func TestManualRetryRetainsVerifiedRepositoryFacts(t *testing.T) {
	s := storetest.Open(t)
	own, plan, err := storePlanSealRequest(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	request := plan.Seal.Request.Request
	git := request.Git
	git.Ref, git.SHA = "refs/heads/main", "0123456789abcdef0123456789abcdef01234567"
	git.App = runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{DeliveryKey: "verified-delivery", RepositoryID: 42, Ref: git.Ref, SHA: git.SHA, DefaultBranch: "main", DefaultBranchPush: true, VerifiedAt: plan.Seal.Request.AdmittedAt}}
	s.SetIntakePolicy(retrySourcePolicy{delivery: &store.VerifiedRunDelivery{Git: git, Actor: runspec.Actor{Kind: "github", ID: "app-actor"}}})
	source, err := own.AdmitRun(t.Context(), store.IntakeRequest{Authority: plan.Seal.Request.AuthorityRef, Request: request, VerifiedDeliveryID: "verified-delivery"})
	if err != nil {
		t.Fatal(err)
	}
	request.Lineage.RetryOf, request.Git.SHA = source.RunID, "later-head"
	request.Git.App = runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{RepositoryID: 999}}
	retry, err := own.AdmitRun(t.Context(), store.IntakeRequest{Authority: plan.Seal.Request.AuthorityRef, Request: request})
	if err != nil || !reflect.DeepEqual(retry.Request, source.Request) {
		t.Fatalf("retry changed verified facts: %+v, %v", retry.Request, err)
	}
	var delivery string
	if err := s.DB().QueryRowContext(t.Context(), storetest.Rebind(s, `SELECT webhook_delivery FROM triggers WHERE team=? AND id=?`), own.Team(), retry.RunID).Scan(&delivery); err != nil || delivery != "" {
		t.Fatalf("retry replayed a webhook: %q, %v", delivery, err)
	}
}
