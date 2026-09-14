package controller_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type grantWire struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	AmountMicro int64  `json:"amount_micro"`
	Reference   string `json:"reference"`
	Reverses    string `json:"reverses"`
}

type stateWire struct {
	BalanceMicro  int64 `json:"balance_micro"`
	GrantedMicro  int64 `json:"granted_micro"`
	ReversedMicro int64 `json:"reversed_micro"`
}

type historyWire struct {
	Grants []grantWire `json:"grants"`
}

func postGrant(t *testing.T, f creditsFixture, body map[string]any) (int, grantWire) {
	t.Helper()
	status, raw := creditsRequest(t, http.MethodPost, f.url+"/api/v1/credits/grants", f.admin, body)
	var out grantWire
	if status == http.StatusOK || status == http.StatusCreated {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode grant: %v", err)
		}
	}
	return status, out
}

// A payment webhook redelivers until it sees a 2xx, so the route answers the
// second delivery with the grant it already wrote.
func TestCreditsGrantRouteIsIdempotentByReference(t *testing.T) {
	f := newCreditsFixture(t, false)
	body := map[string]any{
		"kind":         store.CreditGrantPaid,
		"amount_micro": 1000 * store.MicroCreditsPerCredit,
		"reference":    "pi_route",
	}

	status, first := postGrant(t, f, body)
	if status != http.StatusCreated {
		t.Fatalf("first grant status = %d, want 201", status)
	}
	status, again := postGrant(t, f, body)
	if status != http.StatusOK {
		t.Fatalf("redelivered grant status = %d, want 200", status)
	}
	if again.ID != first.ID {
		t.Fatalf("redelivery returned %q, want the first grant %q", again.ID, first.ID)
	}

	_, raw := creditsRequest(t, http.MethodGet, f.url+"/api/v1/credits/history", f.admin, nil)
	var history historyWire
	if err := json.Unmarshal(raw, &history); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(history.Grants) != 1 {
		t.Fatalf("history grants = %d, want the one row", len(history.Grants))
	}
}

func TestCreditsGrantRouteReversesAPaidGrant(t *testing.T) {
	f := newCreditsFixture(t, false)
	if status, _ := postGrant(t, f, map[string]any{
		"kind":         store.CreditGrantPaid,
		"amount_micro": 1000 * store.MicroCreditsPerCredit,
		"reference":    "pi_refunded",
	}); status != http.StatusCreated {
		t.Fatalf("paid grant status = %d, want 201", status)
	}
	status, reversal := postGrant(t, f, map[string]any{
		"kind":         store.CreditGrantReversal,
		"amount_micro": -400 * store.MicroCreditsPerCredit,
		"reference":    "re_refunded",
		"reverses":     "pi_refunded",
	})
	if status != http.StatusCreated {
		t.Fatalf("reversal status = %d, want 201", status)
	}
	if reversal.Reverses != "pi_refunded" {
		t.Fatalf("reversal row = %+v, want it to name the payment", reversal)
	}

	_, raw := creditsRequest(t, http.MethodGet, f.url+"/api/v1/credits", f.admin, nil)
	var state stateWire
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if want := int64(600 * store.MicroCreditsPerCredit); state.BalanceMicro != want {
		t.Errorf("balance = %d, want %d", state.BalanceMicro, want)
	}
	if want := int64(400 * store.MicroCreditsPerCredit); state.ReversedMicro != want {
		t.Errorf("reversed = %d, want %d", state.ReversedMicro, want)
	}
	if want := int64(1000 * store.MicroCreditsPerCredit); state.GrantedMicro != want {
		t.Errorf("granted = %d, want the payment %d", state.GrantedMicro, want)
	}
}

func TestCreditsGrantRouteRefusesAnUnbackedOrPositiveReversal(t *testing.T) {
	f := newCreditsFixture(t, false)
	if status, _ := postGrant(t, f, map[string]any{
		"kind":         store.CreditGrantPaid,
		"amount_micro": 100 * store.MicroCreditsPerCredit,
		"reference":    "pi_known",
	}); status != http.StatusCreated {
		t.Fatalf("paid grant status = %d, want 201", status)
	}
	for name, body := range map[string]map[string]any{
		"a reversal of a payment the ledger never saw": {
			"kind": store.CreditGrantReversal, "amount_micro": -5,
			"reference": "re_x", "reverses": "pi_unknown",
		},
		"a reversal with a positive amount": {
			"kind": store.CreditGrantReversal, "amount_micro": 5,
			"reference": "re_y", "reverses": "pi_known",
		},
		"a reversal that names no payment": {
			"kind": store.CreditGrantReversal, "amount_micro": -5, "reference": "re_z",
		},
	} {
		if status, _ := postGrant(t, f, body); status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, status)
		}
	}
}
