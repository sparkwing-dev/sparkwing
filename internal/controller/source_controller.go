package controller

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// sourceTransactions commits source authorization and its release audit.
type sourceTransactions interface {
	ReleaseSourceCredential(context.Context, capability.Authorized) (store.GitCredential, capability.SourceReleaseAudit, error)
}

// SourceController releases source access only after its store audit commits.
type SourceController struct {
	GitHub        store.GitHubSourceProvider
	Cipher        BoundCipher
	ResolveTenant func(context.Context, store.Team) (TenantHandle, error)
}

// SourceCredentialResponse carries ephemeral source access for an already sealed checkout.
type SourceCredentialResponse struct {
	Kind              string   `json:"kind"`
	Host              string   `json:"host"`
	Username          string   `json:"username,omitempty"`
	Secret            string   `json:"secret"`
	KnownHosts        string   `json:"known_hosts,omitempty"`
	ExtraRepositories []string `json:"extra_repositories,omitempty"`
}

func (s *SourceController) transactions(ctx context.Context, team string) (sourceTransactions, error) {
	return ScopedTenantPort[sourceTransactions](ctx, store.Team(team), s.ResolveTenant)
}

// Release returns an App token or opens a stored credential after its audit commits.
func (s *SourceController) Release(ctx context.Context, a capability.Authorized) (SourceCredentialResponse, error) {
	if s == nil {
		return SourceCredentialResponse{}, missingEndpointPolicy()
	}
	var appMissing error
	if s.GitHub != nil {
		appTenant, err := ScopedTenantPort[interface {
			ReleaseGitHubSourceCredential(context.Context, capability.Authorized, store.GitHubSourceProvider) (source.Credential, error)
		}](ctx, store.Team(a.Target.Team), s.ResolveTenant)
		if err != nil {
			return SourceCredentialResponse{}, engineLookupError(err)
		}
		token, err := appTenant.ReleaseGitHubSourceCredential(ctx, a, s.GitHub)
		if err == nil {
			return SourceCredentialResponse{Kind: token.Kind, Host: token.Host, Secret: token.Secret, ExtraRepositories: token.ExtraRepositories}, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return SourceCredentialResponse{}, err
		}
		var missing *apierr.Error
		if errors.As(err, &missing) {
			appMissing = err
		}
	}
	if s.Cipher == nil {
		return SourceCredentialResponse{}, missingEndpointPolicy()
	}
	tenant, err := s.transactions(ctx, a.Target.Team)
	if err != nil {
		return SourceCredentialResponse{}, err
	}
	credential, audit, err := tenant.ReleaseSourceCredential(ctx, a)
	if err != nil {
		if appMissing != nil && errors.Is(err, store.ErrNotFound) {
			err = appMissing
		}
		var typed *apierr.Error
		if errors.As(err, &typed) {
			return SourceCredentialResponse{}, typed
		}
		return SourceCredentialResponse{}, engineLookupError(err)
	}
	if !sourceReleaseMatches(a, credential, audit) {
		return SourceCredentialResponse{}, errors.New("invalid persisted source release")
	}
	if err := ctx.Err(); err != nil {
		return SourceCredentialResponse{}, err
	}
	plain, err := openSecret(s.Cipher, gitCredentialBinding(credential), credential.Secret)
	if err != nil || plain == "" {
		return SourceCredentialResponse{}, errors.New("stored source credential did not open")
	}
	return SourceCredentialResponse{Kind: credential.Kind, Host: credential.Host, Username: credential.Username, Secret: plain, KnownHosts: credential.KnownHosts}, nil
}

func sourceReleaseMatches(a capability.Authorized, c store.GitCredential, audit capability.SourceReleaseAudit) bool {
	p, b := a.Principal.Capability, a.Binding
	return audit.ID != "" && !audit.ReleasedAt.IsZero() && audit.MachineID != "" && c.ID != "" && c.Host != "" &&
		(c.HostWide || c.Repository == audit.RepositoryKey) && c.ConfirmedAt != nil && (c.Kind == store.GitCredentialSSH || c.Kind == store.GitCredentialHTTPS) &&
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
	source := &SourceController{ResolveTenant: s.resolveTenant, Cipher: cipher}
	if s.githubApp != nil {
		source.GitHub = s.githubApp.client
	}
	return source
}

func (s *Server) sourceEndpoints() []Endpoint {
	e := s.engineController
	return []Endpoint{
		{Method: "POST", Pattern: "/api/v1/runs/{run}/source-credential", Authentication: capability.AuthCapability, Decode: runSelectorTarget, ResolveTarget: e.resolveRunSelector, Handle: s.handleSourceRelease},
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

var _ sourceTransactions = (*store.Tenant)(nil)
