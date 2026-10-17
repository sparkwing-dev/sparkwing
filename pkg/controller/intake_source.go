package controller

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type (
	verifiedIntakeSourceKey struct{}
	verifiedIntakeSource    struct {
		location   runspec.SourceLocation
		repository runspec.Repository
	}
)

func (s *Server) resolveIntakeSource(ctx context.Context, team string, request *runspec.Request) error {
	if request.Lineage.RetryOf != "" {
		return nil
	}
	remote := request.Source.Location.RemoteURL
	if remote == "" {
		if request.Git.Repository.Key != "" {
			return nil
		}
		return invalidEngineBody("source.location.remote_url is required to resolve repository identity")
	}
	parsed, err := parseIntakeRemote(remote)
	if err != nil {
		return err
	}
	display, err := sourceurl.Identity(remote)
	if err != nil {
		return invalidEngineBody("source repository identity is invalid")
	}
	input := runspec.RepoInput{RemoteURL: remote, Display: display}
	if strings.EqualFold(parsed.Hostname(), "github.com") {
		slug := strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git")
		repo, valid := store.ParseGitHubRepo(slug)
		if !valid || parsed.Port() != "" {
			return invalidEngineBody("GitHub source must name owner/repository")
		}
		id, err := s.intakeGitHubRepositoryID(ctx, team, repo.Slug())
		if err != nil {
			return err
		}
		input.GitHubRepositoryID = runspec.Optional[int64]{Set: true, Value: id}
	}
	canonical, err := runspec.CanonicalRepository(input)
	if err != nil {
		return invalidEngineBody("source repository identity is invalid")
	}
	if key := request.Git.Repository.Key; key != "" && key != canonical.Key {
		return apierr.Authorization(apierr.ReasonRepositoryUnauthorized, "source does not match repository identity")
	}
	request.Git.Repository = canonical
	return nil
}

func (s *Server) intakeGitHubRepositoryID(ctx context.Context, team, slug string) (int64, error) {
	if team == "" || s.store == nil {
		return 0, missingEndpointPolicy()
	}
	if s.githubApp != nil {
		tenant, err := s.store.ForTeam(ctx, store.Team(team))
		if err != nil {
			return 0, engineLookupError(err)
		}
		_, repo, err := s.resolveGitHubAppRepository(ctx, tenant, slug)
		if errors.Is(err, store.ErrNotFound) {
			return 0, apierr.Authorization(apierr.ReasonRepositoryUnauthorized, "GitHub installation does not cover the source repository")
		}
		if err != nil {
			return 0, errors.Join(&apierr.Error{Code: apierr.Unavailable, Message: "GitHub source identity could not be verified"}, err)
		}
		return repo.ID, nil
	}
	tenant, err := s.store.ForTeam(ctx, store.Team(team))
	if err != nil {
		return 0, engineLookupError(err)
	}
	credential, err := tenant.GitCredentialForHost(ctx, "github.com")
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return 0, engineLookupError(err)
	}
	token := ""
	if err == nil && credential.Kind == store.GitCredentialHTTPS && credential.ConfirmedAt != nil {
		cipher, ok := s.secretsCipher.(BoundCipher)
		if !ok {
			return 0, &apierr.Error{Code: apierr.Unavailable, Message: "GitHub identity lookup requires the configured source credential cipher"}
		}
		token, err = openSecret(cipher, gitCredentialBinding(credential.Team, credential.Host), credential.Secret)
		if err != nil || token == "" {
			return 0, &apierr.Error{Code: apierr.Unavailable, Message: "GitHub source identity credential could not be opened"}
		}
		s.masker.Register(token)
	}
	return sourceurl.GitHubRepositoryID(ctx, "https://github.com/"+slug, token)
}

func parseIntakeRemote(remote string) (*url.URL, error) {
	parsedRemote := remote
	if !strings.Contains(parsedRemote, "://") {
		if host, path, found := strings.Cut(parsedRemote, ":"); found {
			parsedRemote = "ssh://" + host + "/" + path
		}
	}
	parsed, err := url.Parse(parsedRemote)
	if err != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Scheme != "https" && parsed.Scheme != "http" && parsed.Scheme != "ssh" && parsed.Scheme != "git" {
		return nil, invalidEngineBody("source remote must be a credential-free repository URL")
	}
	if parsed.User != nil {
		_, password := parsed.User.Password()
		if parsed.Scheme != "ssh" || password {
			return nil, invalidEngineBody("source remote must not contain credentials")
		}
	}
	return parsed, nil
}

// ResolveScheduleRepository uses the same server-owned source identity lookup as run intake.
func (s *Server) ResolveScheduleRepository(ctx context.Context, team store.Team, remote string) (runspec.Repository, error) {
	request := runspec.Request{Source: runspec.Source{Location: runspec.SourceLocation{RemoteURL: remote}}}
	err := s.resolveIntakeSource(ctx, string(team), &request)
	return request.Git.Repository, err
}
