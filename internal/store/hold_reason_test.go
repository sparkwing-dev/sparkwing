package store_test

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestHoldReasonsSurvivePersistenceAsValidText(t *testing.T) {
	s := storetest.Open(t)
	ctx := t.Context()
	if err := s.AsOperator().TestOnlyCreateTeam(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	acme, err := s.ForTeam(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	operator := strings.Repeat("€", store.OperatorReasonLimit)
	id, err := acme.HoldByOperator(ctx, "korey", operator, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acme.TestOnlyHoldForDispute(ctx, "dp_euro", "pi_euro", strings.Repeat("€", 300), time.Now()); err != nil {
		t.Fatal(err)
	}
	for hold, want := range map[string]func(string) bool{
		id: func(r string) bool { return r == operator },
		"dp_euro": func(r string) bool {
			return utf8.ValidString(r) && len(r) <= 500 && strings.HasPrefix(strings.Repeat("€", 300), r) && r != ""
		},
	} {
		var reason string
		if err := s.DB().QueryRowContext(ctx, storetest.Rebind(s, `SELECT reason FROM credit_freezes WHERE dispute_id=?`), hold).Scan(&reason); err != nil {
			t.Fatal(err)
		}
		if !want(reason) {
			t.Fatalf("%s stored %d bytes, valid=%v", hold, len(reason), utf8.ValidString(reason))
		}
	}
}
