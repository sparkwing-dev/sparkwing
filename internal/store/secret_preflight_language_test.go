package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestSecretPreflightOptionalAdviceUsesDeclarationLanguage(t *testing.T) {
	for _, sdk := range []engineid.SDKKind{engineid.GoSDK, engineid.TypeScriptSDK, ""} {
		t.Run(string(sdk), func(t *testing.T) {
			tenant, request, _ := planFixtureWithInput(t, runspec.DispatchHost, func(input *IntakeRequest) { input.Request.Origin = runspec.OriginLocal })
			request.Seal.Resolved.Engine.Pin.SDK = sdk
			request.Seal.Resolved.SecretNames = []string{"ABSENT"}
			_, err := tenant.AcceptPlan(t.Context(), request)
			var refusal *secretPreflightError
			if !errors.As(err, &refusal) {
				t.Fatalf("required-secret refusal: %v", err)
			}
			events, err := tenant.ReadStateEvents(t.Context(), request.RunID, 0, 10)
			if err != nil || len(events) != 1 {
				t.Fatalf("refusal events: %+v %v", events, err)
			}
			var event struct{ Message string }
			if err := json.Unmarshal(events[0].Payload, &event); err != nil {
				t.Fatal(err)
			}
			want, other := `sw:"NAME,optional"`, `secrets: [{ name: "NAME", optional: true }]`
			if sdk == engineid.TypeScriptSDK {
				want, other = other, want
			}
			for _, message := range []string{refusal.Error(), event.Message} {
				if !strings.Contains(message, want) || (sdk != "" && strings.Contains(message, other)) || (sdk == "" && !strings.Contains(message, other)) {
					t.Fatalf("optional-secret syntax for %s: %s", sdk, message)
				}
			}
		})
	}
}
