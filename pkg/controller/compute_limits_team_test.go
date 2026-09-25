package controller_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestComputeLimitsPaidCapacityShowsOnlyTheReadersTeam(t *testing.T) {
	f := newIdentityFixture(t)
	a := f.signIn(person("payer", "payer@example.test", "Payer"))
	b := f.signIn(person("neighbor", "neighbor@example.test", "Neighbor"))
	for name, value := range map[string]int64{
		store.ComputeLimitConcurrentRunners:      1,
		store.ComputeLimitRunnerScaleStepCredits: 5000,
	} {
		if code := f.call(http.MethodPut, "/api/v1/compute-limits", "Bearer "+f.admin,
			map[string]any{"limits": map[string]int64{name: value}}, nil); code != http.StatusOK {
			t.Fatalf("set %s: %d", name, code)
		}
	}
	team, err := f.store.ForTeam(context.Background(), store.Team(a.ActiveTeam.Slug))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := team.GrantCredits(context.Background(), store.CreditGrantPaid,
		5000*store.MicroCreditsPerCredit, "pay_a", "admin"); err != nil {
		t.Fatal(err)
	}
	view := func(session string) (int64, int64) {
		t.Helper()
		var out struct {
			Usage struct {
				Cap  int64 `json:"derived_runner_cap"`
				Paid int64 `json:"recent_paid_micro"`
			} `json:"usage"`
		}
		if code := f.call(http.MethodGet, "/api/v1/compute-limits", sessionAuth(session), nil, &out); code != http.StatusOK {
			t.Fatalf("compute limits: %d", code)
		}
		return out.Usage.Cap, out.Usage.Paid
	}
	if cap, paid := view(b.SessionID); cap != 1 || paid != 0 {
		t.Fatalf("neighbor reads cap %d and paid %d; want 1 and 0", cap, paid)
	}
	if cap, paid := view(a.SessionID); cap != 2 || paid != 5000*store.MicroCreditsPerCredit {
		t.Fatalf("payer reads cap %d and paid %d; want 2 and its own payment", cap, paid)
	}
}
