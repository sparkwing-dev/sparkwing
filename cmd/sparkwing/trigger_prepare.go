package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/envredact"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

type triggerSourceInput struct {
	checkout, ref, pipeline string
	environment             []string
	githubID                func(context.Context, string) (int64, error)
	buildEnv                func(procenv.Kind, procenv.Base, procenv.Runtime) ([]string, error)
}

type preparedTriggerSource struct {
	pipeline string
	git      runspec.Git
	source   runspec.Source
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
	env, err := buildEnv(procenv.Git, procenv.Base{Local: true, Captured: input.environment}, procenv.Runtime{})
	if err != nil {
		return result, err
	}
	read := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", checkout}, args...)...)
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("inspect trigger checkout: %w", errors.Join(ctx.Err(), err))
		}
		return strings.TrimSuffix(string(out), "\n"), nil
	}
	checkout, err = read("rev-parse", "--show-toplevel")
	if err != nil {
		return result, err
	}
	ref := input.ref
	if ref == "" {
		ref = "HEAD"
	}
	sha, err := read("rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return result, err
	}
	remote, err := read("remote", "get-url", "origin")
	if err != nil {
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
	configPath := ".sparkwing/" + projectconfig.Filename
	listed, err := read("ls-tree", "--name-only", sha, "--", configPath)
	if err != nil {
		return result, err
	}
	var cfg *projectconfig.Config
	if listed != "" {
		raw, err := read("show", sha+":"+configPath)
		if err != nil {
			return result, err
		}
		cfg, err = projectconfig.Parse([]byte(raw))
		if err != nil {
			return result, err
		}
	}
	identity, err := clisurface.ResolvePipeline(cfg, input.pipeline)
	if err != nil {
		return result, err
	}
	return preparedTriggerSource{pipeline: identity.Name, git: git, source: runspec.Source{
		Ref: sha, Entrypoint: identity.Entrypoint,
		Location: runspec.SourceLocation{RemoteURL: remote, PipelineDir: ".sparkwing"},
	}}, nil
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
	for _, raw := range []string{u.RawQuery, u.Fragment} {
		values, err := url.ParseQuery(raw)
		if err != nil {
			return runspec.Repository{}, invalid
		}
		for name := range values {
			if envredact.CredentialName(name) {
				return runspec.Repository{}, invalid
			}
		}
	}
	input := runspec.RepoInput{RemoteURL: remote}
	if strings.EqualFold(u.Hostname(), "github.com") {
		if githubID == nil {
			return runspec.Repository{}, &apierr.Error{Code: apierr.Unavailable, Message: "GitHub repository identity lookup is unavailable"}
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
	idempotencyKey string
	source         preparedTriggerSource
	settings       settings.Inputs
	described      []nodeproto.PipelineDescription
	args           []string
	env            map[string]string
}

func prepareTriggerRequest(input triggerRequestInput) (request controllerwire.CreateRunRequest, resultErr error) {
	masker := secrets.NewMasker()
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
	pipeline := input.source.pipeline
	schema, err := boundPipelineSchema(input.described, clisurface.PipelineIdentity{Name: pipeline, Entrypoint: input.source.source.Entrypoint})
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	args, err := parsePipelineArgs(schema, input.args)
	if err != nil {
		return controllerwire.CreateRunRequest{}, &apierr.Error{Code: apierr.InvalidArgs, Message: err.Error()}
	}
	input.settings.Context.Origin = runspec.OriginTrigger
	options, err := settings.ResolveRequest(input.settings)
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	env, err := runspec.EnvironmentDigest(input.env)
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	secretArgs := []string{}
	for _, argument := range schema.Args {
		if argument.Secret {
			secretArgs = append(secretArgs, argument.Name)
		}
	}
	key := input.idempotencyKey
	if key == "" {
		key = rand.Text()
	}
	return controllerwire.CreateRunRequest{IdempotencyKey: key, Request: runspec.Request{
		Version: 1, Pipeline: pipeline, Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost,
		Git: input.source.git, Source: input.source.source, Options: options, Env: env,
		RequestedArgs: args, SecretArgs: runspec.Optional[[]string]{Set: true, Value: secretArgs},
	}}, nil
}
