package controller

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// sourceTransactions commits source authorization and its release audit.
type sourceTransactions interface {
	ReleaseSourceCredential(context.Context, capability.Authorized) (store.GitCredential, capability.SourceReleaseAudit, error)
}

// GitHubSourceProvider mints repository-scoped read tokens after store authorization.
type GitHubSourceProvider interface {
	InstallationTokenByIDs(context.Context, int64, []int64, map[string]string) (githubapp.Token, error)
}

type githubSourceTransactions interface {
	PrepareGitHubSourceRelease(context.Context, capability.Authorized) (store.GitHubSourceGrant, error)
	CommitGitHubSourceRelease(context.Context, capability.Authorized, store.GitHubSourceGrant, time.Time) error
}

func (s *SourceController) releaseGitHubSource(ctx context.Context, a capability.Authorized) (SourceCredentialResponse, error) {
	tenant, err := scopedTenantPort[githubSourceTransactions](ctx, store.Team(a.Target.Team), s.ResolveTenant)
	if err != nil {
		return SourceCredentialResponse{}, engineLookupError(err)
	}
	grant, err := tenant.PrepareGitHubSourceRelease(ctx, a)
	if err != nil {
		return SourceCredentialResponse{}, err
	}
	token, err := s.GitHub.InstallationTokenByIDs(ctx, grant.Installation, grant.Repositories, map[string]string{"contents": "read"})
	if errors.Is(err, githubapp.ErrNotInstalled) {
		return SourceCredentialResponse{}, &apierr.Error{Code: apierr.Conflict, Message: "install the team's GitHub App on the approved repositories or update the owner's extras: " + strings.Join(grant.Names, ", ")}
	}
	if err != nil {
		return SourceCredentialResponse{}, errors.Join(&apierr.Error{Code: apierr.Unavailable, Message: "GitHub source token mint failed"}, err)
	}
	if token.Token == "" {
		return SourceCredentialResponse{}, &apierr.Error{Code: apierr.Unavailable, Message: "GitHub returned no live source token"}
	}
	if err := tenant.CommitGitHubSourceRelease(ctx, a, grant, token.ExpiresAt); err != nil {
		return SourceCredentialResponse{}, err
	}
	return SourceCredentialResponse{Kind: "github_app", Host: "github.com", Secret: token.Token, ExtraRepositories: grant.Names[1:]}, nil
}

// SourceController releases source access only after its store audit commits.
type SourceController struct {
	GitHub        GitHubSourceProvider
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
	return scopedTenantPort[sourceTransactions](ctx, store.Team(team), s.ResolveTenant)
}

// Release returns an App token or opens a stored credential after its audit commits.
func (s *SourceController) Release(ctx context.Context, a capability.Authorized) (SourceCredentialResponse, error) {
	if s == nil {
		return SourceCredentialResponse{}, routepolicy.MissingEndpointPolicy()
	}
	var appMissing error
	if s.GitHub != nil {
		token, err := s.releaseGitHubSource(ctx, a)
		if err == nil {
			return token, nil
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
		return SourceCredentialResponse{}, routepolicy.MissingEndpointPolicy()
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

func (s *Server) sourceEndpoints() []controllerwire.Endpoint {
	e := s.engineController
	return []controllerwire.Endpoint{
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
