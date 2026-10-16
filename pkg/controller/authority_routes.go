package controller

import (
	"context"
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// AuthorityRevoker rechecks actor and target authority and advances revocation
// generations in the same transaction. Tenant and Operator keep their own scope.
type AuthorityRevoker interface {
	RevokeAuthority(context.Context, store.Actor, capability.AuthorityRef) error
}

type membershipRequest struct {
	Role store.Role `json:"role"`
}

func (s *Server) authorityEndpoints() []Endpoint {
	e := s.engineController
	if e == nil {
		e = &EngineController{Store: s.store}
	}
	return []Endpoint{
		{
			Method: "PATCH", Pattern: "/api/v1/team/members/{account}", Authentication: capability.AuthCapability,
			Decode: authorityTarget, Handle: e.handleUpdateMembership,
		},
		{
			Method: "DELETE", Pattern: "/api/v1/team/members/{account}", Authentication: capability.AuthCapability,
			Decode: authorityTarget, Handle: e.handleUpdateMembership,
		},
		{
			Method: "GET", Pattern: "/api/v1/team/members", Authentication: capability.AuthCapability,
			Decode: authorityTarget, Handle: e.handleMemberDirectory,
		},
		{
			Method: "GET", Pattern: "/api/v1/auth/whoami", Authentication: capability.AuthCapability,
			Decode: authorityTarget, Handle: s.handleIdentity,
		},
		{
			Method: "GET", Pattern: "/api/v1/services", Authentication: capability.AuthCapability,
			Decode: authorityTarget, Handle: s.handleServices,
		},
		{
			Method: "GET", Pattern: "/api/v1/data/capabilities", Authentication: capability.AuthCapability,
			Decode: authorityTarget, Handle: s.handleDirectCapabilities,
		},
		{Method: "POST", Pattern: "/api/v1/runs/{run}/oidc-token", Authentication: capability.AuthCapability, Decode: e.runTarget, Handle: s.handleOIDCToken},
		{Method: "GET", Pattern: "/.well-known/openid-configuration", Authentication: capability.AuthPublic, Handle: s.handleOIDCDiscovery},
		{Method: "GET", Pattern: "/.well-known/jwks.json", Authentication: capability.AuthPublic, Handle: s.handleOIDCKeys},
	}
}

func authorityTarget(r *http.Request) (capability.RequestEvidence, error) {
	return capability.RequestEvidence{Target: capability.Target{TokenPrefix: r.PathValue("token"), AccountID: r.PathValue("account")}}, nil
}

func authorityActor(a capability.Authorized) store.Actor {
	return store.Actor{ConsoleProof: capability.ProofForConsole(a.Principal), Authority: a.Principal.Authority, Kind: store.ActorOperator, CapabilityPrefix: principalCapabilityPrefix(a.Principal)}
}

func (s *EngineController) handleMemberDirectory(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.runTransactions(r.Context(), a.Target.Team)
	if err != nil {
		return authorityMutationError(err)
	}
	members, err := tenant.Members(r.Context())
	if err != nil {
		return authorityMutationError(err)
	}
	out := make([]memberJSON, 0, len(members))
	for _, member := range members {
		out = append(out, memberJSON{UserID: member.AccountID, Email: member.Email, Name: member.Name, Role: string(member.Role)})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *EngineController) handleRevokeAuthority(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var revoker AuthorityRevoker
	var err error
	if a.Target.TokenPrefix == "" {
		return missingEndpointPolicy()
	}
	if r.Pattern == "DELETE /api/v1/tokens/{token}" {
		revoker, err = s.runTransactions(r.Context(), a.Target.Team)
	} else if s.ResolveOperator != nil {
		revoker, err = s.ResolveOperator(r.Context())
	} else if s.Store != nil {
		revoker = s.Store.AsOperator()
	}
	if err != nil {
		return authorityMutationError(err)
	}
	if revoker == nil {
		return missingEndpointPolicy()
	}
	target := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: a.Target.TokenPrefix}
	if err := revoker.RevokeAuthority(r.Context(), authorityActor(a), target); err != nil {
		return authorityMutationError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *EngineController) handleUpdateMembership(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	change := store.MembershipUpdate{AccountID: a.Target.AccountID}
	if change.AccountID == "" {
		return missingEndpointPolicy()
	}
	if r.Method == http.MethodPatch {
		var body membershipRequest
		if err := decodeEngineRequest(r, &body); err != nil {
			return err
		}
		if !body.Role.Valid() {
			return invalidEngineBody("role must be owner, editor, or reader")
		}
		noteAuditTarget(r.Context(), "role", string(body.Role))
		change.Role = &body.Role
	}
	tenant, err := s.runTransactions(r.Context(), a.Target.Team)
	if err != nil {
		return authorityMutationError(err)
	}
	if err := tenant.UpdateMembership(r.Context(), authorityActor(a), change); err != nil {
		return authorityMutationError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func authorityMutationError(err error) error {
	if errors.Is(err, store.ErrNotMember) {
		return &apierr.Error{Code: apierr.NotFound, Message: "team member not found"}
	}
	if errors.Is(err, store.ErrLastOwner) {
		return &apierr.Error{Code: apierr.Conflict, Message: "a team must retain at least one owner"}
	}
	if errors.Is(err, store.ErrRoleAboveOwn) {
		return &apierr.Error{Code: apierr.Forbidden, Message: "membership change refused", Details: map[string]any{"reason": apierr.ReasonMissingGrant}}
	}
	return engineLookupError(err)
}
