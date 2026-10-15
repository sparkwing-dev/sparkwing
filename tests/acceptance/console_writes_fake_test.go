package acceptance_test

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
	tenant, err := f.store.ForTeam(ctx, store.Team(a.Target.Team))
	if err != nil {
		return store.BillingStanding{}, err
	}
	_, after, err := tenant.SetBillingTrust(ctx, req, time.Now())
	return after, err
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

func (f consoleStoreFake) ConsoleUnfreeze(ctx context.Context, a capability.Authorized, hold, reason string) (store.TeamCreditFreeze, error) {
	tenant, err := f.store.ForTeam(ctx, store.Team(a.Target.Team))
	if err != nil {
		return store.TeamCreditFreeze{}, err
	}
	if hold != "" {
		_, err = f.store.ReleaseCreditFreezes(ctx, tenant.Team(), hold, a.Principal.Capability.Subject.ID, reason, time.Now())
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
