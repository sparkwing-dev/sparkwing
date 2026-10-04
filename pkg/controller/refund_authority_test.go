package controller_test

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRefundReportingRejectsTeamOwnerAndRunner(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("refund-owner", "refund-owner@example.com")
	ctx := t.Context()
	tenant, err := f.store.ForTeam(ctx, store.Team(owner.team))
	if err != nil {
		t.Fatal(err)
	}
	runner, _, err := tenant.CreateToken(ctx, "private-runner", store.TokenKindRunner,
		[]string{controller.ScopeNodesClaim, controller.ScopeRunsState, controller.ScopeSecretsRead, controller.ScopeLogsWrite}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if code := f.call(http.MethodPost, "/api/v1/credits/grants", f.creditsGrantToken(), map[string]any{
		"kind": "paid", "amount_micro": 500 * store.MicroCreditsPerCent, "reference": "pi_private_refund", "team": owner.team,
	}, nil); code != http.StatusCreated {
		t.Fatalf("private payment grant=%d", code)
	}
	balance, err := tenant.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := tenant.ListCreditGrants(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, caller := range []struct {
		name, auth string
		want       int
	}{
		{"anonymous", "", http.StatusUnauthorized},
		{"team owner", owner.auth, http.StatusForbidden},
		{"team runner", "Bearer " + runner, http.StatusForbidden},
	} {
		t.Run(caller.name, func(t *testing.T) {
			for _, route := range []struct {
				path string
				body map[string]any
			}{
				{"/api/v1/credits/reversals", map[string]any{"payment_id": "pi_private_refund", "reference": "refund:pi_private_refund"}},
				{"/api/v1/credits/card-refunds", map[string]any{"payment_intent": "pi_private_refund", "queue": "pi_private_refund-0", "refund_id": "re_private", "status": "succeeded"}},
			} {
				if code := f.call(http.MethodPost, route.path, caller.auth, route.body, nil); code != caller.want {
					t.Errorf("%s status=%d want%d", route.path, code, caller.want)
				}
			}
		})
	}
	gotBalance, err := tenant.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gotGrants, err := tenant.ListCreditGrants(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if gotBalance != balance || !reflect.DeepEqual(gotGrants, grants) {
		t.Fatal("refused refund requests changed the private credit balance or ledger")
	}
}
