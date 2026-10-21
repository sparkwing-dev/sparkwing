package runspec

import (
	"errors"
	"maps"

	"github.com/sparkwing-dev/sparkwing/internal/envredact"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// SealRequest snapshots authorized intake values without resolving their precedence.
func SealRequest(request Request, facts IntakeFacts) (SealedRequest, error) {
	if request.Burst != nil {
		if err := request.Burst.Validate(); err != nil {
			return SealedRequest{}, err
		}
	}
	if request.Version != 1 {
		return SealedRequest{}, errors.New("unsupported request version")
	}
	if request.Team == "" || request.Pipeline == "" || facts.RunID == "" {
		return SealedRequest{}, errors.New("request requires team, pipeline and run identity")
	}
	if request.Mode != IntakeExecute && request.Mode != IntakePublish {
		return SealedRequest{}, errors.New("invalid intake mode")
	}
	if request.Dispatch != DispatchHost && request.Dispatch != DispatchController {
		return SealedRequest{}, errors.New("invalid dispatch")
	}
	if request.Mode == IntakeExecute && (!IsCommitID(request.Git.SHA) || !IsCommitID(request.Source.Ref)) {
		return SealedRequest{}, &apierr.Error{Code: apierr.InvalidArgs, Message: "executable intake requires resolved execution and code commits"}
	}
	for _, ref := range []string{request.Source.RequestedRef, request.Source.Ref, request.Source.Location.RemoteURL} {
		if err := envredact.ValidateSourceReference(ref); err != nil {
			return SealedRequest{}, err
		}
	}
	if app := request.Git.App; app.Set && app.Value.DefaultBranch != "" {
		if branch := request.Git.DefaultBranch; branch.Set && branch.Value != app.Value.DefaultBranch {
			return SealedRequest{}, errors.New("request default branch differs from App facts")
		}
		request.Git.DefaultBranch = Optional[string]{Set: true, Value: app.Value.DefaultBranch}
	}
	if err := ValidateRepoUse(request.Git.Repository.Key, RepoMachineStore); err != nil {
		return SealedRequest{}, err
	}
	sealed, err := clone(SealedRequest{Version: 1, Request: request, RunID: facts.RunID, AdmittedAt: facts.AdmittedAt.UTC(), AllowedPools: facts.AllowedPools, AuthorityRef: facts.AuthorityRef, Ceilings: facts.Ceilings, Provenance: facts.Provenance})
	if err != nil {
		return SealedRequest{}, err
	}
	sealed.Hash, err = CanonicalHash(sealed)
	return sealed, err
}

func verifyRequest(r SealedRequest) error {
	if r.Version != 1 || r.Request.Version != 1 || r.Hash == "" {
		return errors.New("invalid request seal")
	}
	h, err := CanonicalHash(r)
	if err != nil {
		return err
	}
	if h != r.Hash {
		return drift()
	}
	return nil
}

// SealSpec binds the already validated plan and resolved settings to the original request.
func SealSpec(input SealInput) (Spec, error) {
	if err := verifyRequest(input.Request); err != nil {
		return Spec{}, err
	}
	request, resolved := input.Request.Request.Options, input.Resolved.Settings.Options
	sameSelection := func(want, got Setting[string]) bool {
		if want.Set {
			return want == got
		}
		return got.Value == ""
	}
	if !sameSelection(request.StartAt, resolved.StartAt) || !sameSelection(request.StopAt, resolved.StopAt) || !sameSelection(request.Only, resolved.Only) {
		return Spec{}, errors.New("resolved step window differs from sealed intake")
	}
	if !validResolvedClassification(input.Request.Request, input.Resolved) {
		return Spec{}, errors.New("resolved secret argument classification is missing or invalid")
	}
	if branch := input.Request.Request.Git.DefaultBranch; branch.Set && input.Resolved.DefaultBranch.Set && branch.Value != input.Resolved.DefaultBranch.Value {
		return Spec{}, errors.New("resolved default branch differs from sealed intake")
	}
	if input.Resolved.DefaultBranchSource == DefaultBranchApp {
		app := input.Request.Request.Git.App
		if !app.Set || !input.Resolved.DefaultBranch.Set || input.Resolved.DefaultBranch.Value != app.Value.DefaultBranch {
			return Spec{}, errors.New("default branch does not match sealed App facts")
		}
	} else if input.Resolved.DefaultBranchSource != "" && input.Resolved.DefaultBranchSource != DefaultBranchOriginHEAD {
		return Spec{}, errors.New("invalid default branch source")
	}
	planHash, err := CanonicalHash(input.Plan)
	if err != nil {
		return Spec{}, err
	}
	spec, err := clone(Spec{Version: 1, Declarations: input.Declarations, Request: input.Request, Resolved: input.Resolved, PlanHash: planHash, SealedAt: input.Now.UTC()})
	if err != nil {
		return Spec{}, err
	}
	spec.Hash, err = CanonicalHash(spec)
	return spec, err
}

func verifySpec(s Spec) error {
	if s.Version != 1 || s.Hash == "" || s.PlanHash == "" {
		return errors.New("invalid spec seal")
	}
	if err := verifyRequest(s.Request); err != nil {
		return err
	}
	h, err := CanonicalHash(s)
	if err != nil {
		return err
	}
	if h != s.Hash {
		return drift()
	}
	return nil
}

func drift() error { return &apierr.Error{Code: apierr.SpecDrift, Message: "sealed run inputs differ"} }

// CheckSeal verifies bytes as well as claimed hashes before permitting replay.
func CheckSeal(existing, proposed Spec) (bool, error) {
	if err := verifySpec(existing); err != nil {
		return false, err
	}
	if err := verifySpec(proposed); err != nil {
		return false, err
	}
	if existing.Hash != proposed.Hash {
		return false, drift()
	}
	return true, nil
}

// Project derives every indexed input from its immutable owner.
func Project(s Spec) Projections {
	r := s.Request.Request
	branch := ""
	if s.Resolved.DefaultBranch.Set {
		branch = s.Resolved.DefaultBranch.Value
	} else if r.Git.DefaultBranch.Set {
		branch = r.Git.DefaultBranch.Value
	}
	return Projections{RepoKey: r.Git.Repository.Key, RepoDisplay: r.Git.Repository.Display, Pipeline: r.Pipeline, SHA: r.Git.SHA, Ref: r.Git.Ref, DefaultBranch: branch, Dispatch: r.Dispatch, AllowedPools: append([]string(nil), s.Request.AllowedPools...), RequestHash: s.Request.Hash, SpecHash: s.Hash, PlanHash: s.PlanHash}
}

// CarryRetry keeps the source envelope immutable; destination lineage is separate.
func CarryRetry(input RetryInput) (RetrySnapshot, error) {
	if err := verifyRequest(input.Request); err != nil {
		return RetrySnapshot{}, err
	}
	if input.SourceRunID == "" {
		return RetrySnapshot{}, errors.New("retry requires source run identity")
	}
	if input.Spec.Set {
		if err := verifySpec(input.Spec.Value); err != nil {
			return RetrySnapshot{}, err
		}
		if input.Spec.Value.Request.Hash != input.Request.Hash {
			return RetrySnapshot{}, drift()
		}
		hash, err := CanonicalHash(input.Plan)
		if err != nil {
			return RetrySnapshot{}, err
		}
		if hash != input.Spec.Value.PlanHash {
			return RetrySnapshot{}, drift()
		}
	} else if len(input.Plan) != 0 {
		return RetrySnapshot{}, errors.New("retry plan requires an accepted spec")
	}
	lineage := input.Request.Request.Lineage
	lineage.RetryOf = input.SourceRunID
	lineage.Retrier = Optional[Actor]{Set: true, Value: input.Retrier}
	snapshot, err := clone(RetrySnapshot{Request: input.Request, Spec: input.Spec, Lineage: lineage})
	if err != nil {
		return RetrySnapshot{}, err
	}
	snapshot.Plan = append([]byte(nil), input.Plan...)
	return snapshot, nil
}

// RunContext uses sealed winners and returns a detached copy for executable consumers.
func (s Spec) RunContext() Context {
	r := s.Request.Request
	git := r.Git
	if s.Resolved.DefaultBranch.Set {
		git.DefaultBranch = s.Resolved.DefaultBranch
	}
	source := r.Source
	if s.Resolved.Entrypoint != "" {
		source.Entrypoint = s.Resolved.Entrypoint
	}
	context := Context{Args: s.Resolved.Args, Git: git, Actor: r.Actor, PullRequest: r.PullRequest, Source: source, Lineage: r.Lineage, DryRun: s.Resolved.Settings.Options.DryRun.Set && s.Resolved.Settings.Options.DryRun.Value}
	context.Args = maps.Clone(context.Args)
	return context
}

// GuardContext retains provenance; origin-HEAD discovery never adds App verification.
func (s Spec) GuardContext() Context { return s.RunContext() }
