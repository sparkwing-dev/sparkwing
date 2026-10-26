package controller

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/envredact"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/internal/store"
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
		if request.Git.Repository.Key == "" {
			return invalidEngineBody("source.location.remote_url is required to resolve repository identity")
		}
		display, err := s.keyedRepositoryDisplay(ctx, team, *request)
		if err != nil {
			return err
		}
		return sealDerivedDisplay(&request.Git.Repository, display)
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
		id, err := s.intakeGitHubRepositoryID(ctx, team, repo.Slug(), request.Git.Repository.Key)
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

func sealDerivedDisplay(repository *runspec.Repository, display string) error {
	if supplied := repository.Display; supplied != "" && !strings.EqualFold(supplied, display) {
		return apierr.Authorization(apierr.ReasonRepositoryUnauthorized, "source repository display does not match its identity")
	}
	repository.Display = display
	return nil
}

// safety: a keyed request without a remote URL takes its display from the key, never from the caller;
// only a published local record, which no claimant can take, keeps its own label.
func (s *Server) keyedRepositoryDisplay(ctx context.Context, team string, request runspec.Request) (string, error) {
	key := string(request.Git.Repository.Key)
	switch {
	case strings.HasPrefix(key, "gh:"):
		display, err := s.githubAppRepositoryDisplay(ctx, team, strings.TrimPrefix(key, "gh:"))
		if errors.Is(err, errUncoveredGitHubRepository) {
			return key, nil
		}
		return display, err
	case strings.HasPrefix(key, "git:"):
		if runspec.ValidateRepoUse(runspec.RepoKey(key), runspec.RepoAccessRule) != nil {
			return "", invalidEngineBody("invalid git repository key")
		}
		return strings.TrimPrefix(key, "git:"), nil
	case strings.HasPrefix(key, "local:"):
		if request.Mode == runspec.IntakePublish {
			return request.Git.Repository.Display, nil
		}
		if checkout := request.Source.Location.LocalCheckout; checkout != "" {
			local, err := runspec.CanonicalRepository(runspec.RepoInput{AbsolutePath: checkout})
			if err == nil && local.Key == request.Git.Repository.Key {
				return local.Display, nil
			}
		}
		return key, nil
	}
	return "", invalidEngineBody("unknown repository key")
}

// fetchedGitHubRepository is the GitHub repository the controller itself fetches
// a run's code from, through the team's App installation that resolved it, or
// zero when the submitter supplies the code: an upload, a working tree, a local
// checkout or a run on the submitter's own machine.
func (s *Server) fetchedGitHubRepository(request runspec.Request) int64 {
	source := request.Source
	here := request.Options.Runner.Set && request.Options.Runner.Value.Kind == runspec.RunnerHere
	if s.githubApp == nil || source.Location.RemoteURL == "" || source.BundleDigest.Set || source.WorkingTree || source.Location.LocalCheckout != "" || here {
		return 0
	}
	key, ok := strings.CutPrefix(string(request.Git.Repository.Key), "gh:")
	if !ok {
		return 0
	}
	id, err := strconv.ParseInt(key, 10, 64)
	if err != nil {
		return 0
	}
	return id
}

var errUncoveredGitHubRepository = errors.New("no GitHub App installation of this team covers the repository")

func (s *Server) githubAppRepositoryDisplay(ctx context.Context, team, rawID string) (string, error) {
	refused := errUncoveredGitHubRepository
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 || s.githubApp == nil || s.operator == nil {
		return "", refused
	}
	tenant, err := s.tenantForTeam(ctx, store.Team(team))
	if err != nil {
		return "", engineLookupError(err)
	}
	installations, err := tenant.GitHubAppInstallations(ctx)
	if err != nil {
		return "", err
	}
	for _, installation := range installations {
		if installation.Suspended {
			continue
		}
		repositories, err := s.githubApp.client.InstallationRepositories(ctx, installation.InstallationID)
		if err != nil {
			return "", errors.Join(&apierr.Error{Code: apierr.Unavailable, Message: "GitHub source identity could not be verified"}, err)
		}
		for _, repository := range repositories {
			if canonical, valid := store.ParseGitHubRepo(repository.FullName); valid && repository.ID == id {
				return canonical.Slug(), nil
			}
		}
	}
	return "", refused
}

func (s *Server) intakeGitHubRepositoryID(ctx context.Context, team, slug string, supplied runspec.RepoKey) (int64, error) {
	if team == "" || s.operator == nil {
		return 0, routepolicy.MissingEndpointPolicy()
	}
	if s.githubApp != nil {
		tenant, err := s.tenantForTeam(ctx, store.Team(team))
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
	repository := ""
	if strings.HasPrefix(string(supplied), "gh:") {
		repository = string(supplied)
	}
	credential, err := s.intakeStoredCredential(ctx, store.Team(team), repository, "github.com", store.GitCredentialHTTPS)
	if err != nil {
		return 0, err
	}
	token := ""
	if credential.Kind == store.GitCredentialHTTPS {
		token = credential.Secret
	}
	id, err := sourceurl.GitHubRepositoryID(ctx, "https://github.com/"+slug, token)
	if err != nil {
		return 0, err
	}
	if repository != "" && repository != "gh:"+strconv.FormatInt(id, 10) {
		return 0, apierr.Authorization(apierr.ReasonRepositoryUnauthorized, "source does not match repository identity")
	}
	return id, nil
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
	if err := envredact.ValidateSourceReference(parsedRemote); err != nil {
		return nil, invalidEngineBody(err.Error())
	}
	return parsed, nil
}

// ResolveScheduleRepository uses the same server-owned source identity lookup as run intake.
func (s *Server) ResolveScheduleRepository(ctx context.Context, team store.Team, remote string) (runspec.Repository, error) {
	request := runspec.Request{Source: runspec.Source{Location: runspec.SourceLocation{RemoteURL: remote}}}
	err := s.resolveIntakeSource(ctx, string(team), &request)
	return request.Git.Repository, err
}
