package controller

import (
	"context"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// CreditReader supplies a single team's ledger without deployment-wide read authority.
// Its methods retain Tenant's read contracts and never mutate the financial ledger.
type CreditReader interface {
	Team() store.Team
	CreditState(context.Context, time.Duration) (store.CreditState, error)
	ListCreditGrants(context.Context, int) ([]store.CreditGrant, error)
	ListCreditCharges(context.Context, int) ([]store.CreditCharge, error)
}

// WithCreditReader supplies tenant ledger reads before Handler is constructed.
// A nil resolver uses Store.ForTeam. The selected reader must belong to the authorized team.
func (s *Server) WithCreditReader(resolve func(context.Context, store.Team) (CreditReader, error)) *Server {
	s.resolveCreditReader = resolve
	return s
}

func (s *Server) creditReader(ctx context.Context, team string) (CreditReader, error) {
	if !s.Metering() {
		return nil, &apierr.Error{Code: apierr.NotFound, Message: "credit billing is unavailable"}
	}
	if team == "" {
		return nil, missingEndpointPolicy()
	}
	var reader CreditReader
	var err error
	if s.resolveCreditReader != nil {
		reader, err = s.resolveCreditReader(ctx, store.Team(team))
	} else if s.store != nil {
		reader, err = s.store.ForTeam(ctx, store.Team(team))
	} else {
		return nil, missingEndpointPolicy()
	}
	if errors.Is(err, store.ErrUnknownTeam) || errors.Is(err, store.ErrNoTeam) {
		return nil, engineLookupError(store.ErrNotFound)
	}
	if err != nil {
		return nil, engineLookupError(err)
	}
	if reader == nil || reader.Team() != store.Team(team) {
		return nil, missingEndpointPolicy()
	}
	return reader, nil
}

func (s *Server) creditEndpoints() []Endpoint {
	return []Endpoint{
		{Method: "GET", Pattern: "/api/v1/credits/settings", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleCreditsSettingsShow},
		{Method: "GET", Pattern: "/api/v1/credits", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleCreditsShow},
		{Method: "GET", Pattern: "/api/v1/credits/history", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleCreditsHistory},
	}
}

var _ CreditReader = (*store.Tenant)(nil)
