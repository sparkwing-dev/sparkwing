package acceptance

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

// SettingsBoundaryScenarios identifies in-process W6 proofs; transaction and process drivers remain separate.
func SettingsBoundaryScenarios() []string {
	return []string{"environment-setting-precedence", "environment-owner-queue-approval-limits", "environment-author-limit-boundaries", "lifecycle-request-and-plan-seals", "lifecycle-retry-sealed-lineage", "storage-repository-identity", "credentials-secret-display-classification", "environment-origin-head-and-guards", "environment-no-progress-versus-execution-timeout"}
}

// CheckSettingsBoundary exercises public settings and run-spec contracts without claiming store or SDK coverage.
func CheckSettingsBoundary(id string) error {
	switch id {
	case "environment-origin-head-and-guards":
		return defaultBranchSeals()
	case "environment-no-progress-versus-execution-timeout":
		return independentJobTimeouts()
	case "environment-setting-precedence", "lifecycle-request-and-plan-seals":
		return settingSeals()
	case "environment-owner-queue-approval-limits":
		return independentRunBounds()
	case "environment-author-limit-boundaries":
		return authorBounds()
	case "lifecycle-retry-sealed-lineage":
		return retrySeals()
	case "storage-repository-identity":
		return repositoryKeys()
	case "credentials-secret-display-classification":
		return classifiedDisplay()
	default:
		return fmt.Errorf("no settings boundary proof for %q", id)
	}
}

func boundaryPointer[T any](value T) *T { return &value }

func acceptedSettings() (runspec.Spec, json.RawMessage, error) {
	options, err := settings.ResolveRequest(settings.Inputs{Context: settings.Context{Submitter: true, Local: true, NumCPU: 4}, Flags: settings.Values{Priority: boundaryPointer(0), DryRun: boundaryPointer(false)}})
	if err != nil {
		return runspec.Spec{}, nil, err
	}
	request, err := runspec.SealRequest(runspec.Request{Version: 1, Team: "team", Pipeline: "build", Mode: runspec.IntakeExecute, Origin: runspec.OriginApp, Dispatch: runspec.DispatchHost, Actor: runspec.Actor{ID: "original"}, Options: options, RequestedArgs: map[string]string{"mode": "test", "secret": "hidden-value"}, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:42"}, SHA: "sha-a", Ref: "refs/heads/trunk", App: runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{RepositoryID: 42, DefaultBranch: "trunk", DefaultBranchPush: true, SHA: "sha-a"}}}}, runspec.IntakeFacts{RunID: "run-a", AdmittedAt: time.Unix(100, 0)})
	if err != nil {
		return runspec.Spec{}, nil, err
	}
	declarations, err := settings.ProjectCheckoutDeclarations([]byte("defaults:\n  requires: [linux]\n  args: {mode: project}\n"), "sparkwing.yaml", "build")
	if err != nil {
		return runspec.Spec{}, nil, err
	}
	plan := nodeproto.PlanDoc{Priority: boundaryPointer(8), Jobs: []nodeproto.JobSpec{{ID: "build", Requires: []string{"go"}}, {ID: "test", Needs: []string{"build"}}}}
	winners, err := settings.ResolvePlan(settings.PlanInputs{Request: request, Declarations: declarations, Plan: plan, Context: settings.Context{Local: true}})
	if err != nil {
		return runspec.Spec{}, nil, err
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return runspec.Spec{}, nil, err
	}
	spec, err := runspec.SealSpec(runspec.SealInput{Request: request, Declarations: declarations, Resolved: runspec.Resolved{Args: map[string]string{"mode": "test", "secret": "hidden-value"}, SecretArgs: []string{"secret"}, Settings: winners}, Plan: raw, Now: time.Unix(200, 0)})
	return spec, raw, err
}

func settingSeals() error {
	spec, _, err := acceptedSettings()
	if err != nil {
		return err
	}
	if spec.Resolved.Settings.Options.Priority.Value != 0 || spec.Resolved.Settings.Options.Priority.Origin.Layer != runspec.LayerFlag || len(spec.Resolved.Settings.Jobs) != 2 {
		return errors.New("request winner was lost at acceptance")
	}
	if !reflect.DeepEqual(spec.Resolved.Settings.Jobs["build"].Requires.Value, []string{"linux", "go"}) {
		return errors.New("defaults without pipeline entry or job requirements were lost")
	}
	if replay, err := runspec.CheckSeal(spec, spec); err != nil || !replay {
		return errors.New("identical seal replay refused")
	}
	changed := spec
	changed.Resolved.Args = map[string]string{"mode": "changed", "secret": "hidden-value"}
	changed.Hash, err = runspec.CanonicalHash(changed)
	if err != nil {
		return err
	}
	if _, err := runspec.CheckSeal(spec, changed); err == nil {
		return errors.New("changed sealed args accepted")
	}
	generated, err := settings.ResolveExpansion(settings.ExpansionInputs{Spec: spec, Plan: nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "generated", Requires: []string{"docker"}}}}, Context: settings.Context{Local: true}})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(generated["generated"].Requires.Value, []string{"linux", "docker"}) {
		return errors.New("expansion changed captured checkout requirements")
	}
	return nil
}

func independentRunBounds() error {
	for _, hours := range []int{3, 6, 12} {
		in := settings.Inputs{Context: settings.Context{Submitter: true}, Operator: settings.OperatorInputs{Flags: settings.OperatorValues{OwnerExecution: boundaryPointer(time.Duration(hours) * time.Hour)}}}
		owner, _, err := settings.OwnerExecution(in)
		if err != nil || owner != time.Duration(hours)*time.Hour {
			return errors.New("operator owner deadline was clamped or ignored")
		}
		queue, _, err := settings.ClaimWait(in)
		if err != nil || queue != 24*time.Hour {
			return errors.New("queue limit coupled to owner timeout")
		}
		in.Code.JobTimeout = boundaryPointer(owner + time.Second)
		if _, _, err := settings.JobTimeout(in); err == nil {
			return errors.New("excess author timeout accepted")
		}
		in.Context.Local = true
		owner, _, err = settings.OwnerExecution(in)
		if err != nil || owner != 0 {
			return errors.New("local owner acquired execution deadline")
		}
		wait, _, err := settings.ApprovalWait(in)
		if err != nil || wait != 0 {
			return errors.New("default approval wait is no longer indefinite")
		}
	}
	return nil
}

func authorBounds() error {
	in := settings.Inputs{Context: settings.Context{Submitter: true}, Code: settings.Values{Retry: boundaryPointer(10), MemoTTL: boundaryPointer(30 * 24 * time.Hour)}, Flags: settings.Values{CatchUp: boundaryPointer(24 * time.Hour)}}
	if value, _, err := settings.Retry(in); err != nil || value != 10 {
		return errors.New("retry boundary refused or clamped")
	}
	if value, _, err := settings.MemoTTL(in); err != nil || value != 30*24*time.Hour {
		return errors.New("memo boundary refused or clamped")
	}
	if value, _, err := settings.CatchUp(in); err != nil || value != 24*time.Hour {
		return errors.New("catch-up boundary refused or clamped")
	}
	in.Code.Retry = boundaryPointer(11)
	in.Code.MemoTTL = boundaryPointer(30*24*time.Hour + time.Nanosecond)
	in.Flags.CatchUp = boundaryPointer(24*time.Hour + time.Nanosecond)
	if _, _, err := settings.Retry(in); err == nil {
		return errors.New("excess retry accepted")
	}
	if _, _, err := settings.MemoTTL(in); err == nil {
		return errors.New("excess memo TTL accepted")
	}
	if _, _, err := settings.CatchUp(in); err == nil {
		return errors.New("excess catch-up accepted")
	}
	return nil
}

func retrySeals() error {
	spec, plan, err := acceptedSettings()
	if err != nil {
		return err
	}
	retry, err := runspec.CarryRetry(runspec.RetryInput{Request: spec.Request, Spec: runspec.Optional[runspec.Spec]{Set: true, Value: spec}, Plan: plan, SourceRunID: "run-a", Retrier: runspec.Actor{ID: "retrier"}})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(retry.Request, spec.Request) || !reflect.DeepEqual(retry.Spec.Value, spec) || string(retry.Plan) != string(plan) || retry.Lineage.Retrier.Value.ID != "retrier" || retry.Request.Request.Actor.ID != "original" || retry.Request.Request.Git.SHA != "sha-a" || !retry.Request.Request.Git.App.Value.DefaultBranchPush {
		return errors.New("retry changed sealed facts or lost retrier lineage")
	}
	return nil
}

func repositoryKeys() error {
	for _, item := range []struct {
		input runspec.RepoInput
		want  runspec.RepoKey
	}{{runspec.RepoInput{GitHubRepositoryID: runspec.Optional[int64]{Set: true, Value: 42}, RemoteURL: "https://github.com/Renamed/Repo"}, "gh:42"}, {runspec.RepoInput{RemoteURL: "https://user:password@EXAMPLE.com:8443/Org/Repo.git"}, "git:example.com:8443/Org/Repo"}, {runspec.RepoInput{RemoteURL: "git@GitLab.com:Org/Repo.git"}, "git:gitlab.com/org/repo"}} {
		repo, err := runspec.CanonicalRepository(item.input)
		if err != nil {
			return err
		}
		if repo.Key != item.want || strings.Contains(repo.Display, "password") {
			return errors.New("repository canonical identity or safe display differs")
		}
	}
	local, err := runspec.CanonicalRepository(runspec.RepoInput{AbsolutePath: "/work/a/../repo"})
	if err != nil {
		return err
	}
	clean, err := runspec.CanonicalRepository(runspec.RepoInput{AbsolutePath: "/work/repo"})
	if err != nil {
		return err
	}
	if local.Key != clean.Key || runspec.ValidateRepoUse(local.Key, runspec.RepoControllerMemo) == nil || runspec.ValidateRepoUse(local.Key, runspec.RepoAccessRule) == nil {
		return errors.New("local identity escaped machine scope")
	}
	return nil
}

func classifiedDisplay() error {
	spec, plan, err := acceptedSettings()
	if err != nil {
		return err
	}
	pending := runspec.DisplayRequest(spec.Request)
	if pending.RequestedArgs["mode"] != "***" || pending.RequestedArgs["secret"] != "***" {
		return errors.New("unclassified request exposed values")
	}
	display := runspec.DisplaySpec(spec)
	if display.Args["mode"] != "test" || display.Args["secret"] != "***" {
		return errors.New("classified display lost public value or exposed secret")
	}
	raw, err := json.Marshal(display)
	if err != nil {
		return err
	}
	if strings.Contains(string(raw), "hidden-value") || strings.Contains(string(raw), "inputs_hash") {
		return errors.New("display leaked restricted inputs")
	}
	request := spec.Request.Request
	request.SecretArgs = runspec.Optional[[]string]{Set: true, Value: []string{"secret"}}
	sealed, err := runspec.SealRequest(request, runspec.IntakeFacts{RunID: "classified"})
	if err != nil {
		return err
	}
	resolved := spec.Resolved
	resolved.SecretArgs = []string{}
	if _, err := runspec.SealSpec(runspec.SealInput{Request: sealed, Resolved: resolved, Plan: plan}); err == nil {
		return errors.New("second seal discarded intake secret classification")
	}
	request.RequestedArgs = map[string]string{"mode": "test"}
	request.SecretArgs = runspec.Optional[[]string]{Set: true, Value: []string{}}
	sealed, err = runspec.SealRequest(request, runspec.IntakeFacts{RunID: "public"})
	if err != nil {
		return err
	}
	resolved.Args = map[string]string{"mode": "test"}
	public, err := runspec.SealSpec(runspec.SealInput{Request: sealed, Resolved: resolved, Plan: plan})
	if err != nil {
		return err
	}
	if runspec.DisplaySpec(public).Args["mode"] != "test" {
		return errors.New("explicit empty classification treated as absent")
	}
	spec.Resolved.SecretArgs = nil
	if runspec.DisplaySpec(spec).Args["mode"] != "***" {
		return errors.New("absent classification failed open")
	}
	return nil
}
