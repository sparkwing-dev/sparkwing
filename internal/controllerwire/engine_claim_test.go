package controllerwire

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClaimRequestPreservesBusyAndUnspecifiedSlots(t *testing.T) {
	zero := 0
	for _, test := range []struct {
		name    string
		slots   *int
		present bool
	}{
		{"unspecified", nil, false}, {"busy", &zero, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(ClaimRequest{FreeSlots: test.slots})
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(raw), `"free_slots":0`); got != test.present {
				t.Fatalf("free slot presence: %s", raw)
			}
			var request ClaimRequest
			if err := json.Unmarshal(raw, &request); err != nil {
				t.Fatal(err)
			}
			if (request.FreeSlots != nil) != test.present {
				t.Fatalf("lost slot presence: %+v", request)
			}
		})
	}
}

func TestClaimConcurrencyObservationPreservesLegacyBody(t *testing.T) {
	body, err := json.Marshal(ClaimRequest{MaxConcurrent: 2, RunnerVersion: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	var legacy struct {
		RunnerVersion string   `json:"runner_version"`
		Kinds         []string `json:"kinds"`
		Labels        []string `json:"labels"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&legacy); err != nil {
		t.Fatalf("inventory observation broke legacy claims: %s: %v", body, err)
	}
}
