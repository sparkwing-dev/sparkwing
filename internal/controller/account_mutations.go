package controller

import (
	"context"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// AccountWriter rechecks the account session and commits its mutation and session selection together.
type AccountWriter interface {
	SelectAccountTeam(context.Context, capability.Authorized, store.Team) error
	CreateAccountTeam(context.Context, capability.Authorized, store.TeamInfo) (store.TeamInfo, error)
	AcceptAccountInvitation(context.Context, capability.Authorized, string) (store.InvitationAcceptance, error)
	UnlinkAccountIdentity(context.Context, capability.Authorized, string) (store.IdentityUnlink, error)
}

// WithAccountWriter supplies account transactions before Handler is constructed.
func (s *Server) WithAccountWriter(writer AccountWriter) *Server {
	s.accountWrites = writer
	return s
}

func (s *Server) accountWriter() (AccountWriter, error) {
	if s.accountWrites != nil {
		return s.accountWrites, nil
	}

	return nil, routepolicy.MissingEndpointPolicy()
}

func unknownRole(role store.Role) error {
	return invalidEngineBody(fmt.Sprintf("unknown role %q: use %s, %s, or %s", role, store.RoleOwner, store.RoleEditor, store.RoleReader))
}

func accountMutationError(err error) error {
	switch {
	case errors.Is(err, store.ErrNotMember):
		return &apierr.Error{Code: apierr.NotFound, Message: "account resource not found"}
	case errors.Is(err, store.ErrInvalidSlug), errors.Is(err, store.ErrInvalidInput):
		return invalidEngineBody("invalid account request")
	case errors.Is(err, store.ErrSlugTaken), errors.Is(err, store.ErrAlreadyMember), errors.Is(err, store.ErrInvitationOpen):
		return &apierr.Error{Code: apierr.Conflict, Message: "account resource already exists"}
	case errors.Is(err, store.ErrTeamLimit):
		return apierr.Authorization(apierr.ReasonMissingGrant, fmt.Sprintf("one account may create at most %d teams, including its personal team", store.MaxCreatedTeams))
	case errors.Is(err, store.ErrLastOwner), errors.Is(err, store.ErrRoleAboveOwn),
		errors.Is(err, store.ErrTeamFull), errors.Is(err, store.ErrWaitlisted):
		return apierr.Authorization(apierr.ReasonMissingGrant, "account change refused")
	case errors.Is(err, store.ErrInvitationClosed):
		return &apierr.Error{Code: apierr.Conflict, Message: "invitation is no longer open"}
	case errors.Is(err, store.ErrLastSignInMethod):
		return &apierr.Error{Code: apierr.Conflict, Message: "link another sign-in method before unlinking this one", Details: map[string]any{"reason": "last_sign_in_method"}}
	default:
		return engineLookupError(err)
	}
}
