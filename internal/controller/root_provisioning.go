package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type runnerEnrollmentRequest struct {
	AllowedLabels []string         `json:"allowed_labels,omitempty"`
	Personal      bool             `json:"personal,omitempty"`
	Platform      string           `json:"platform,omitempty"`
	Name          string           `json:"name"`
	Repos         []string         `json:"repos,omitempty"`
	ReservedPools []string         `json:"reserved_pools,omitempty"`
	Trust         capability.Trust `json:"trust,omitempty"`
	Grants        json.RawMessage  `json:"grants,omitempty"`
	ExpiresAt     json.RawMessage  `json:"expires_at,omitempty"`
}

type serviceCredentialRequest struct {
	Name      string             `json:"name"`
	Grants    []capability.Grant `json:"grants"`
	ExpiresAt json.RawMessage    `json:"expires_at,omitempty"`
}

func (s *Server) handleEnrollRunner(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var request runnerEnrollmentRequest
	if err := decodeEngineRequest(r, &request); err != nil {
		return err
	}
	if !request.Personal && !slices.Contains(a.Principal.Capability.Grants, capability.TeamAdmin) {
		return apierr.Authorization(apierr.ReasonMissingGrant, "team runner enrollment requires team administration")
	}
	spec, err := runnerEnrollmentSpec(request, a.Principal.Authority, a.Target.Team)
	if err != nil {
		return err
	}
	return s.provisionRoot(w, r, spec)
}

// safety: the store rechecks the authority's grants when it issues the spec.
func runnerEnrollmentSpec(request runnerEnrollmentRequest, authority capability.AuthorityRef, team string) (capability.ProvisionSpec, error) {
	if !store.ValidRunnerName(request.Name) || !request.Personal && len(request.Repos) == 0 || len(request.Repos) > maxRunnerRepos {
		return capability.ProvisionSpec{}, invalidEngineBody("name must be 1 to 63 letters, digits, '-', '_' or '.'; repos must contain at most 32 patterns and are required for shared runners")
	}
	allow, err := sourceurl.ParseRepoAllowlist(request.Repos)
	if err != nil {
		return capability.ProvisionSpec{}, invalidEngineBody("repos must contain valid host/path patterns")
	}
	if len(request.ReservedPools) > maxRunnerRepos || request.Platform != "" && request.Platform != "kubernetes" {
		return capability.ProvisionSpec{}, invalidEngineBody("reserved_pools must name at most 32 pools and platform must be empty or kubernetes")
	}
	seen := make(map[string]bool, len(request.ReservedPools))
	for _, pool := range request.ReservedPools {
		if !store.ValidRunnerName(pool) || seen[pool] {
			return capability.ProvisionSpec{}, invalidEngineBody("reserved_pools must contain unique names of 1 to 63 letters, digits, '-', '_' or '.'")
		}
		seen[pool] = true
	}
	if !request.Trust.Valid() || request.Personal && request.Trust != "" {
		return capability.ProvisionSpec{}, invalidEngineBody("trust must be verified-default or any, on a shared runner only")
	}
	expires, err := provisionExpiry(request.ExpiresAt)
	if err != nil {
		return capability.ProvisionSpec{}, err
	}
	grants, err := runnerGrants(request.Grants)
	if err != nil {
		return capability.ProvisionSpec{}, err
	}
	return capability.ProvisionSpec{
		Kind: capability.ProvisionMachine, Personal: request.Personal, Platform: request.Platform, Authority: &authority,
		RegistrationID: "runner:" + team + ":" + request.Name,
		Subject:        capability.Subject{Kind: capability.SubjectMachine, ID: request.Name}, Team: team,
		Grants: grants, Binding: capability.Binding{Kind: capability.BindingNone},
		Repositories: allow.Patterns(), ReservedPools: request.ReservedPools, Trust: request.Trust, ExpiresAt: expires, AllowedLabels: request.AllowedLabels,
	}, nil
}

func runnerGrants(raw json.RawMessage) ([]capability.Grant, error) {
	grants := []capability.Grant{capability.NodesClaim, capability.TriggersClaim}
	if raw == nil {
		return grants, nil
	}
	if json.Unmarshal(raw, &grants) != nil || len(grants) == 0 {
		return nil, invalidEngineBody("grants must contain nodes.claim or triggers.claim")
	}
	if err := validateCredentialRequest(credentialRequest{Grants: grants}); err != nil {
		return nil, err
	}
	for _, grant := range grants {
		if grant != capability.NodesClaim && grant != capability.TriggersClaim && grant != capability.ClaimsLaunch {
			return nil, invalidEngineBody("machine grants must be nodes.claim, triggers.claim or claims.launch")
		}
	}
	return grants, nil
}

func (s *Server) handleIssueServiceCredential(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var request serviceCredentialRequest
	if err := decodeEngineRequest(r, &request); err != nil {
		return err
	}
	if request.Name == "" || request.Name != strings.TrimSpace(request.Name) || len(request.Grants) == 0 {
		return invalidEngineBody("name and nonempty grants are required")
	}
	if err := validateCredentialRequest(credentialRequest{Grants: request.Grants}); err != nil {
		return err
	}
	for _, grant := range request.Grants {
		if !slices.Contains([]capability.Grant{capability.ClaimsLaunch, capability.OperatorLimits, capability.OperatorObjectStore, capability.OperatorTokens}, grant) {
			return invalidEngineBody("service grants must be deployment operations or claims.launch")
		}
	}
	expires, err := provisionExpiry(request.ExpiresAt)
	if err != nil {
		return err
	}
	return s.provisionRoot(w, r, capability.ProvisionSpec{
		Kind: capability.ProvisionService, ConsoleProof: capability.ProofForConsole(a.Principal), Authority: &a.Principal.Authority, RegistrationID: "service:" + request.Name,
		Subject: capability.Subject{Kind: capability.SubjectService, ID: request.Name},
		Grants:  request.Grants, Binding: capability.Binding{Kind: capability.BindingNone}, ExpiresAt: expires,
	})
}

func provisionExpiry(raw json.RawMessage) (*time.Time, error) {
	if raw == nil {
		return nil, nil
	}
	var expires *time.Time
	if err := json.Unmarshal(raw, &expires); err != nil || expires == nil || expires.IsZero() {
		return nil, invalidEngineBody("expires_at must be a timestamp")
	}
	return expires, nil
}

func (s *Server) provisionRoot(w http.ResponseWriter, r *http.Request, spec capability.ProvisionSpec) error {
	issued, err := s.issueRoot(r.Context(), spec)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, issued)
	return nil
}

func (s *Server) issueRoot(ctx context.Context, spec capability.ProvisionSpec) (credentialResponse, error) {
	if s.credentialIssuer == nil {
		return credentialResponse{}, routepolicy.MissingEndpointPolicy()
	}
	issued, err := s.credentialIssuer.ProvisionCapability(ctx, spec)
	if err != nil {
		if errors.Is(err, store.ErrAgentNameTaken) {
			return credentialResponse{}, &apierr.Error{Code: apierr.Conflict, Message: "an enrolled credential already uses this name"}
		}
		if errors.Is(err, store.ErrRunnerTokenLimit) {
			return credentialResponse{}, &apierr.Error{Code: apierr.QuotaExceeded, Message: err.Error()}
		}
		return credentialResponse{}, engineLookupError(err)
	}
	noteAuditTarget(ctx, "token_prefix", tokenPrefixTarget(issued.Capability.Prefix))
	return credentialResponse{
		Capability: issued.Raw, Prefix: issued.Capability.Prefix,
		Team: issued.Capability.Team, Grants: append([]capability.Grant{}, issued.Capability.Grants...), ExpiresAt: issued.Capability.ExpiresAt,
		Principal: issued.Capability.Subject.ID,
	}, nil
}
