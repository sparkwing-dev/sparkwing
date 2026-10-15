package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ConsoleWriter rechecks a direct operator session in every committing transaction.
type ConsoleWriter interface {
	ConsoleGrantCredit(context.Context, capability.Authorized, store.CreditGrantRequest) (store.CreditGrantResult, error)
	ConsoleSetBillingTrust(context.Context, capability.Authorized, store.BillingTrustChange) (store.BillingStanding, error)
	ConsoleFreeze(context.Context, capability.Authorized, string) (store.TeamCreditFreeze, error)
	ConsoleUnfreeze(context.Context, capability.Authorized, string, string) (store.TeamCreditFreeze, error)
	ConsoleDeleteAccount(context.Context, capability.Authorized) (store.AccountDeletion, error)
	ConsoleDeleteTeam(context.Context, capability.Authorized) (store.TeamDeletion, error)
	ConsoleApproveWaitlist(context.Context, capability.Authorized, []string, int, string) ([]store.Account, error)
	ConsoleSetCreditSettings(context.Context, capability.Authorized, store.CreditSettingsUpdate) (store.CreditSettings, error)
	ConsoleUnbindGitHubInstallation(context.Context, capability.Authorized, int64) (store.Team, error)
}

// WithConsoleWriter supplies the operator console's committing transactions.
func (s *Server) WithConsoleWriter(writer ConsoleWriter) *Server {
	s.consoleWrites = writer
	return s
}

func (s *Server) consoleWriter() (ConsoleWriter, error) {
	if s.consoleWrites != nil {
		return s.consoleWrites, nil
	}
	if s.store != nil {
		if writer, ok := any(s.store.AsOperator()).(ConsoleWriter); ok {
			return writer, nil
		}
	}
	return nil, missingEndpointPolicy()
}
