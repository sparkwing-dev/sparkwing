package acceptance_test

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

type consoleStoreFake struct {
	controller.ConsoleWriter
	store *store.Store
}

func (f consoleStoreFake) ConsoleGrantCredit(ctx context.Context, a capability.Authorized, req store.CreditGrantRequest) (store.CreditGrantResult, error) {
	tenant, err := f.store.ForTeam(ctx, store.Team(a.Target.Team))
	if err != nil {
		return store.CreditGrantResult{}, err
	}
	return tenant.RecordCreditGrant(ctx, req)
}

func (f consoleStoreFake) ConsoleSetBillingTrust(ctx context.Context, a capability.Authorized, req store.BillingTrustChange) (store.BillingStanding, error) {
	actor := a.Principal.Capability.Subject.ID
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE teams SET billing_trust=?,billing_trust_by=?,billing_trust_at=?,billing_trust_reason=? WHERE name=?`, []any{req.Trust, actor, time.Now().UnixNano(), req.Reason, a.Target.Team}},
		{`INSERT INTO business_events(id,ts,team,kind,actor,attrs) VALUES (?,?,?,?,?,?)`, []any{"fixture-trust-" + a.Target.Team, time.Now().UnixNano(), a.Target.Team, store.BusinessEventBillingTrustChanged, actor, `{"reason":"` + req.Reason + `"}`}},
	} {
		if _, err := teststore.Exec(ctx, f.store, stmt.sql, stmt.args...); err != nil {
			return store.BillingStanding{}, err
		}
	}
	tenant, err := f.store.ForTeam(ctx, store.Team(a.Target.Team))
	if err != nil {
		return store.BillingStanding{}, err
	}
	return tenant.BillingStanding(ctx, time.Now())
}

func (f consoleStoreFake) ConsoleFreeze(ctx context.Context, a capability.Authorized, reason string) (store.TeamCreditFreeze, error) {
	tenant, err := f.store.ForTeam(ctx, store.Team(a.Target.Team))
	if err != nil {
		return store.TeamCreditFreeze{}, err
	}
	if _, err := tenant.HoldByOperator(ctx, a.Principal.Capability.Subject.ID, reason, time.Now()); err != nil {
		return store.TeamCreditFreeze{}, err
	}
	return tenant.CreditFreeze(ctx)
}

func (f consoleStoreFake) ConsoleUnfreeze(ctx context.Context, a capability.Authorized, hold, reason string, _ bool) (store.TeamCreditFreeze, error) {
	tenant, err := f.store.ForTeam(ctx, store.Team(a.Target.Team))
	if err != nil {
		return store.TeamCreditFreeze{}, err
	}
	if hold != "" {
		_, err = tenant.ReleaseCreditFreezes(ctx, hold, a.Principal.Capability.Subject.ID, reason, time.Now())
	} else {
		_, err = tenant.ReleaseOperatorHolds(ctx, a.Principal.Capability.Subject.ID, reason, time.Now())
	}
	if err != nil {
		return store.TeamCreditFreeze{}, err
	}
	return tenant.CreditFreeze(ctx)
}

func (f consoleStoreFake) ConsoleDeleteAccount(ctx context.Context, a capability.Authorized) (store.AccountDeletion, error) {
	return f.store.DeleteAccount(ctx, a.Target.AccountID, time.Now())
}
