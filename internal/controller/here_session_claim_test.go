package controller

import (
	"context"
	"crypto/sha256"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestAHereAwardRootedInAnAccountSessionClaimsItsTrigger(t *testing.T) {
	s, options, input, _ := sourceUploadFixture(t)
	s.WithEngineController(NewEngineController(s.store, EngineController{RunnerPolicy: runnerAdmissionFunc(func(context.Context, string) error { return nil })}))
	options.HTTP = &http.Client{Transport: inventoryRoundTrip{s.Handler()}}
	bearer, err := options.Bearer(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	member, err := s.store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(bearer)))
	if err != nil || member.Capability.Parent == nil || member.Capability.Parent.Kind != capability.AuthoritySession {
		t.Fatalf("the submitting member token is not rooted in an account session: %+v %v", member.Capability.Parent, err)
	}
	input.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerHere}}}
	c, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	created, err := c.CreateRun(t.Context(), controllerwire.CreateRunRequest{Request: input, IdempotencyKey: "session-here"})
	if err != nil || created.HereBearer == "" {
		t.Fatalf("here intake: %+v %v", created, err)
	}
	if award := hereClaim(t, options, created.HereBearer, created.RunID); award == "" {
		t.Fatal("a here award rooted in an account session could not claim its own trigger")
	}
}
