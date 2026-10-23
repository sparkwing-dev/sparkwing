package store

import (
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func TestConcurrentClassificationNeverShrinksTheIntakeSeal(t *testing.T) {
	tenant, request := sharedSettingsPlan(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job"}}})
	classified := request
	classified.Seal.Resolved.SecretArgs = []string{"message"}
	unclassified := request
	unclassified.Seal.Resolved.SecretArgs = []string{}
	start := make(chan struct{})
	type result struct {
		accepted AcceptedPlan
		err      error
	}
	results := make(chan result, 2)
	for _, in := range []PlanAcceptance{classified, unclassified} {
		go func() {
			<-start
			accepted, err := tenant.AcceptPlan(t.Context(), in)
			results <- result{accepted: accepted, err: err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent acceptance: %v, %v", first.err, second.err)
	}
	if first.accepted.Replayed == second.accepted.Replayed || first.accepted.Spec.Hash != second.accepted.Spec.Hash {
		t.Fatal("concurrent seals did not converge to one accepted spec")
	}
	run, err := tenant.ReadRun(t.Context(), request.RunID)
	if err != nil || run.Spec == nil {
		t.Fatalf("persisted spec: %v", err)
	}
	if !slices.Equal(run.Spec.Resolved.SecretArgs, []string{"message"}) || run.Request.Request.RequestedArgs["message"] != "hello" || runspec.DisplaySpec(*run.Spec).RequestedArgs["message"] != runspec.RedactedArgValue {
		t.Fatal("concurrent acceptance changed the request or exposed an intake secret")
	}
	rewriteSchemaCount(t, tenant.s.db, "nodes", 1)
}
