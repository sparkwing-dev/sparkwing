package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestLocalSecretPreflightGivesLocalSetCommand(t *testing.T) {
	tenant, request, _ := planFixtureWithInput(t, runspec.DispatchHost, func(input *IntakeRequest) {
		input.Request.Origin = runspec.OriginLocal
	})
	request.Seal.Resolved.SecretNames = []string{"BAR"}
	_, err := tenant.AcceptPlan(t.Context(), request)
	var refused *secretPreflightError
	if !errors.As(err, &refused) {
		t.Fatalf("missing required secret: %v", err)
	}
	events, err := tenant.ReadStateEvents(t.Context(), request.RunID, 0, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("secret diagnostic events: %+v, %v", events, err)
	}
	var diagnostic struct{ Message string }
	if err := json.Unmarshal(events[0].Payload, &diagnostic); err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{refused.Error(), diagnostic.Message} {
		if !strings.Contains(message, "sparkwing secrets set --secret BAR --file <path>") || strings.Contains(message, "--profile") || strings.Contains(message, "connected GitHub") {
			t.Errorf("local missing-secret guidance: %s", message)
		}
	}
}
