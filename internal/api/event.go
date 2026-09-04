package api

import (
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func PublicEvents(events []store.Event) []store.Event {
	if events == nil {
		return nil
	}
	out := make([]store.Event, len(events))
	for i, event := range events {
		out[i] = event
		switch event.Kind {
		case "executor_selected", "execution_attempt_started", "execution_attempt_finished":
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				out[i].Payload = nil
				continue
			}
			delete(payload, "claim_generation")
			out[i].Payload, _ = json.Marshal(payload)
		}
	}
	return out
}
