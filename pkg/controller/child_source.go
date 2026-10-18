package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (s *Server) resolveChildRevisions(ctx context.Context, engine *EngineController, a capability.Authorized, request *runspec.Request) error {
	if request.Lineage.ParentRunID == "" || request.Git.SHA != "" {
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
	if request.Git.Repository.Key != parent.Request.Request.Git.Repository.Key || request.Source.Location != parent.Request.Request.Source.Location {
		return apierr.Authorization(apierr.ReasonRepositoryUnauthorized, "fresh child source must use its parent's repository location")
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
	options := source.Options{Access: &intakeRevisionAccess{server: s, team: team}, Base: procenv.Base{Ambient: os.Environ(), Masker: requestMasker(ctx)}, ReadFile: read}
	if spec.LocalCheckout != "" || s.dashboard != nil && s.dashboard.Local {
		options.Base.Local, options.Base.Captured = true, os.Environ()
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
	if s.store == nil {
		return source.Credential{}, missingEndpointPolicy()
	}
	tenant, err := s.store.ForTeam(ctx, a.team)
	if err != nil {
		return source.Credential{}, err
	}
	host, err := sourceurl.Host(spec.RemoteURL)
	if err != nil {
		return source.Credential{}, err
	}
	if host == "github.com" && s.githubApp != nil {
		display, err := sourceurl.Identity(spec.RemoteURL)
		if err != nil {
			return source.Credential{}, err
		}
		_, slug, _ := strings.Cut(display, "/")
		installation, repository, err := s.resolveGitHubAppRepository(ctx, tenant, slug)
		if err != nil {
			return source.Credential{}, err
		}
		if runspec.RepoKey(fmt.Sprintf("gh:%d", repository.ID)) != runspec.RepoKey(spec.RepoKey) {
			return source.Credential{}, apierr.Authorization(apierr.ReasonRepositoryUnauthorized, "child source repository identity changed")
		}
		token, err := s.githubApp.client.InstallationTokenByIDs(ctx, installation.InstallationID, []int64{repository.ID}, map[string]string{"contents": "read"})
		if err != nil {
			return source.Credential{}, err
		}
		requestMasker(ctx).Register(token.Token)
		return source.Credential{Kind: "github_app", Host: host, Secret: token.Token}, nil
	}
	return s.intakeStoredCredential(ctx, a.team, host, "")
}

func (s *Server) intakeStoredCredential(ctx context.Context, team store.Team, host, kind string) (source.Credential, error) {
	tenant, err := s.store.ForTeam(ctx, team)
	if err != nil {
		return source.Credential{}, engineLookupError(err)
	}
	credential, err := tenant.GitCredentialForHost(ctx, host)
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
	plain, err := openSecret(cipher, gitCredentialBinding(credential.Team, credential.Host), credential.Secret)
	if err != nil || plain == "" {
		return source.Credential{}, &apierr.Error{Code: apierr.Unavailable, Message: "source credential could not be opened"}
	}
	requestMasker(ctx).Register(plain)
	return source.Credential{Kind: credential.Kind, Host: host, Username: credential.Username, Secret: plain, KnownHosts: credential.KnownHosts}, nil
}

func (*intakeRevisionAccess) BundleURL(context.Context, string) (string, error) {
	return "", errors.New("child revision lookup cannot read bundles")
}
