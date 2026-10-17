package controller_test

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"

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
	runner, _, err := teststore.ProvisionCredential(ctx, f.store, capability.ProvisionSpec{Kind: capability.ProvisionMachine, Repositories: []string{"github.com/*/*"}, Team: string(tenant.Team()), Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "private-runner"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, ExpiresAt: teststore.CredentialExpiry(time.Now(), 0), RegistrationID: "runner:" + string(tenant.Team()) + ":" + "private-runner"})
	if err != nil {
		t.Fatal(err)
	}
	controller.PaidCreditsFixture(t, f.store, tenant, 500)
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
				path    string
				body    map[string]any
				retired bool
			}{
				{"/api/v1/credits/reversals", map[string]any{"payment_id": "pi_fixture", "reference": "refund:pi_fixture"}, true},
				{"/api/v1/credits/card-refunds", map[string]any{"payment_intent": "pi_fixture", "queue": "pi_fixture-0", "refund_id": "re_private", "status": "succeeded"}, true},
				{"/api/v1/operator/teams/" + owner.team + "/refunds", map[string]any{"payment_id": "pi_fixture", "refund_id": "re_private", "amount_cents": 500, "reason": "test"}, false},
			} {
				want := caller.want
				if route.retired {
					want = http.StatusNotFound
				}
				if code := f.call(http.MethodPost, route.path, caller.auth, route.body, nil); code != want {
					t.Errorf("%s status=%d want%d", route.path, code, want)
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
