package controller

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// SourceTransactions commits source authorization and machine-owner consent.
type SourceTransactions interface {
	ReleaseSourceCredential(context.Context, capability.Authorized) (store.GitCredential, capability.SourceReleaseAudit, error)
	SetSourceMachineConsent(context.Context, capability.AuthorityRef, string, bool) error
}

// SourceController opens only credentials returned with a committed release audit.
type SourceController struct {
	Store         *store.Store
	Cipher        BoundCipher
	ResolveTenant func(context.Context, store.Team) (SourceTransactions, error)
}

// SourceCredentialResponse carries ephemeral source access for an already sealed checkout.
type SourceCredentialResponse struct {
	Kind       string `json:"kind"`
	Host       string `json:"host"`
	Username   string `json:"username,omitempty"`
	Secret     string `json:"secret"`
	KnownHosts string `json:"known_hosts,omitempty"`
}

func (s *SourceController) transactions(ctx context.Context, team string) (SourceTransactions, error) {
	if s == nil || team == "" {
		return nil, missingEndpointPolicy()
	}
	if s.ResolveTenant != nil {
		tenant, err := s.ResolveTenant(ctx, store.Team(team))
		if err == nil && tenant == nil {
			return nil, missingEndpointPolicy()
		}
		return tenant, engineLookupError(err)
	}
	if s.Store == nil {
		return nil, missingEndpointPolicy()
	}
	tenant, err := s.Store.ForTeam(ctx, store.Team(team))
	return tenant, engineLookupError(err)
}

// Release decrypts the audited credential after the store commits its authorization.
func (s *SourceController) Release(ctx context.Context, a capability.Authorized) (SourceCredentialResponse, error) {
	if s == nil || s.Cipher == nil {
		return SourceCredentialResponse{}, missingEndpointPolicy()
	}
	tenant, err := s.transactions(ctx, a.Target.Team)
	if err != nil {
		return SourceCredentialResponse{}, err
	}
	credential, audit, err := tenant.ReleaseSourceCredential(ctx, a)
	if err != nil {
		return SourceCredentialResponse{}, engineLookupError(err)
	}
	if !sourceReleaseMatches(a, credential, audit) {
		return SourceCredentialResponse{}, errors.New("invalid persisted source release")
	}
	if err := ctx.Err(); err != nil {
		return SourceCredentialResponse{}, err
	}
	plain, err := openSecret(s.Cipher, gitCredentialBinding(credential.Team, credential.Host), credential.Secret)
	if err != nil || plain == "" {
		return SourceCredentialResponse{}, errors.New("stored source credential did not open")
	}
	return SourceCredentialResponse{Kind: credential.Kind, Host: credential.Host, Username: credential.Username, Secret: plain, KnownHosts: credential.KnownHosts}, nil
}

func sourceReleaseMatches(a capability.Authorized, c store.GitCredential, audit capability.SourceReleaseAudit) bool {
	p, b := a.Principal.Capability, a.Binding
	return audit.ID != "" && !audit.ReleasedAt.IsZero() && audit.MachineID != "" && c.ID != "" && c.Host != "" &&
		c.ConfirmedAt != nil && (c.Kind == store.GitCredentialSSH || c.Kind == store.GitCredentialHTTPS) &&
		string(c.Team) == a.Target.Team && audit.Team == a.Target.Team && audit.CredentialID == c.ID && audit.Host == c.Host &&
		p.Data != nil && audit.RepositoryKey == p.Data.RepositoryKey && audit.RepositoryKey != "" &&
		audit.RunID == a.Target.RunID && audit.RunID == b.RunID && audit.NodeID == b.NodeID && audit.Generation == b.Generation &&
		audit.CapabilityPrefix == p.Prefix && audit.CapabilityPrefix != ""
}

// WithSourceController supplies the source-release transaction and decryption adapters.
func (s *Server) WithSourceController(source *SourceController) *Server {
	s.sourceController = source
	return s
}

func (s *Server) sourceAdapter() *SourceController {
	if s.sourceController != nil {
		return s.sourceController
	}
	cipher, _ := s.secretsCipher.(BoundCipher)
	return &SourceController{Store: s.store, Cipher: cipher}
}

func (s *Server) sourceEndpoints() []Endpoint {
	e := s.engineController
	if e == nil {
		e = &EngineController{Store: s.store}
	}
	return []Endpoint{
		{Method: "POST", Pattern: "/api/v1/runs/{run}/source-credential", Authentication: capability.AuthCapability, Decode: runSelectorTarget, ResolveTarget: e.resolveRunSelector, Handle: s.handleSourceRelease},
		{Method: "PUT", Pattern: "/api/v1/tokens/{token}/git-credentials", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleSourceConsent},
	}
}

func (s *Server) handleSourceRelease(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil {
		return err
	}
	if len(body) != 0 {
		return invalidEngineBody("source release takes no body")
	}
	credential, err := s.sourceAdapter().Release(r.Context(), a)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, credential)
	return nil
}

func (s *Server) handleSourceConsent(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var request struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeEngineRequest(r, &request); err != nil {
		return err
	}
	if request.Enabled == nil {
		return invalidEngineBody("enabled must be a boolean")
	}
	tenant, err := s.sourceAdapter().transactions(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	if err := tenant.SetSourceMachineConsent(r.Context(), a.Principal.Authority, a.Target.TokenPrefix, *request.Enabled); err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": *request.Enabled})
	return nil
}

var _ SourceTransactions = (*store.Tenant)(nil)
