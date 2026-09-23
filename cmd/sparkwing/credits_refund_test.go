package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A refund takes the purchase back on the ledger once, and the operator is
// sent to Stripe to move the money.
func TestCreditsRefundReversesOnceAndPointsAtStripe(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	admin, _, err := st.CreateToken("root", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	defer srv.Close()
	if _, err := tokensPost(srv.URL, admin, "/api/v1/credits/grants", map[string]any{
		"kind": store.CreditGrantPaid, "amount_micro": int64(1000) * store.MicroCreditsPerCent, "reference": "pi_42",
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}

	var outputs []string
	for range 2 {
		raw, err := tokensPost(srv.URL, admin, "/api/v1/credits/reversals",
			map[string]any{"payment_id": "pi_42", "reference": refundReference("pi_42")})
		if err != nil {
			t.Fatalf("refund: %v", err)
		}
		var resp paymentReversalResp
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := renderRefund(&out, resp); err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, out.String())
	}
	if !strings.Contains(outputs[0], "reversed 200000.00 credits of pi_42 from team default") ||
		!strings.Contains(outputs[0], "https://dashboard.stripe.com/test/payments/pi_42") ||
		!strings.Contains(outputs[0], "https://dashboard.stripe.com/payments/pi_42") {
		t.Errorf("first refund output: %q", outputs[0])
	}
	if !strings.Contains(outputs[1], "already refunded") {
		t.Errorf("second refund output:\n%s", outputs[1])
	}
	if got, err := st.CreditBalanceMicro(t.Context()); err != nil || got != 0 {
		t.Errorf("balance = %d, %v; want the purchase taken back once", got, err)
	}
}

func TestCreditFreezeBodyNamesOneTeamAndAReason(t *testing.T) {
	if _, err := creditFreezeBody("", "", "x", false); err == nil {
		t.Error("neither --team nor --payment was accepted")
	}
	if _, err := creditFreezeBody("acme", "pi_1", "x", false); err == nil {
		t.Error("both --team and --payment were accepted")
	}
	if _, err := creditFreezeBody("acme", "", "", false); err == nil {
		t.Error("a hold without --reason was accepted")
	}
	body, err := creditFreezeBody("", "pi_1", "", true)
	if err != nil || body["payment_id"] != "pi_1" || body["frozen"] != false {
		t.Errorf("release by payment = %v, %v", body, err)
	}
}
