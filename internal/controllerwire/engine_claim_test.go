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
