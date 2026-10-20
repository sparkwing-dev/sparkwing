package controller

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// CreditReader supplies a single team's ledger without deployment-wide read authority.
// Its methods retain Tenant's read contracts and never mutate the financial ledger.
type CreditReader interface {
	Team() store.Team
	CreditState(context.Context, time.Duration) (store.CreditState, error)
	ListCreditGrants(context.Context, int) ([]store.CreditGrant, error)
	ListCreditCharges(context.Context, int) ([]store.CreditCharge, error)
}

func (s *Server) creditReader(ctx context.Context, team string) (CreditReader, error) {
	return tenantPort[CreditReader](ctx, s, store.Team(team))
}

func (s *Server) creditEndpoints() []Endpoint {
	return []Endpoint{
		{Method: "GET", Pattern: "/api/v1/credits/settings", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.meteredOnly(s.handleCreditsSettingsShow)},
		{Method: "GET", Pattern: "/api/v1/credits", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.meteredOnly(s.handleCreditsShow)},
		{Method: "GET", Pattern: "/api/v1/credits/history", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.meteredOnly(s.handleCreditsHistory)},
	}
}

var _ CreditReader = (*store.Tenant)(nil)
