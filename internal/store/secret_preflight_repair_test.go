package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestRemoteSecretPreflightPointsToStatusRepair(t *testing.T) {
	tenant, request, _ := planFixture(t, runspec.DispatchHost)
	request.Seal.Resolved.SecretNames = []string{"ABSENT"}
	_, err := tenant.AcceptPlan(t.Context(), request)
	var refusal *secretPreflightError
	if !errors.As(err, &refusal) || refusal.local {
		t.Fatalf("remote required-secret refusal: %v", err)
	}
	if !strings.Contains(refusal.Error(), "sparkwing runs status --run "+request.RunID+" --profile <profile>") || strings.Contains(refusal.Error(), "GET /api/") {
		t.Fatalf("remote refusal points to a surface without its repair: %s", refusal.Error())
	}
	events, err := tenant.ReadStateEvents(t.Context(), request.RunID, 0, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("repair event: %+v %v", events, err)
	}
	var event struct{ Message string }
	if err := json.Unmarshal(events[0].Payload, &event); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(event.Message, "sparkwing secrets set --secret ABSENT --file <path> --profile <profile>") || !strings.Contains(event.Message, "--shared") {
		t.Fatalf("target event lacks the scoped repair: %s", event.Message)
	}
}
