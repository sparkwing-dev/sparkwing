package nodehost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// CheckoutPlanningInput supplies acquired project bytes and immutable intake facts to every planner.
type CheckoutPlanningInput struct {
	Request     runspec.SealedRequest
	Entry       *source.Entry
	Runner      nodeproto.RunnerIdentity
	Project     []byte
	ProjectFile string
	Planning    PlanningInput
}

func (h *OwnerHost) prepare(ctx context.Context, input RunInput, request runspec.SealedRequest, entry *source.Entry) (OwnerPlanning, error) {
	if h.Prepare != nil {
		return h.Prepare(ctx, input, entry)
	}
	if h.Planner == nil {
		return OwnerPlanning{}, errors.New("new run requires checkout resolution and planning")
	}
	project, file, err := readCheckoutProject(ctx, entry.CheckoutDirectory(), filepath.Join(entry.CheckoutDirectory(), request.Request.Source.Location.PipelineDir))
	if err != nil {
		return OwnerPlanning{}, err
	}
	return h.Planner.PrepareCheckout(ctx, CheckoutPlanningInput{Request: request, Entry: entry, Runner: input.GrantedRunner, Project: project, ProjectFile: file, Planning: h.Planning})
}

func readCheckoutProject(ctx context.Context, checkout, directory string) ([]byte, string, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, "", err
	}
	if !filepath.IsAbs(checkout) || !filepath.IsAbs(directory) {
		return nil, "", errors.New("checkout project requires an acquired root and pipeline directory")
	}
	relative, err := filepath.Rel(checkout, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, "", errors.New("pipeline directory escapes the acquired checkout")
	}
	root, err := os.OpenRoot(checkout)
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	for directory := filepath.ToSlash(relative); ; directory = path.Dir(directory) {
		if err := context.Cause(ctx); err != nil {
			return nil, "", err
		}
		file := path.Join(directory, "sparkwing.yaml")
		raw, err := root.ReadFile(file)
		if err == nil {
			return raw, file, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, "", err
		}
		if directory == "." {
			return nil, "", nil
		}
	}
}

// PrepareCheckout resolves checkout arguments and guards before constructing the owner's second seal.
func (p SDKPlanner) PrepareCheckout(ctx context.Context, input CheckoutPlanningInput) (out OwnerPlanning, err error) {
	if p.Masker == nil {
		return out, errors.New("checkout planning requires a shared secret registry")
	}
	defer func() { err = maskHostError(err, p.Masker) }()
	if err := context.Cause(ctx); err != nil {
		return out, err
	}
	if input.Entry == nil || input.Request.RunID == "" || input.Request.Request.Pipeline == "" {
		return out, errors.New("checkout planning requires an acquired entry and sealed request")
	}
	request := input.Request.Request
	if p.Masks == nil {
		p.Masks = NewMaskRegistry(p.Masker)
	}
	if err := p.Masks.initialize(input.Request, nil, nil, nil); err != nil {
		return out, err
	}
	declarations, err := settings.ProjectCheckoutDeclarations(input.Project, input.ProjectFile, request.Pipeline)
	if err != nil {
		return out, err
	}
	inputs, entrypoint, err := checkoutPlanningSettings(input)
	if err != nil {
		return out, err
	}
	args, _, err := settings.Args(inputs)
	if err != nil {
		return out, err
	}
	guards, _, err := settings.Guards(inputs)
	if err != nil {
		return out, err
	}
	resolved := runspec.Resolved{Engine: input.Entry.Engine.Identity, Env: request.Env, Entrypoint: entrypoint, Settings: runspec.ExecutionSettings{Options: request.Options}}
	resolveDefaultBranch(&resolved, request.Git, input.Entry.DefaultBranch)
	process := PlanningProcessInput{Debug: request.Options.Debug.Value, Entry: input.Entry, Entrypoint: entrypoint, Runner: input.Runner, Args: args, SecretArgs: request.SecretArgs.Value, Environment: request.Env, Run: executorRunContext(runspec.Spec{Request: input.Request, Resolved: resolved})}
	prepared, err := p.Prepare(ctx, process, input.Planning)
	if err != nil {
		return out, err
	}
	resolved.Args, resolved.SecretArgs = prepared.Args, prepared.SecretArgs
	resolved.SecretRegistration = prepared.SecretRegistration
	resolved.SecretNames = []string{}
	names := map[string]bool{}
	for _, declaration := range prepared.Description.Secrets {
		if declaration.Name == "" || names[declaration.Name] {
			return out, executorError(apierr.ProtocolError, "SDK describe has invalid secret declarations")
		}
		names[declaration.Name] = true
		resolved.SecretNames = append(resolved.SecretNames, declaration.Name)
	}
	resolved.Guards = checkoutGuardVerdict(guards, request, resolved)
	return OwnerPlanning{
		Declarations: declarations, Resolved: resolved, Planning: prepared.Input, Plan: prepared.Plan,
		Resolve: func(plan nodeproto.PlanDoc) (runspec.Resolved, error) {
			winner := resolved
			settings, err := settings.ResolvePlan(settings.PlanInputs{Request: input.Request, Declarations: declarations, Plan: plan, Context: settings.Context{Local: request.Origin == runspec.OriginLocal}})
			winner.Settings = settings
			return winner, err
		},
	}, nil
}

func checkoutPlanningSettings(input CheckoutPlanningInput) (settings.Inputs, string, error) {
	inputs := settings.Inputs{Sealed: settings.Values{Args: input.Request.Request.RequestedArgs}, Context: settings.Context{Submitter: true}}
	entrypoint := input.Request.Request.Source.Entrypoint
	if entrypoint == "" {
		entrypoint = input.Request.Request.Pipeline
	}
	if len(input.Project) == 0 {
		return inputs, entrypoint, nil
	}
	cfg, err := projectconfig.Parse(input.Project)
	if err != nil {
		return inputs, "", err
	}
	if cfg.Defaults != nil {
		inputs.Defaults = settings.Values{Args: cfg.Defaults.Args, Guards: cfg.Defaults.Guards}
	}
	selection := ResolvePipeline(cfg, input.Request.Request.Pipeline)
	for _, pipeline := range cfg.Pipelines {
		if pipeline.Name == input.Request.Request.Pipeline {
			entrypoint = selection.Entrypoint
			inputs.Pipeline = settings.Values{Args: pipeline.Args, Guards: pipeline.Guards}
			break
		}
	}
	return inputs, entrypoint, nil
}

func resolveDefaultBranch(resolved *runspec.Resolved, git runspec.Git, acquired string) {
	branch, origin := acquired, runspec.DefaultBranchOriginHEAD
	if git.DefaultBranch.Set {
		branch = git.DefaultBranch.Value
	}
	if git.App.Set {
		branch, origin = git.App.Value.DefaultBranch, runspec.DefaultBranchApp
	}
	if branch != "" {
		resolved.DefaultBranch = runspec.Optional[string]{Set: true, Value: branch}
		resolved.DefaultBranchSource = origin
	}
}

func checkoutGuardVerdict(guards projectconfig.Guards, request runspec.Request, resolved runspec.Resolved) runspec.Guards {
	verdict := runspec.Guards{When: guards.When, Unless: guards.Unless, Passed: true}
	for _, token := range append(slices.Clone(guards.When), guards.Unless...) {
		if token == "git:branch=default" && !resolved.DefaultBranch.Set {
			verdict.Passed, verdict.Reason = false, "run guard requires an unresolved default branch"
			return verdict
		}
		if strings.HasPrefix(token, "where:") && request.ExecutionLocation != "local" && request.ExecutionLocation != "controller" {
			verdict.Passed, verdict.Reason = false, "run guard requires a sealed execution location"
			return verdict
		}
	}
	at := pipelines.GuardContext{ProfileIsLocal: request.ExecutionLocation == "local", Args: resolved.Args, GitBranch: request.Git.Ref, GitDefaultBranch: resolved.DefaultBranch.Value}
	for _, group := range []struct {
		field  string
		tokens []string
		reject bool
	}{{"unless", guards.Unless, true}, {"when", guards.When, false}} {
		for i, raw := range group.tokens {
			token, err := pipelines.ParseGuardToken(raw)
			if err != nil || token.Matches(at) == group.reject {
				verdict.Passed, verdict.Reason = false, fmt.Sprintf("run guard %s[%d] refused execution", group.field, i)
				return verdict
			}
		}
	}
	return verdict
}
