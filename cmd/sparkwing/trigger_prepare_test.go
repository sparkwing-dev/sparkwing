package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

func triggerPreparationRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := initSnapshotRepo(t)
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: deploy-prod\n    entrypoint: Deploy\n", 0o600)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "--quiet", "-m", "pipeline")
	sha := strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD"))
	runSnapshotGit(t, repo, "tag", "release")
	runSnapshotGit(t, repo, "remote", "add", "origin", "https://github.com/acme/app.git")
	return repo, sha
}

func triggerPreparationInput(repo string) triggerSourceInput {
	return triggerSourceInput{
		checkout: repo, pipeline: "deploy-prod", environment: os.Environ(),
		githubID: func(context.Context, string) (int64, error) { return 17, nil },
		buildEnv: func(kind procenv.Kind, base procenv.Base, runtime procenv.Runtime) ([]string, error) {
			if kind != procenv.Git || !base.Local || len(base.Captured) == 0 || !reflect.DeepEqual(runtime, procenv.Runtime{}) {
				panic("invalid Git environment projection")
			}
			return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + repo}, nil
		},
	}
}

func TestPrepareTriggerSourceUsesTheSelectedCommit(t *testing.T) {
	repo, sha := triggerPreparationRepo(t)
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: deploy-prod\n    entrypoint: Later\n", 0o600)
	runSnapshotGit(t, repo, "commit", "--quiet", "-am", "later")
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: deploy-prod\n    entrypoint: Dirty\n", 0o600)
	before := runSnapshotGit(t, repo, "status", "--porcelain=v1")
	input := triggerPreparationInput(repo)
	input.checkout = filepath.Join(repo, ".sparkwing")
	input.ref = "release"
	input.environment = append(input.environment, "GIT_DIR=/missing", "GIT_WORK_TREE=/missing", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=remote.origin.url", "GIT_CONFIG_VALUE_0=https://github.com/forged/repo.git")
	input.githubID = func(_ context.Context, remote string) (int64, error) {
		if remote != "https://github.com/acme/app.git" {
			t.Fatalf("remote changed: %s", remote)
		}
		return 17, nil
	}
	got, err := prepareTriggerSource(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if got.git.SHA != sha || got.git.Ref != "release" || got.git.Repository.Key != "gh:17" || got.git.App.Set {
		t.Fatalf("source identity changed: %+v", got.git)
	}
	if got.source.Entrypoint != "Deploy" || got.source.Ref != sha || got.source.Location.RemoteURL != "https://github.com/acme/app.git" || got.source.Location.LocalCheckout != "" || got.source.Location.PipelineDir != ".sparkwing" {
		t.Fatalf("selected source changed: %+v", got.source)
	}
	if after := runSnapshotGit(t, repo, "status", "--porcelain=v1"); after != before {
		t.Fatalf("discovery modified checkout: before=%q after=%q", before, after)
	}
}

func TestPrepareTriggerSourceDefaultBranchIsUnverified(t *testing.T) {
	repo, sha := triggerPreparationRepo(t)
	runSnapshotGit(t, repo, "update-ref", "refs/remotes/origin/main", sha)
	runSnapshotGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	runSnapshotGit(t, repo, "checkout", "--quiet", "--detach", sha)
	got, err := prepareTriggerSource(t.Context(), triggerPreparationInput(repo))
	if err != nil {
		t.Fatal(err)
	}
	if got.git.Ref != "" || !got.git.DefaultBranch.Set || got.git.DefaultBranch.Value != "main" || got.git.App.Set {
		t.Fatalf("origin HEAD became App evidence: %+v", got.git)
	}
}

func TestPrepareTriggerSourceSeparatesExecutionAndPipelineRevisions(t *testing.T) {
	repo, pipelineSHA := triggerPreparationRepo(t)
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: deploy-prod\n    entrypoint: Deploy\n    source: release\n", 0o600)
	runSnapshotGit(t, repo, "commit", "--quiet", "-am", "execution configuration")
	executionSHA := strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD"))
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: deploy-prod\n    entrypoint: Dirty\n    source: absent\n", 0o600)
	before := runSnapshotGit(t, repo, "status", "--porcelain=v1")
	for _, pipelineRef := range []string{"", "release", pipelineSHA} {
		input := triggerPreparationInput(repo)
		input.pipelineRef = pipelineRef
		got, err := prepareTriggerSource(t.Context(), input)
		if err != nil {
			t.Fatal(err)
		}
		if got.git.SHA != executionSHA || got.source.Ref != pipelineSHA || got.pipeline != "deploy-prod" || got.source.Entrypoint != "Deploy" {
			t.Fatalf("revision identities changed: %+v", got)
		}
	}
	input := triggerPreparationInput(repo)
	input.pipelineRef = executionSHA
	if _, err := prepareTriggerSource(t.Context(), input); err == nil {
		t.Fatal("pipeline revision bypassed the configured source pin")
	}
	if after := runSnapshotGit(t, repo, "status", "--porcelain=v1"); after != before {
		t.Fatalf("revision selection changed checkout: before=%q after=%q", before, after)
	}
}

func TestPrepareTriggerSourceExplicitPipelineRevisionKeepsExecutionBinding(t *testing.T) {
	repo, pipelineSHA := triggerPreparationRepo(t)
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: deploy-prod\n    entrypoint: ReleaseDeploy\n", 0o600)
	runSnapshotGit(t, repo, "commit", "--quiet", "-am", "execution binding")
	executionSHA := strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD"))
	input := triggerPreparationInput(repo)
	input.pipelineRef = "release"
	got, err := prepareTriggerSource(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if got.git.SHA != executionSHA || got.source.Ref != pipelineSHA || got.source.Entrypoint != "ReleaseDeploy" {
		t.Fatalf("pipeline checkout replaced execution binding: %+v", got)
	}
	input.ref, input.pipelineRef = "release", "HEAD"
	got, err = prepareTriggerSource(t.Context(), input)
	if err != nil || got.git.SHA != pipelineSHA || got.source.Ref != executionSHA || got.source.Entrypoint != "Deploy" {
		t.Fatalf("execution ref changed pipeline binding: %+v %v", got, err)
	}
	for _, invalid := range []string{"absent", "--output=/tmp/forged"} {
		input.pipelineRef = invalid
		if _, err := prepareTriggerSource(t.Context(), input); err == nil {
			t.Fatal("invalid pipeline revision was accepted")
		}
	}
}

func TestPreparedTriggerSourceRequiresBothCommitsOnOrigin(t *testing.T) {
	repo, baseSHA := triggerPreparationRepo(t)
	runSnapshotGit(t, repo, "checkout", "--quiet", "-b", "pipeline")
	writeSnapshotFile(t, repo, "pipeline.txt", "pipeline checkout", 0o600)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "--quiet", "-m", "pipeline source")
	pipelineSHA := strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD"))
	runSnapshotGit(t, repo, "tag", "--force", "release", pipelineSHA)
	runSnapshotGit(t, repo, "checkout", "--quiet", "--detach", baseSHA)
	writeSnapshotFile(t, repo, "execution.txt", "execution checkout", 0o600)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "--quiet", "-m", "execution")
	executionSHA := strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD"))
	input := triggerPreparationInput(repo)
	input.pipelineRef = "release"
	got, err := prepareTriggerSource(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"neither", "pipeline only", "execution only", "other remote", "both"} {
		switch mode {
		case "pipeline only":
			runSnapshotGit(t, repo, "update-ref", "refs/remotes/origin/pipeline", pipelineSHA)
		case "execution only":
			runSnapshotGit(t, repo, "update-ref", "-d", "refs/remotes/origin/pipeline")
			runSnapshotGit(t, repo, "update-ref", "refs/remotes/origin/main", executionSHA)
		case "other remote":
			runSnapshotGit(t, repo, "update-ref", "refs/remotes/upstream/pipeline", pipelineSHA)
		case "both":
			runSnapshotGit(t, repo, "update-ref", "refs/remotes/origin/pipeline", pipelineSHA)
		}
		err := got.checkReachable(t.Context())
		if (err == nil) != (mode == "both") {
			t.Fatalf("%s: reachability = %v", mode, err)
		}
		if err != nil {
			var canonical *apierr.Error
			if !errors.As(err, &canonical) || !strings.Contains(canonical.Hint, "--working-tree") {
				t.Fatalf("missing source recovery guidance: %v", err)
			}
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := got.checkReachable(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("reachability cancellation lost: %v", err)
	}
}

func TestPrepareTriggerSourceRejectsUnresolvedIdentityAndRefs(t *testing.T) {
	repo, _ := triggerPreparationRepo(t)
	for _, mode := range []string{"missing resolver", "lookup failure", "invalid ID", "missing ref", "option ref", "missing pipeline"} {
		t.Run(mode, func(t *testing.T) {
			input := triggerPreparationInput(repo)
			switch mode {
			case "missing resolver":
				input.githubID = nil
			case "lookup failure":
				input.githubID = func(context.Context, string) (int64, error) { return 0, errors.New("lookup failed") }
			case "invalid ID":
				input.githubID = func(context.Context, string) (int64, error) { return 0, nil }
			case "missing ref":
				input.ref = "absent"
			case "option ref":
				input.ref = "--output=/tmp/forged"
			case "missing pipeline":
				input.pipeline = ""
			}
			if _, err := prepareTriggerSource(t.Context(), input); err == nil {
				t.Fatal("invalid preparation succeeded")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := prepareTriggerSource(ctx, triggerPreparationInput(repo)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestPrepareTriggerSourceUsesGenericRepositoryIdentity(t *testing.T) {
	repo, _ := triggerPreparationRepo(t)
	runSnapshotGit(t, repo, "remote", "set-url", "origin", "ssh://git@gitlab.com/Acme/App.git")
	input := triggerPreparationInput(repo)
	input.githubID = func(context.Context, string) (int64, error) {
		t.Fatal("non-GitHub identity used GitHub lookup")
		return 0, nil
	}
	got, err := prepareTriggerSource(t.Context(), input)
	if err != nil || got.git.Repository.Key != "git:gitlab.com/acme/app" {
		t.Fatalf("generic repository: %+v %v", got, err)
	}
	runSnapshotGit(t, repo, "remote", "remove", "origin")
	if _, err := prepareTriggerSource(t.Context(), input); err == nil {
		t.Fatal("remote intake accepted no source URL")
	}
}

func TestPrepareTriggerRequestBindsAliasArgumentsAndIntake(t *testing.T) {
	repo, sha := triggerPreparationRepo(t)
	selection, err := prepareTriggerSource(t.Context(), triggerPreparationInput(repo))
	if err != nil {
		t.Fatal(err)
	}
	password := "private-password"
	inputs := settings.Inputs{Context: settings.Context{NumCPU: 4}}
	input := triggerRequestInput{
		source: selection, settings: inputs,
		args: []string{"-r", "us-east", "--image", "release", "--password", password},
		described: []nodeproto.PipelineDescription{
			{Name: "Deploy", Args: []nodeproto.ArgumentDescription{{Name: "unrelated", Type: "bool"}}},
			{Name: "deploy-prod", Args: []nodeproto.ArgumentDescription{
				{Name: "region", Short: "r", Type: "string", Required: true},
				{Name: "image", Type: "string", JobID: "rollout", Required: true},
				{Name: "password", Type: "string", Secret: true},
			}},
		},
	}
	request, err := prepareTriggerRequest(input)
	if err != nil {
		t.Fatal(err)
	}
	if request.IdempotencyKey == "" || request.Request.Pipeline != "deploy-prod" || request.Request.Source.Entrypoint != "Deploy" || request.Request.Git.SHA != sha || request.Request.Mode != runspec.IntakeExecute || request.Request.Origin != runspec.OriginTrigger || request.Request.Dispatch != runspec.DispatchHost {
		t.Fatalf("prepared intake changed: %+v", request)
	}
	if request.Request.Team != "" || request.Request.Actor.ID != "" || request.Request.Git.App.Set {
		t.Fatal("preparation manufactured authority")
	}
	if !reflect.DeepEqual(request.Request.RequestedArgs, map[string]string{"region": "us-east", "image": "release", "password": password}) || !request.Request.SecretArgs.Set || !reflect.DeepEqual(request.Request.SecretArgs.Value, []string{"password"}) {
		t.Fatalf("selected schema lost: %+v", request.Request)
	}
	_, err = prepareTriggerRequest(triggerRequestInput{source: selection, described: input.described[:1]})
	if err == nil || !strings.Contains(err.Error(), "deploy-prod") {
		t.Fatalf("missing registration used Go type name: %v", err)
	}
	input.args = []string{"--typo", "value"}
	_, err = prepareTriggerRequest(input)
	var canonical *apierr.Error
	if !errors.As(err, &canonical) || canonical.Code != apierr.InvalidArgs {
		t.Fatalf("argument failure: %v", err)
	}
}

func TestPrepareTriggerRequestSubmitsThroughTheFinalController(t *testing.T) {
	repo, _ := triggerPreparationRepo(t)
	selection, err := prepareTriggerSource(t.Context(), triggerPreparationInput(repo))
	if err != nil {
		t.Fatal(err)
	}
	request, err := prepareTriggerRequest(triggerRequestInput{source: selection, settings: settings.Inputs{Context: settings.Context{NumCPU: 4}}, described: []nodeproto.PipelineDescription{{Name: "deploy-prod"}}, idempotencyKey: "stable-key"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	err = submitTriggeredRun(t.Context(), request, true, "json", &strings.Builder{}, triggerSubmission{Controller: initialTriggerController{create: func(_ context.Context, got controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
		calls++
		if !reflect.DeepEqual(got, request) {
			t.Fatal("prepared request changed before intake")
		}
		got.Request.Team = "server-team"
		return initialTriggerResponse(t, got), nil
	}}})
	if err != nil || calls != 1 {
		t.Fatalf("prepared submission: calls=%d %v", calls, err)
	}
}

func TestPrepareTriggerSourcePropagatesEnvironmentFailure(t *testing.T) {
	failed := errors.New("environment failed")
	input := triggerSourceInput{checkout: "/missing", pipeline: "deploy-prod", buildEnv: func(kind procenv.Kind, base procenv.Base, runtime procenv.Runtime) ([]string, error) {
		if kind != procenv.Git || !base.Local || len(base.Ambient) != 0 || !reflect.DeepEqual(runtime, procenv.Runtime{}) {
			t.Fatal("discovery changed the Git process boundary")
		}
		return nil, failed
	}}
	if _, err := prepareTriggerSource(t.Context(), input); !errors.Is(err, failed) {
		t.Fatalf("environment failure lost: %v", err)
	}
}

func TestPrepareTriggerRequestRetainsSharedSettings(t *testing.T) {
	priority := 0
	output := "plain"
	runner := runspec.RunnerSelection{Kind: runspec.RunnerLabel, Value: "desktop"}
	request, err := prepareTriggerRequest(triggerRequestInput{
		idempotencyKey: "retained",
		source:         preparedTriggerSource{pipeline: "deploy-prod", source: runspec.Source{Entrypoint: "Deploy"}},
		described:      []nodeproto.PipelineDescription{{Name: "deploy-prod"}},
		settings:       settings.Inputs{Flags: settings.Values{Priority: &priority, Output: &output, RunnerSelection: &runner}, Context: settings.Context{NumCPU: 4}},
		env:            map[string]string{"DEPLOY_REGION": "private-region"},
	})
	if err != nil {
		t.Fatal(err)
	}
	options := request.Request.Options
	if request.IdempotencyKey != "retained" || !options.Priority.Set || options.Priority.Value != 0 || !options.Output.Set || options.Output.Value != output || !options.Runner.Set || options.Runner.Value != runner {
		t.Fatalf("settings changed: %+v", options)
	}
	if !reflect.DeepEqual(request.Request.Env.Names, []string{"DEPLOY_REGION"}) || request.Request.Env.Digest == "" {
		t.Fatalf("environment identity lost: %+v", request.Request.Env)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-region") {
		t.Fatal("private environment value entered intake")
	}
}

func TestPrepareTriggerSourceReadsOnlyCommittedConfiguration(t *testing.T) {
	repo, _ := triggerPreparationRepo(t)
	runSnapshotGit(t, repo, "rm", "--quiet", ".sparkwing/sparkwing.yaml")
	runSnapshotGit(t, repo, "commit", "--quiet", "-m", "no configuration")
	input := triggerPreparationInput(repo)
	got, err := prepareTriggerSource(t.Context(), input)
	if err != nil || got.source.Entrypoint != input.pipeline {
		t.Fatalf("unconfigured binding: %+v %v", got, err)
	}
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: deploy-prod\n    entrypoint: Deploy\n    typo: private-config-value\n", 0o600)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "--quiet", "-m", "invalid configuration")
	if _, err := prepareTriggerSource(t.Context(), input); err == nil || strings.Contains(err.Error(), "private-config-value") {
		t.Fatalf("invalid committed configuration: %v", err)
	}
}

func FuzzTriggerEnvironmentValuesStayPrivate(f *testing.F) {
	f.Add("production")
	f.Add("引数\"'/@:&=\n")
	f.Fuzz(func(t *testing.T, value string) {
		value = "private-" + value
		request, err := prepareTriggerRequest(triggerRequestInput{
			source:    preparedTriggerSource{pipeline: "deploy-prod", source: runspec.Source{Entrypoint: "Deploy"}},
			described: []nodeproto.PipelineDescription{{Name: "deploy-prod"}},
			settings:  settings.Inputs{Context: settings.Context{NumCPU: 4}}, env: map[string]string{"DEPLOY_REGION": value},
		})
		if strings.ContainsRune(value, 0) {
			if err == nil || strings.Contains(err.Error(), value) {
				t.Fatal("invalid private environment was accepted or disclosed")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), value) {
			t.Fatal("private environment escaped intake projection")
		}
		want, err := runspec.EnvironmentDigest(map[string]string{"DEPLOY_REGION": value})
		if err != nil || !reflect.DeepEqual(request.Request.Env, want) {
			t.Fatal("private environment identity changed")
		}
	})
}

func FuzzTriggerPreparationDiagnosticsExcludeArguments(f *testing.F) {
	f.Add("a-secret", false)
	f.Add("引数\"'/@:&=\n", true)
	f.Add("000000=a", true)
	f.Add("a\x00b", false)
	f.Fuzz(func(t *testing.T, value string, equals bool) {
		if !utf8.ValidString(value) {
			t.Skip()
		}
		value = "private-" + value
		args := []string{"--password", value, value}
		if equals {
			args = []string{"--password=" + value, "--" + value}
		}
		_, err := prepareTriggerRequest(triggerRequestInput{
			source:    preparedTriggerSource{pipeline: "deploy-prod", source: runspec.Source{Entrypoint: "Deploy"}},
			described: []nodeproto.PipelineDescription{{Name: "deploy-prod", Args: []nodeproto.ArgumentDescription{{Name: "password", Type: "string", Secret: true}}}},
			args:      args,
		})
		var canonical *apierr.Error
		if !errors.As(err, &canonical) || (canonical.Code != apierr.InvalidArgs && canonical.Code != apierr.Internal) {
			t.Fatalf("error changed: %v", err)
		}
		raw, marshalErr := json.Marshal(canonical)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		quoted := strconv.Quote(value)
		if strings.Contains(err.Error()+string(raw), value) || strings.Contains(canonical.Message, quoted[1:len(quoted)-1]) {
			t.Fatal("private argument escaped preparation failure")
		}
	})
}

func TestPrepareTriggerRequestMasksQuotedEnumValues(t *testing.T) {
	_, err := prepareTriggerRequest(triggerRequestInput{
		source:    preparedTriggerSource{pipeline: "deploy-prod", source: runspec.Source{Entrypoint: "Deploy"}},
		described: []nodeproto.PipelineDescription{{Name: "deploy-prod", Args: []nodeproto.ArgumentDescription{{Name: "region", Type: "string", Enum: []string{"us-east"}}}}},
		args:      []string{"--region=private-\x00value"},
	})
	if err == nil || strings.Contains(err.Error(), `private-\x00value`) || !strings.Contains(err.Error(), "--region") {
		t.Fatalf("enum diagnostic changed or disclosed input: %v", err)
	}
}

func FuzzTriggerRepositoryCredentialsNeverReachIntake(f *testing.F) {
	f.Add("private-value", false)
	f.Add("引数\"'/@:&=", true)
	f.Fuzz(func(t *testing.T, value string, query bool) {
		value = "private-" + value
		remote := "https://" + url.UserPassword("submitter", value).String() + "@github.com/acme/app.git"
		if query {
			remote = "https://github.com/acme/app.git?access_token=" + url.QueryEscape(value)
		}
		for _, remote := range []string{remote, "https://github.com/acme/app.git#access_token=" + url.QueryEscape(value), "https://github.com/acme/app.git?access_token=%zz" + url.QueryEscape(value)} {
			called := false
			_, err := triggerRepository(t.Context(), remote, func(context.Context, string) (int64, error) { called = true; return 17, nil })
			if err == nil || called {
				t.Fatal("credential-bearing source reached identity resolution")
			}
			var canonical *apierr.Error
			if errors.As(err, &canonical) {
				raw, marshalErr := json.Marshal(canonical)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				if strings.Contains(string(raw), value) {
					t.Fatal("source credential escaped diagnostic")
				}
			}
			if strings.Contains(err.Error(), value) {
				t.Fatal("source credential escaped diagnostic")
			}
		}
	})
}
