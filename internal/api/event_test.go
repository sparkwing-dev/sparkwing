package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestPublicEventsRemoveClaimGenerationFromAttemptLifecycle(t *testing.T) {
	kinds := []string{"executor_selected", "execution_attempt_started", "execution_attempt_finished"}
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			event := store.Event{Kind: kind, Payload: json.RawMessage(`{"claim_generation":"private-generation-value","attempt":2}`)}
			projected := PublicEvents([]store.Event{event})
			body := string(projected[0].Payload)
			if strings.Contains(body, "claim_generation") || strings.Contains(body, "private-generation-value") {
				t.Fatalf("public event exposed generation: %s", body)
			}
			if !strings.Contains(body, `"attempt":2`) {
				t.Fatalf("public event omitted attempt: %s", body)
			}
			if !strings.Contains(string(event.Payload), "private-generation-value") {
				t.Fatal("projection mutated source event")
			}
		})
	}
}

func TestPublicEventsDropMalformedProtectedLifecyclePayload(t *testing.T) {
	event := store.Event{Kind: "execution_attempt_started", Payload: json.RawMessage(`{"claim_generation":"private-generation-value"`)}
	projected := PublicEvents([]store.Event{event})
	if projected[0].Payload != nil {
		t.Fatalf("malformed protected payload was returned: %s", projected[0].Payload)
	}
}
