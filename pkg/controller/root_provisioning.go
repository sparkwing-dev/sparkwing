package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type runnerEnrollmentRequest struct {
	Personal      bool             `json:"personal,omitempty"`
	Name          string           `json:"name"`
	Repos         []string         `json:"repos"`
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
	if !store.ValidRunnerName(request.Name) || len(request.Repos) == 0 || len(request.Repos) > maxRunnerRepos {
		return invalidEngineBody("name must be 1 to 63 letters, digits, '-', '_' or '.'; repos must contain 1 to 32 patterns")
	}
	allow, err := sourceurl.ParseRepoAllowlist(request.Repos)
	if err != nil {
		return invalidEngineBody("repos must contain valid host/path patterns")
	}
	seen := make(map[string]bool, len(request.ReservedPools))
	for _, pool := range request.ReservedPools {
		if strings.TrimSpace(pool) == "" || seen[pool] {
			return invalidEngineBody("reserved_pools must contain unique nonempty names")
		}
		seen[pool] = true
	}
	if !request.Trust.Valid() || request.Personal && request.Trust != "" {
		return invalidEngineBody("trust must be verified-default or any, on a shared runner only")
	}
	expires, err := provisionExpiry(request.ExpiresAt)
	if err != nil {
		return err
	}
	grants := []capability.Grant{capability.NodesClaim, capability.TriggersClaim}
	if request.Grants != nil {
		if json.Unmarshal(request.Grants, &grants) != nil || len(grants) == 0 {
			return invalidEngineBody("grants must contain nodes.claim or triggers.claim")
		}
		if err := validateCredentialRequest(credentialRequest{Grants: grants}); err != nil {
			return err
		}
		for _, grant := range grants {
			if grant != capability.NodesClaim && grant != capability.TriggersClaim && grant != capability.ClaimsLaunch {
				return invalidEngineBody("machine grants must be nodes.claim, triggers.claim or claims.launch")
			}
		}
	}
	return s.provisionRoot(w, r, capability.ProvisionSpec{
		Kind: capability.ProvisionMachine, Personal: request.Personal, Authority: &a.Principal.Authority,
		RegistrationID: "runner:" + a.Target.Team + ":" + request.Name,
		Subject:        capability.Subject{Kind: capability.SubjectMachine, ID: request.Name}, Team: a.Target.Team,
		Grants: grants, Binding: capability.Binding{Kind: capability.BindingNone},
		Repositories: allow.Patterns(), ReservedPools: request.ReservedPools, Trust: request.Trust, ExpiresAt: expires,
	})
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
	if s.credentialIssuer == nil {
		return missingEndpointPolicy()
	}
	issued, err := s.credentialIssuer.ProvisionCapability(r.Context(), spec)
	if err != nil {
		if errors.Is(err, store.ErrAgentNameTaken) {
			return &apierr.Error{Code: apierr.Conflict, Message: "an enrolled credential already uses this name"}
		}
		if errors.Is(err, store.ErrRunnerTokenLimit) {
			return &apierr.Error{Code: apierr.QuotaExceeded, Message: "the team's runner enrollment limit is reached"}
		}
		return engineLookupError(err)
	}
	noteAuditTarget(r.Context(), "token_prefix", tokenPrefixTarget(issued.Capability.Prefix))
	writeJSON(w, http.StatusCreated, credentialResponse{
		Capability: issued.Raw, Prefix: issued.Capability.Prefix,
		Team: issued.Capability.Team, Grants: append([]capability.Grant{}, issued.Capability.Grants...), ExpiresAt: issued.Capability.ExpiresAt,
		Principal: issued.Capability.Subject.ID,
	})
	return nil
}
