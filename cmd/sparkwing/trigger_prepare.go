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
	"path"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/envredact"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

type triggerSourceInput struct {
	checkout, ref, pipeline, pipelineRef string
	environment                          []string
	githubID                             func(context.Context, string) (int64, error)
	buildEnv                             func(procenv.Kind, procenv.Base, procenv.Runtime) ([]string, error)
}

type preparedTriggerSource struct {
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

func (s preparedTriggerSource) sdk(ctx context.Context) (engine.SDKKind, error) {
	manifest := path.Join(s.source.Location.PipelineDir, "package.json")
	listed, err := s.checkout.read(ctx, "ls-tree", "--name-only", s.source.Ref, "--", manifest)
	if err != nil || listed == "" {
		return engine.GoSDK, err
	}
	raw, err := s.checkout.read(ctx, "show", s.source.Ref+":"+manifest)
	if err != nil {
		return "", err
	}
	typescript, err := pipelineentry.IsTypeScriptManifest([]byte(raw))
	if err != nil {
		return "", fmt.Errorf("selected pipeline package.json: %w", err)
	}
	if typescript {
		return engine.TypeScriptSDK, nil
	}
	return engine.GoSDK, nil
}

func (c triggerCheckout) read(ctx context.Context, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "credential.helper=", "-C", c.directory}, args...)...)
	cmd.Env = append(append([]string{}, c.environment...), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_LFS_SKIP_SMUDGE=1", "GIT_TERMINAL_PROMPT=0")
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
			return &apierr.Error{Code: apierr.InvalidArgs, Message: "selected source commit is not on origin", Hint: "push both selected commits or trigger with --working-tree"}
		}
	}
	return nil
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
	reader := triggerCheckout{directory: checkout, environment: env}
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
	pipelineSHA, err := triggerPipelineRevision(ctx, reader, cfg, input.pipeline, input.pipelineRef, sha)
	if err != nil {
		return result, err
	}
	profile := ""
	if cfg != nil {
		if entry := cfg.Registry().Find(identity.Name); entry != nil {
			profile = entry.Profile
		}
	}
	return preparedTriggerSource{pipeline: identity.Name, profile: profile, git: git, source: runspec.Source{
		Ref: pipelineSHA, Entrypoint: identity.Entrypoint,
		Location: runspec.SourceLocation{RemoteURL: remote, PipelineDir: ".sparkwing"},
	}, checkout: reader}, nil
}

func (s preparedTriggerSource) checkProfile(profile string) error {
	if s.profile != "" && s.profile != profile {
		return fmt.Errorf("pipeline trigger: pipeline %s is pinned to profile %s; pass --profile %s", s.pipeline, s.profile, s.profile)
	}
	return nil
}

func triggerPipelineRevision(ctx context.Context, checkout triggerCheckout, cfg *projectconfig.Config, pipeline, explicit, execution string) (string, error) {
	declared := ""
	if cfg != nil {
		for _, entry := range cfg.Pipelines {
			if entry.Name == pipeline && entry.Source != nil {
				declared = strings.TrimSpace(*entry.Source)
			}
		}
	}
	explicit = strings.TrimSpace(explicit)
	ref := declared
	if ref == "" {
		ref = explicit
	}
	if ref == "" {
		return execution, nil
	}
	sha, err := checkout.read(ctx, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	if declared != "" && explicit != "" && explicit != declared {
		override, err := checkout.read(ctx, "rev-parse", "--verify", "--end-of-options", explicit+"^{commit}")
		if err != nil {
			return "", err
		}
		if override != sha {
			return "", &apierr.Error{Code: apierr.InvalidArgs, Message: "pipeline revision disagrees with the configured source pin"}
		}
	}
	return sha, nil
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
