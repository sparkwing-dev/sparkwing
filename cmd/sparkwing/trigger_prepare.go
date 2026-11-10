package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/envredact"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

type triggerSourceInput struct {
	checkout, ref, pipeline, pipelineRef string
	snapshot                             *worktreeSnapshot
	environment                          []string
	githubID                             func(context.Context, string) (int64, error)
	buildEnv                             func(procenv.Kind, procenv.Base, procenv.Runtime) (procenv.Env, error)
}

type preparedTriggerSource struct {
	project  *projectconfig.Config
	pipeline string
	profile  string
	git      runspec.Git
	source   runspec.Source
	checkout triggerCheckout
}

type triggerCheckout struct {
	directory   string
	environment []string
	diagnostics io.Writer
}

func (c triggerCheckout) read(ctx context.Context, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	cmd := procenv.ControlCommandContext(ctx, append(append([]string{}, c.environment...), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_LFS_SKIP_SMUDGE=1", "GIT_TERMINAL_PROMPT=0"), "git", append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "credential.helper=", "-C", c.directory}, args...)...)
	cmd.Stderr = c.diagnostics
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			err = fmt.Errorf("%w: %s", err, gitDiagnostic(exit.Stderr))
		}
		return "", fmt.Errorf("inspect trigger checkout: %w", errors.Join(ctx.Err(), err))
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

const maxGitDiagnostic = 512

func gitDiagnostic(stderr []byte) string {
	fields := strings.Fields(string(stderr))
	for i, field := range fields {
		value := strings.Trim(field, "'\"")
		fields[i] = strings.Replace(field, value, envredact.RedactValue(value), 1)
	}
	detail := strings.Join(fields, " ")
	if len(detail) > maxGitDiagnostic {
		detail = detail[:maxGitDiagnostic] + "..."
	}
	return detail
}

func (s preparedTriggerSource) checkReachable(ctx context.Context) error {
	for _, sha := range []string{s.git.SHA, s.source.Ref} {
		refs, err := s.checkout.read(ctx, "for-each-ref", "--count=1", "--contains="+sha, "--format=%(refname)", "refs/remotes/origin/")
		if err != nil {
			return err
		}
		if refs == "" {
			fetched, err := s.checkout.fetchedFromOrigin(ctx, sha)
			if err != nil {
				return err
			}
			if !fetched {
				return &apierr.Error{Code: apierr.InvalidArgs, Message: "selected source commit is not on origin", Hint: "push both selected commits or trigger with --working-tree"}
			}
		}
	}
	return nil
}

func (c triggerCheckout) fetchedFromOrigin(ctx context.Context, sha string) (bool, error) {
	origin, err := c.read(ctx, "remote", "get-url", "origin")
	if err != nil {
		return false, err
	}
	file, err := c.read(ctx, "rev-parse", "--git-path", "FETCH_HEAD")
	if err != nil {
		return false, err
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(c.directory, file)
	}
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect trigger fetch evidence: %w", err)
	}
	// bug: fetching an exact commit leaves FETCH_HEAD but no origin-tracking branch.
	if parsed, err := url.Parse(origin); err == nil && parsed.Host != "" {
		parsed.User = nil
		origin = parsed.String()
	} else if _, remote, ok := strings.Cut(origin, "@"); ok {
		origin = remote
	}
	want := sha + "\t\t'" + sha + "' of " + strings.TrimSuffix(strings.TrimRight(origin, "/"), ".git")
	for line := range strings.SplitSeq(string(data), "\n") {
		if line == want {
			return true, nil
		}
	}
	return false, nil
}

func prepareTriggerSource(ctx context.Context, input triggerSourceInput) (preparedTriggerSource, error) {
	var result preparedTriggerSource
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if input.pipeline == "" {
		return result, &clisurface.UsageError{Path: "sparkwing pipeline trigger", Message: "pipeline is required"}
	}
	checkout, err := filepath.Abs(input.checkout)
	if err != nil {
		return result, err
	}
	buildEnv := input.buildEnv
	if buildEnv == nil {
		buildEnv = procenv.Build
	}
	env, err := buildEnv(procenv.Git, procenv.Base{Local: true, Captured: input.environment, Masker: secrets.NewMasker()}, procenv.Runtime{})
	if err != nil {
		return result, err
	}
	reader := triggerCheckout{directory: checkout, environment: slices.Collect(env.Values())}
	read := func(args ...string) (string, error) { return reader.read(ctx, args...) }
	checkout, err = read("rev-parse", "--show-toplevel")
	if err != nil {
		return result, err
	}
	reader.directory = checkout
	ref := input.ref
	if ref == "" {
		ref = "HEAD"
	}
	options := source.Options{GitAccess: source.GitAccessOwner, Base: procenv.Base{Local: true, Captured: input.environment, Masker: secrets.NewMasker()}, BuildEnvironment: buildEnv}
	var sha string
	if input.snapshot != nil && input.ref == "" {
		sha = input.snapshot.SHA
	} else {
		sha, err = source.ResolveRef(ctx, source.Spec{LocalCheckout: checkout}, ref, options)
		if err != nil {
			return result, err
		}
	}
	if input.snapshot != nil {
		options.LocalBundle = input.snapshot.BundlePath
	}
	remote, err := read("remote", "get-url", "origin")
	if err != nil {
		if input.snapshot != nil && gitExitCode(err, 2) {
			remotes, listErr := read("remote")
			if listErr == nil && !slices.Contains(strings.Fields(remotes), "origin") {
				return result, &apierr.Error{Code: apierr.InvalidArgs, Message: "uploaded source needs a credential-free hosted clone URL as origin", Hint: "add your repository's hosted clone URL: git remote add origin https://HOST/OWNER/REPO.git; then retry"}
			}
		}
		return result, err
	}
	repository, err := triggerRepository(ctx, remote, input.githubID)
	if err != nil {
		return result, err
	}
	git := runspec.Git{Repository: repository, SHA: sha, Ref: input.ref}
	if input.ref == "" {
		git.Ref, err = read("symbolic-ref", "--quiet", "--short", "HEAD")
		if err != nil && !gitExitCode(err, 1) {
			return result, err
		}
	}
	branch, err := read("symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
	if err != nil && !gitExitCode(err, 1) {
		return result, err
	}
	if strings.HasPrefix(branch, "refs/remotes/origin/") {
		git.DefaultBranch = runspec.Optional[string]{Set: true, Value: strings.TrimPrefix(branch, "refs/remotes/origin/")}
	}
	selected := runspec.Source{Location: runspec.SourceLocation{RemoteURL: remote, PipelineDir: ".sparkwing"}}
	git, selected, cfg, err := source.Select(ctx, source.Spec{RepoKey: string(repository.Key), LocalCheckout: checkout}, input.pipeline, git, selected, input.pipelineRef, options)
	if err != nil {
		return result, err
	}
	identity, err := clisurface.ResolvePipeline(cfg.Project(), input.pipeline)
	if err != nil {
		return result, err
	}
	selected.Entrypoint = identity.Entrypoint
	profile := ""
	if entry := cfg.Project().Registry().Find(identity.Name); entry != nil {
		profile = entry.Profile
	}
	return preparedTriggerSource{pipeline: identity.Name, project: cfg.Project(), profile: profile, git: git, source: selected, checkout: reader}, nil
}

func (s preparedTriggerSource) checkProfile(profile string) error {
	if s.profile != "" && s.profile != profile {
		return fmt.Errorf("pipeline trigger: pipeline %s is pinned to profile %s; pass --profile %s", s.pipeline, s.profile, s.profile)
	}
	return nil
}

func gitExitCode(err error, code int) bool {
	var exited *exec.ExitError
	return errors.As(err, &exited) && exited.ExitCode() == code
}

func triggerRepository(ctx context.Context, remote string, githubID func(context.Context, string) (int64, error)) (runspec.Repository, error) {
	invalid := &apierr.Error{Code: apierr.InvalidArgs, Message: "trigger source requires a credential-free clone URL"}
	if _, err := sourceurl.ValidateCloneURL(remote); err != nil {
		return runspec.Repository{}, invalid
	}
	parsed := remote
	if !strings.Contains(parsed, "://") {
		before, after, ok := strings.Cut(parsed, ":")
		if !ok {
			return runspec.Repository{}, invalid
		}
		parsed = "ssh://" + before + "/" + after
	}
	u, err := url.Parse(parsed)
	if err != nil {
		return runspec.Repository{}, invalid
	}
	if err := envredact.ValidateSourceReference(parsed); err != nil {
		return runspec.Repository{}, invalid
	}
	input := runspec.RepoInput{RemoteURL: remote}
	if strings.EqualFold(u.Hostname(), "github.com") {
		if githubID == nil {
			return runspec.Repository{}, &apierr.Error{Code: hosterr.Unavailable, Message: "GitHub repository identity lookup is unavailable"}
		}
		id, err := githubID(ctx, remote)
		if err != nil {
			return runspec.Repository{}, err
		}
		input.GitHubRepositoryID = runspec.Optional[int64]{Set: true, Value: id}
		input.Display = strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	}
	return runspec.CanonicalRepository(input)
}

type triggerRequestInput struct {
	home           string
	submittedEnv   map[string][]byte
	idempotencyKey string
	reason         string
	agent          string
	source         preparedTriggerSource
	settings       settings.Inputs
	declarations   *runspec.DeclarationGate
	args           []string
}

func prepareTriggerRequest(input triggerRequestInput) (request controllerwire.CreateRunRequest, resultErr error) {
	masker := secrets.NewMasker()
	for _, value := range input.submittedEnv {
		masker.Register(string(value))
	}
	for _, token := range input.args {
		values := []string{token}
		if _, value, present := strings.Cut(token, "="); present {
			values = append(values, value)
		}
		for _, value := range values {
			masker.Register(value)
			quoted := fmt.Sprintf("%q", value)
			masker.Register(quoted[1 : len(quoted)-1])
		}
	}
	defer func() {
		if resultErr != nil {
			resultErr = &maskedCLIError{err: resultErr, masker: masker}
		}
	}()
	if input.declarations == nil {
		return controllerwire.CreateRunRequest{}, errors.New("trigger preparation requires pipeline declarations")
	}
	pipeline := input.source.pipeline
	schema, err := boundPipelineSchema(input.declarations.Declarations().Pipelines, clisurface.PipelineIdentity{Name: pipeline, Entrypoint: input.source.source.Entrypoint})
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	args, err := parsePipelineArgs(schema, input.args)
	if err != nil {
		return controllerwire.CreateRunRequest{}, input.declarations.Error(&apierr.Error{Code: apierr.InvalidArgs, Message: err.Error()})
	}
	input.settings.Context.Origin = runspec.OriginTrigger
	options, err := settings.ResolveRequest(input.settings)
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	assignments := make([]string, 0, len(input.submittedEnv))
	for name, value := range input.submittedEnv {
		assignments = append(assignments, name+"="+string(value))
	}
	env, err := identifyCapturedEnvironment(input.home, assignments)
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	if err := validateRequestedPipelineArgs(input.declarations, clisurface.PipelineIdentity{Name: pipeline, Entrypoint: input.source.source.Entrypoint}, input.source.project, args); err != nil {
		return controllerwire.CreateRunRequest{}, input.declarations.Error(err)
	}
	secretArgs := []string{}
	for _, argument := range schema.Args {
		if _, present := args[argument.Name]; argument.Secret && present {
			secretArgs = append(secretArgs, argument.Name)
		}
	}
	key := input.idempotencyKey
	if key == "" {
		key = rand.Text()
	}
	return controllerwire.CreateRunRequest{IdempotencyKey: key, SubmittedEnv: input.submittedEnv, Request: runspec.Request{
		Version: 1, Pipeline: pipeline, Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Reason: input.reason, Agent: input.agent,
		Git: input.source.git, Source: input.source.source, Options: options, Env: env, EnvironmentSource: runspec.EnvironmentSubmitted,
		RequestedArgs: args, SecretArgs: runspec.Optional[[]string]{Set: true, Value: secretArgs},
	}}, nil
}
