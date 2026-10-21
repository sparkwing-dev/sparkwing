package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// resolveChildRevisions preserves an authorized explicit source, or pins an
// inherited source to the parent's revision or a branch the controller resolves.
func (s *Server) resolveChildRevisions(ctx context.Context, engine *EngineController, a capability.Authorized, request *runspec.Request) error {
	if request.Lineage.ParentRunID == "" {
		return nil
	}
	if a.Target.RunID != request.Lineage.ParentRunID || a.Target.NodeID != request.Lineage.ParentJobID {
		return apierr.Authorization(apierr.ReasonBinding, "child source requires its live parent job")
	}
	tenant, err := engine.runTransactions(ctx, a.Target.Team)
	if err != nil {
		return err
	}
	parent, err := tenant.ReadRun(ctx, a.Target.RunID)
	if err != nil {
		return engineLookupError(err)
	}
	from := parent.Request.Request
	if !a.Principal.Capability.AllowsChild(request.Pipeline, string(request.Git.Repository.Key)) {
		return apierr.Authorization(apierr.ReasonRepositoryUnauthorized, "child source is outside the parent claim's declared children")
	}
	sameRepository := request.Git.Repository.Key == from.Git.Repository.Key
	if sameRepository && request.Source.Location != from.Source.Location {
		return apierr.Authorization(apierr.ReasonRepositoryUnauthorized, "an inherited repository uses its parent's source location")
	}
	if request.Git.SHA != "" {
		if sameRepository && (request.Git.SHA != from.Git.SHA || request.Git.Ref != from.Git.Ref || request.Source.Ref != from.Source.Ref) {
			return apierr.Authorization(apierr.ReasonBinding, "a child runs its parent's revision or a branch the controller resolves")
		}
		return nil
	}
	if request.Git.Ref == "" {
		return invalidEngineBody("fresh child source requires a branch")
	}
	request.Source.WorkingTree, request.Source.CommittedCheckout = false, true
	request.Source.BundleDigest = runspec.Optional[string]{}
	git, code, err := s.selectIntakeSource(ctx, store.Team(a.Target.Team), *request, nil)
	if err != nil {
		return err
	}
	request.Git, request.Source = git, code
	return nil
}

func (s *Server) selectIntakeSource(ctx context.Context, team store.Team, request runspec.Request, read func(context.Context, string, string) ([]byte, bool, error)) (runspec.Git, runspec.Source, error) {
	spec := source.Spec{RepoKey: string(request.Git.Repository.Key), RemoteURL: request.Source.Location.RemoteURL, LocalCheckout: request.Source.Location.LocalCheckout}
	access := &intakeRevisionAccess{server: s, team: team}
	options := source.Options{Access: access, Base: procenv.Base{Ambient: os.Environ(), Masker: requestMasker(ctx)}, ReadFile: read}
	if spec.LocalCheckout != "" || s.dashboard != nil && s.dashboard.Local {
		options.Base.Local, options.Base.Captured, options.GitAccess = true, os.Environ(), source.GitAccessOwner
	}
	if read != nil && s.githubApp != nil && strings.HasPrefix(spec.RepoKey, "gh:") {
		options.ResolveRef = func(ctx context.Context, ref string) (string, error) {
			installation, repository, err := access.githubAppRepository(ctx, spec)
			if err != nil {
				return "", err
			}
			repo, _ := store.ParseGitHubRepo(repository.FullName)
			sha, err := s.githubApp.client.ResolveCommit(ctx, installation.InstallationID, repo.Owner, repo.Name, ref)
			if errors.Is(err, githubapp.ErrRefNotFound) {
				return "", nil
			}
			return sha, err
		}
	}
	git, code, _, err := source.Select(ctx, spec, request.Pipeline, request.Git, request.Source, "", options)
	return git, code, err
}

type intakeRevisionAccess struct {
	server *Server
	team   store.Team
}

func (a *intakeRevisionAccess) GitCredential(ctx context.Context, spec source.Spec) (source.Credential, error) {
	s := a.server
	if s.operator == nil {
		return source.Credential{}, missingEndpointPolicy()
	}
	host, err := sourceurl.Host(spec.RemoteURL)
	if err != nil {
		return source.Credential{}, err
	}
	if host == "github.com" && s.githubApp != nil {
		installation, repository, err := a.githubAppRepository(ctx, spec)
		if err != nil {
			return source.Credential{}, err
		}
		token, err := s.githubApp.client.InstallationTokenByIDs(ctx, installation.InstallationID, []int64{repository.ID}, map[string]string{"contents": "read"})
		if err != nil {
			return source.Credential{}, err
		}
		requestMasker(ctx).Register(token.Token)
		return source.Credential{Kind: "github_app", Host: host, Secret: token.Token}, nil
	}
	return s.intakeStoredCredential(ctx, a.team, spec.RepoKey, host, "")
}

func (a *intakeRevisionAccess) githubAppRepository(ctx context.Context, spec source.Spec) (store.GitHubAppInstallation, githubapp.Repository, error) {
	tenant, err := a.server.tenantForTeam(ctx, a.team)
	if err != nil {
		return store.GitHubAppInstallation{}, githubapp.Repository{}, err
	}
	display, err := sourceurl.Identity(spec.RemoteURL)
	if err != nil {
		return store.GitHubAppInstallation{}, githubapp.Repository{}, err
	}
	_, slug, _ := strings.Cut(display, "/")
	installation, repository, err := a.server.resolveGitHubAppRepository(ctx, tenant, strings.TrimSuffix(slug, ".git"))
	if err == nil && runspec.RepoKey(fmt.Sprintf("gh:%d", repository.ID)) != runspec.RepoKey(spec.RepoKey) {
		err = apierr.Authorization(apierr.ReasonRepositoryUnauthorized, "source repository identity changed")
	}
	return installation, repository, err
}

func (s *Server) intakeStoredCredential(ctx context.Context, team store.Team, repository, host, kind string) (source.Credential, error) {
	tenant, err := s.tenantForTeam(ctx, team)
	if err != nil {
		return source.Credential{}, engineLookupError(err)
	}
	credential, err := tenant.GitCredentialForRepository(ctx, repository, host)
	if errors.Is(err, store.ErrNotFound) {
		return source.Credential{}, nil
	}
	if err != nil {
		return source.Credential{}, engineLookupError(err)
	}
	if credential.ConfirmedAt == nil || kind != "" && credential.Kind != kind {
		return source.Credential{}, nil
	}
	cipher, ok := s.secretsCipher.(BoundCipher)
	if !ok || cipher == nil {
		return source.Credential{}, &apierr.Error{Code: apierr.Unavailable, Message: "source credential cipher is unavailable"}
	}
	plain, err := openSecret(cipher, gitCredentialBinding(credential), credential.Secret)
	if err != nil || plain == "" {
		return source.Credential{}, &apierr.Error{Code: apierr.Unavailable, Message: "source credential could not be opened"}
	}
	requestMasker(ctx).Register(plain)
	return source.Credential{Kind: credential.Kind, Host: host, Username: credential.Username, Secret: plain, KnownHosts: credential.KnownHosts}, nil
}

func (*intakeRevisionAccess) BundleURL(context.Context, string) (string, error) {
	return "", errors.New("child revision lookup cannot read bundles")
}

// CoveredChildRepositories confirms App coverage independently of event subscriptions.
func (s *Server) CoveredChildRepositories(ctx context.Context, installations []store.GitHubAppInstallation) ([]string, error) {
	if s.githubApp == nil {
		return nil, nil
	}
	var keys []string
	for _, in := range installations {
		repositories, err := s.githubApp.client.InstallationRepositories(ctx, in.InstallationID)
		if err != nil && !errors.Is(err, githubapp.ErrTooManyRepositories) {
			return nil, err
		}
		for _, repo := range repositories {
			if repo.ID > 0 && repo.Owner.ID == in.AccountID {
				keys = append(keys, fmt.Sprintf("gh:%d", repo.ID))
			}
		}
	}
	return keys, nil
}
