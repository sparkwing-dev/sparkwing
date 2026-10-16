package store_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestBillingModelLaunchWaitlistsThe101stSignup(t *testing.T) {
	s := storetest.Open(t)
	setLimits(t, s, store.SignUpLimits{})
	now := time.Now()
	var last store.SignInResult
	for i := range 101 {
		last = signUpAt(t, s, googleProfile(fmt.Sprint(i), fmt.Sprintf("signup%d@example.test", i)), store.SignUpConditions{}, now)
		if i < 100 {
			requireAdmitted(t, last)
		}
	}
	if !last.Account.Waitlisted || last.Account.ActiveTeam != "" || last.WaitlistReason != "launch_capacity" {
		t.Fatalf("101st signup=%+v", last)
	}
	if _, err := s.ApproveWaitlisted(t.Context(), []string{last.Account.ID}, "operator", now); err != nil {
		t.Fatal(err)
	}
	last = signUpAt(t, s, googleProfile("100", "signup100@example.test"), store.SignUpConditions{}, now)
	requireAdmitted(t, last)
	tenant, err := s.ForTeam(t.Context(), last.Account.ActiveTeam)
	if err != nil {
		t.Fatal(err)
	}
	balance, err := tenant.CreditBalanceMicro(t.Context())
	if err != nil || balance != 0 {
		t.Fatalf("signup received free credit: %d: %v", balance, err)
	}
}
