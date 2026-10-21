package store_test

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestAuthorizedRunDeletionPreservesBillingAndAllowsCleanupReplay(t *testing.T) {
	s := storetest.OpenSQLite(t)
	tenant, finish := runCompletionFixture(t, s)
	row, err := tenant.ReadRun(t.Context(), finish.Unit.RunID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.AsOperator().ResolveAuthority(t.Context(), row.Request.AuthorityRef)
	if err != nil {
		t.Fatal(err)
	}
	route, _ := capability.LookupRoute("DELETE", "/api/v1/runs/{run}")
	target := capability.Target{Team: string(tenant.Team()), RunID: row.ID}
	a, err := capability.Authorize(t.Context(), s.AsOperator(), route, capability.Authenticated{Principal: p, Method: route.Method, Pattern: route.Path, Live: []capability.LiveAccess{{RuleIndex: 0}}}, target)
	if err != nil {
		t.Fatal(err)
	}
	var conflict *apierr.Error
	if err := tenant.DeleteAuthorizedRun(t.Context(), a); !errors.As(err, &conflict) || conflict.Code != apierr.Conflict {
		t.Fatalf("active deletion: %v", err)
	}
	if _, err := tenant.FinishRunAndUnit(t.Context(), finish); err != nil {
		t.Fatal(err)
	}
	if err := tenant.DeleteAuthorizedRun(t.Context(), a); err == nil {
		t.Fatal("terminal run with an unsettled owner was deleted")
	}
	if _, err := tenant.FinishRunAndUnit(t.Context(), finish); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100, Reference: "deletion-fixture", CreatedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	var grants, awards, intervals int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM billing_awards`).Scan(&awards); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM billable_intervals`).Scan(&intervals); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM credit_grants`).Scan(&grants); err != nil || grants == 0 {
		t.Fatalf("missing financial facts: %v", err)
	}
	other := tenantFor(t, s, "other")
	if err := other.DeleteAuthorizedRun(t.Context(), a); err == nil {
		t.Fatal("foreign handle deleted run")
	}
	for range 2 {
		if err := tenant.DeleteAuthorizedRun(t.Context(), a); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tenant.ReadRun(t.Context(), row.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted run: %v", err)
	}
	var kept int
	for query, want := range map[string]int{`SELECT COUNT(*) FROM credit_grants`: grants, `SELECT COUNT(*) FROM billing_awards`: awards, `SELECT COUNT(*) FROM billable_intervals`: intervals} {
		if err := s.DB().QueryRow(query).Scan(&kept); err != nil || kept != want {
			t.Fatalf("billing rows=%d, want=%d: %v", kept, want, err)
		}
	}
	if err := tenant.RevokeAuthority(t.Context(), store.Actor{Authority: p.Authority}, p.Authority); err != nil {
		t.Fatal(err)
	}
	if err := tenant.DeleteAuthorizedRun(t.Context(), a); err == nil {
		t.Fatal("revoked authority retried cleanup")
	}
}
