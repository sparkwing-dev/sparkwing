package controller

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestBurstCancelledOwnerRenewalRetainsReportingAuthority(t *testing.T) {
	s, options, input, _ := sourceUploadFixture(t)
	s.WithEngineController(NewEngineController(s.store, EngineController{RunnerPolicy: runnerAdmissionFunc(func(context.Context, string) error { return nil })}))
	options.HTTP = &http.Client{Transport: inventoryRoundTrip{s.Handler()}}
	input.LocalSecretNames = []string{"TOKEN"}
	input.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerHere}}}
	input.Burst = &runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "here"}}}
	c, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"TOKEN": "private-requester-value"}
	created, err := c.CreateRun(t.Context(), controllerwire.CreateRunRequest{Request: input, LocalSecrets: values})
	if err != nil {
		t.Fatal(err)
	}
	options.Bearer = func(context.Context) (string, error) { return created.HereBearer, nil }
	here, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	response, err := here.Claim(t.Context(), controllerwire.ClaimRequest{RunID: created.RunID, Kinds: []capability.UnitKind{capability.UnitTrigger}, RunnerVersion: "v1", AvailableCores: 1})
	if err != nil || response.Claim == nil {
		t.Fatalf("here claim: %+v %v", response, err)
	}
	options.Bearer = func(context.Context) (string, error) { return response.Claim.Capability, nil }
	owner, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Cancel(t.Context(), created.RunID, controllerwire.CancelRequest{Reason: "interrupt"}); err != nil {
		t.Fatal(err)
	}
	available := &match.Resources{Cores: 1}
	for _, request := range []controllerwire.RenewalRequest{{}, {LocalSecrets: values}, {Available: available}, {LocalSecrets: values, Available: available}} {
		t.Run(fmt.Sprintf("secrets=%v/availability=%v", request.LocalSecrets != nil, request.Available != nil), func(t *testing.T) {
			beat, err := owner.Renew(t.Context(), response.Claim.UnitID, request)
			if err != nil || !beat.CancelRequested {
				t.Fatalf("cancelled owner lost reporting renewal (secrets=%v available=%v): %+v %v", request.LocalSecrets != nil, request.Available != nil, beat, err)
			}
		})
	}
	if _, err := owner.Finish(t.Context(), created.RunID, controllerwire.FinishRequest{}); err != nil {
		t.Fatalf("cancelled owner could not finish: %v", err)
	}
	detail, err := c.ReadRun(t.Context(), created.RunID)
	if err != nil || detail.Status != "cancelled" {
		t.Fatalf("cancelled run not terminal: %+v %v", detail, err)
	}
}
