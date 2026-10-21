package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// consoleWriteTransactions rechecks a direct operator session in every committing transaction.
type consoleWriteTransactions interface {
	ConsoleRefund(context.Context, capability.Authorized, controllerwire.OperatorRefund, string) (store.CreditReversal, error)
	ConsoleGrantCredit(context.Context, capability.Authorized, store.CreditGrantRequest) (store.CreditGrantResult, error)
	ConsoleSetBillingTrust(context.Context, capability.Authorized, store.BillingTrustChange) (store.BillingStanding, error)
	ConsoleDeclineStandingRequest(context.Context, capability.Authorized, string, string) (store.StandingRequest, error)
	ConsoleFreeze(context.Context, capability.Authorized, string) (store.TeamCreditFreeze, error)
	ConsoleUnfreeze(context.Context, capability.Authorized, string, string, bool) (store.TeamCreditFreeze, error)
	ConsoleDeleteAccount(context.Context, capability.Authorized) (store.AccountDeletion, error)
	ConsoleDeleteTeam(context.Context, capability.Authorized) (store.TeamDeletion, error)
	ConsoleApproveWaitlist(context.Context, capability.Authorized, []string, int, string) ([]store.Account, error)
	ConsoleSetCreditSettings(context.Context, capability.Authorized, store.CreditSettingsUpdate) (store.CreditSettings, error)
	ConsoleUnbindGitHubInstallation(context.Context, capability.Authorized, int64) (store.Team, error)
}

func (s *Server) consoleWriter() (consoleWriteTransactions, error) {
	if s.consoleWrites != nil {
		return s.consoleWrites, nil
	}

	return nil, missingEndpointPolicy()
}

var _ consoleWriteTransactions = (*store.Console)(nil)
