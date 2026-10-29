package controller

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// CredentialIssuer commits root provisioning and narrowing through Store.Issue.
type CredentialIssuer interface {
	ProvisionCapability(context.Context, capability.ProvisionSpec) (capability.Issued, error)
	DelegateCapability(context.Context, capability.DelegateSpec) (capability.Issued, error)
}

type credentialRequest struct {
	AllowTeamAdmin bool                  `json:"allow_team_admin,omitempty"`
	Grants         []capability.Grant    `json:"grants"`
	ExpiresAt      *time.Time            `json:"expires_at,omitempty"`
	Data           *capability.DataScope `json:"data,omitempty"`
}

type credentialResponse struct {
	Capability string             `json:"capability"`
	Prefix     string             `json:"prefix"`
	Team       string             `json:"team"`
	Grants     []capability.Grant `json:"grants"`
	ExpiresAt  *time.Time         `json:"expires_at,omitempty"`
	Principal  string             `json:"principal,omitempty"`
}

// WithCredentialIssuer supplies the transactional issuer for embedded controllers and tests.
func (s *Server) WithCredentialIssuer(issuer CredentialIssuer) *Server {
	s.credentialIssuer = issuer
	return s
}

func (s *Server) handleIssueCredential(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var request credentialRequest
	if err := decodeEngineRequest(r, &request); err != nil {
		return err
	}
	if err := validateCredentialRequest(request); err != nil {
		return err
	}
	if s.credentialIssuer == nil {
		return routepolicy.MissingEndpointPolicy()
	}
	p := a.Principal
	// safety: a token minted under the local peer keeps its local subject, which no member route accepts from a bearer.
	if p.Class == capability.CredentialPeer && request.Data == nil {
		return &apierr.Error{Code: apierr.CapabilityUnsupported, Message: "a local controller issues no member tokens; the CLI reaches it over its socket, and `sparkwing dashboard open` signs a browser in"}
	}
	var issued capability.Issued
	var err error
	if p.DirectAccountSession() && request.Data == nil {
		issued, err = s.credentialIssuer.ProvisionCapability(r.Context(), capability.ProvisionSpec{
			Kind: capability.ProvisionMember, AllowTeamAdmin: request.AllowTeamAdmin, Authority: &p.Authority, Subject: p.Capability.Subject,
			Team: a.Target.Team, Grants: request.Grants, Binding: capability.Binding{Kind: capability.BindingNone}, ExpiresAt: request.ExpiresAt,
		})
	} else {
		if request.ExpiresAt == nil {
			request.ExpiresAt = p.Capability.ExpiresAt
		}
		binding, repositories := p.Capability.Binding, p.Capability.Repositories
		if request.Data != nil {
			repositories = []string{request.Data.RepositoryKey}
			binding = capability.Binding{Kind: capability.BindingNone}
			if request.Data.Use == capability.DataPublish {
				binding = capability.Binding{Kind: capability.BindingRun, RunID: request.Data.RunID}
			}
		}
		issued, err = s.credentialIssuer.DelegateCapability(r.Context(), capability.DelegateSpec{
			Parent: p.Authority, Repositories: repositories, Audience: capability.Controller, Grants: request.Grants,
			Binding: binding, Data: request.Data, ExpiresAt: request.ExpiresAt,
		})
	}
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusCreated, credentialResponse{
		Capability: issued.Raw, Prefix: issued.Capability.Prefix,
		Team: issued.Capability.Team, Grants: append([]capability.Grant{}, issued.Capability.Grants...), ExpiresAt: issued.Capability.ExpiresAt,
		Principal: issued.Capability.Subject.ID,
	})
	return nil
}

func validateCredentialRequest(request credentialRequest) error {
	if request.Grants == nil || request.ExpiresAt != nil && request.ExpiresAt.IsZero() {
		return invalidEngineBody("grants must be an array and expiry must be a timestamp")
	}
	seen := make(map[capability.Grant]bool, len(request.Grants))
	for _, grant := range request.Grants {
		if seen[grant] || !slices.Contains(capability.Grants(), grant) {
			return invalidEngineBody("grants must contain unique known permissions")
		}
		seen[grant] = true
	}
	return nil
}

var _ CredentialIssuer = (*store.Operator)(nil)
