package acceptance_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestHereResumeClaimContract(t *testing.T) {
	for _, endpoint := range []string{"http://api.sock", "https://controller.example"} {
		t.Run(endpoint, func(t *testing.T) {
			inv, err := clisurface.Parse("sparkwing", []string{"--profile", "prod", "pipeline", "trigger", "--resume", "here-run", "-o", "json"})
			if err != nil || inv.Pipeline != "" || len(inv.Args) != 0 || inv.Value("resume") != "here-run" {
				t.Fatalf("resume grammar: %#v %v", inv, err)
			}
			claimBy := time.Unix(1, 0)
			want := controllerwire.ClaimedUnit{
				Unit: capability.UnitTrigger, Purpose: capability.PurposeHere, Delivery: capability.DeliveryDirect,
				RunID: "here-run", UnitID: "trigger", Capability: "new-private-owner-bearer",
				Binding: capability.Binding{Kind: capability.BindingTrigger, RunID: "here-run", Generation: 5, Holder: "server-holder", ClaimBy: &claimBy},
				Request: runspec.SealedRequest{RunID: "original-sealed-run", Hash: "unchanged-request-hash"},
			}
			calls := 0
			c, err := client.NewEngineClient(client.EngineOptions{URL: endpoint, Bearer: func(context.Context) (string, error) { return "retained-here-bearer", nil }, HTTP: &http.Client{Transport: surfaceTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != http.MethodPost || req.URL.Path != "/api/v1/runs/here-run/claim" || req.URL.RawQuery != "" || req.Header.Get("Authorization") != "Bearer retained-here-bearer" {
					t.Fatalf("wrong here claim: %s %s", req.Method, req.URL)
				}
				var input controllerwire.ClaimRequest
				if err := json.NewDecoder(req.Body).Decode(&input); err != nil || input.RunID != "here-run" || input.JobID != "" || !reflect.DeepEqual(input.Kinds, []capability.UnitKind{capability.UnitTrigger}) {
					t.Fatalf("claim selection: %+v %v", input, err)
				}
				return engineContractResponse(t, controllerwire.ClaimResponse{Claim: &want}), nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.Claim(t.Context(), controllerwire.ClaimRequest{RunID: "here-run", Kinds: []capability.UnitKind{capability.UnitTrigger}})
			if err != nil || calls != 1 || !reflect.DeepEqual(got.Claim, &want) {
				t.Fatal("here reclaim changed the server's seal or fence")
			}
		})
	}
}

func TestHereClaimResponseLossIsNotRetried(t *testing.T) {
	calls := 0
	c, err := client.NewEngineClient(client.EngineOptions{URL: "https://controller.example", HTTP: &http.Client{Transport: surfaceTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"unavailable","message":"handoff lost"}}`))}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Claim(t.Context(), controllerwire.ClaimRequest{RunID: "here-run", Kinds: []capability.UnitKind{capability.UnitTrigger}})
	if err == nil || calls != 1 {
		t.Fatalf("claim calls=%d error=%v", calls, err)
	}
}
