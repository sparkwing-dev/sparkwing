//go:build unix

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/quicktest"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

type inspectionEngines struct {
	resolve func(engineid.Pin, releaseasset.Target) (engine.Resolved, error)
	ensure  func(engineid.Identity, releaseasset.Target) (engine.Resolved, error)
}

func (e inspectionEngines) Resolve(_ context.Context, pin engineid.Pin, target releaseasset.Target) (engine.Resolved, error) {
	return e.resolve(pin, target)
}

func (e inspectionEngines) Ensure(_ context.Context, id engineid.Identity, target releaseasset.Target) (engine.Resolved, error) {
	return e.ensure(id, target)
}

func inspectionSourceRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := initSnapshotRepo(t)
	dir := filepath.Join(repo, ".sparkwing")
	writeRepoFile(t, filepath.Join(dir, "go.mod"), "module fixture\n\ngo 1.26\n\nrequire github.com/sparkwing-dev/sparkwing/sparkwing v1.2.3\n")
	writeRepoFile(t, filepath.Join(dir, "sparkwing.yaml"), "pipelines:\n  - name: demo\n    entrypoint: demo\n")
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-qm", "selected Go source")
	sha := strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD"))
	if err := os.Remove(filepath.Join(dir, "go.mod")); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, filepath.Join(dir, "package.json"), `{"dependencies":{"@sparkwing/sdk":"9.9.9"}}`)
	writeRepoFile(t, filepath.Join(dir, "sparkwing.yaml"), "pipelines:\n  - name: demo\n    entrypoint: demo\n    source: "+sha+"\n")
	return dir, sha
}

func TestInspectionAcquiresSelectedCodeAndEngine(t *testing.T) {
	dir, _ := inspectionSourceRepo(t)
	bin, requests := fakePlanSDK(t, fixtureDescribe, fixturePlan, fixtureEval)
	hello := filepath.Join(filepath.Dir(bin), "hello")
	writeRepoFile(t, hello, `"ok":true,"result":{"protocol":2,"sdk":{"language":"go","version":"v1.2.3"}}}`)
	home := t.TempDir()
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	pin := engineid.Pin{SDK: engineid.GoSDK, Version: "v1.2.3"}
	selected := engine.Resolved{Identity: engineid.Identity{Pin: pin, Version: "v1.2.3", ManifestDigest: "verified-selected"}, Target: target, Executable: bin}
	var operations []string
	options := source.Options{
		Target: target, Workspace: filepath.Join(home, "workspaces"), SDK: engineid.TypeScriptSDK,
		Engines: inspectionEngines{
			resolve: func(got engineid.Pin, gotTarget releaseasset.Target) (engine.Resolved, error) {
				if got.SDK != pin.SDK || got.Version != pin.Version || gotTarget != target {
					t.Fatalf("engine selected from invoking code: %+v %+v", got, gotTarget)
				}
				selected.Identity.Pin = got
				operations = append(operations, "resolve")
				return selected, nil
			},
			ensure: func(id engineid.Identity, gotTarget releaseasset.Target) (engine.Resolved, error) {
				if id != selected.Identity || gotTarget != target {
					t.Fatal("inspection changed selected engine")
				}
				operations = append(operations, "ensure")
				return selected, nil
			},
		},
		InstallSDK: func(_ context.Context, got engine.Resolved, request engine.SDKInstallRequest) (engine.SDKCommand, error) {
			if got.Identity != selected.Identity || request.SDK != engineid.GoSDK {
				t.Fatal("installer used invoking engine or SDK")
			}
			pipelineDir, err := request.PipelineDirectory()
			if err != nil {
				return engine.SDKCommand{}, err
			}
			if body, err := os.ReadFile(filepath.Join(pipelineDir, "go.mod")); err != nil || !strings.Contains(string(body), "v1.2.3") {
				t.Fatal("installer did not receive selected source")
			}
			operations = append(operations, "install")
			return engine.SDKCommand{Directory: pipelineDir, Command: []string{bin}}, nil
		},
	}
	err := withLocalPipelineEntry(t.Context(), home, dir, "demo", runFlags{}, os.Environ(), options, func(ctx context.Context, entry pipelineInspection, execution string) error {
		schema, err := entry.presentation(ctx)
		if err != nil {
			return err
		}
		if schema.Name != "demo" {
			t.Fatal("describe lost alias")
		}
		_, err = planPipelineEntry(ctx, entry, "demo", []string{"--env=prod"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(operations[:2], []string{"resolve", "install"}) || !slices.Contains(operations, "ensure") {
		t.Fatalf("engine verification order: %v", operations)
	}
	if raw := readRequests(t, requests); !strings.Contains(raw, `"op":"plan"`) || strings.Contains(raw, `"op":"eval"`) {
		t.Fatalf("inspection operations: %s", raw)
	}
}

func TestInspectionPolicyRefusalPreventsInstallationProperty(t *testing.T) {
	dir, _ := inspectionSourceRepo(t)
	home := t.TempDir()
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	property := func(reason uint64) bool {
		denied := fmt.Errorf("selected engine revoked: %d", reason)
		var pin engineid.Pin
		options := source.Options{Workspace: filepath.Join(home, "workspaces"), Target: target, Engines: inspectionEngines{
			resolve: func(got engineid.Pin, _ releaseasset.Target) (engine.Resolved, error) {
				pin = got
				return engine.Resolved{}, denied
			},
			ensure: func(engineid.Identity, releaseasset.Target) (engine.Resolved, error) {
				t.Fatal("ensured revoked engine")
				return engine.Resolved{}, nil
			},
		}, InstallSDK: func(context.Context, engine.Resolved, engine.SDKInstallRequest) (engine.SDKCommand, error) {
			t.Fatal("installed revoked code")
			return engine.SDKCommand{}, nil
		}}
		err := withLocalPipelineEntry(t.Context(), home, dir, "demo", runFlags{}, os.Environ(), options, func(context.Context, pipelineInspection, string) error { t.Fatal("executed revoked code"); return nil })
		return errors.Is(err, denied) && pin.SDK == engineid.GoSDK && pin.Version == "v1.2.3"
	}
	if err := quick.Check(property, quicktest.Config(&quick.Config{MaxCount: 16})); err != nil {
		t.Fatal(err)
	}
}

func gitContextRepo(t *testing.T) (repo, dir, sha string) {
	t.Helper()
	repo = initSnapshotRepo(t)
	runSnapshotGit(t, repo, "symbolic-ref", "HEAD", "refs/heads/main")
	dir = filepath.Join(repo, ".sparkwing")
	writeRepoFile(t, filepath.Join(dir, "go.mod"), "module fixture\n\ngo 1.26\n\nrequire github.com/sparkwing-dev/sparkwing/sparkwing v1.2.3\n")
	writeRepoFile(t, filepath.Join(dir, "sparkwing.yaml"), "pipelines:\n  - name: demo\n    entrypoint: demo\n")
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-qm", "pipeline")
	runSnapshotGit(t, repo, "remote", "add", "origin", "https://example.invalid/acme/widgets.git")
	runSnapshotGit(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	runSnapshotGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return repo, dir, strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD"))
}

func gitContextSDK(t *testing.T, home string) (options source.Options, requests, processes string) {
	t.Helper()
	bin, requests := fakePlanSDK(t, fixtureDescribe, fixturePlan, fixtureEval)
	writeRepoFile(t, filepath.Join(filepath.Dir(bin), "hello"), `"ok":true,"result":{"protocol":2,"sdk":{"language":"go","version":"v1.2.3"}}}`)
	processes = filepath.Join(filepath.Dir(bin), "processes")
	logged := filepath.Join(filepath.Dir(bin), "logged")
	script := "#!/bin/sh\nprintf '%s|%s|%s|%s\\n' \"$SPARKWING_BRANCH\" \"$SPARKWING_COMMIT\" \"$SPARKWING_REPO\" \"$SPARKWING_TRIGGER_SOURCE\" >> " + processes + "\nexec " + bin + " \"$@\"\n"
	if err := os.WriteFile(logged, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	selected := engine.Resolved{Identity: engineid.Identity{Version: "v1.2.3", ManifestDigest: "fixture"}, Target: target, Executable: logged}
	options = source.Options{
		Target: target, Workspace: filepath.Join(home, "workspaces"),
		Engines: inspectionEngines{
			resolve: func(pin engineid.Pin, _ releaseasset.Target) (engine.Resolved, error) {
				selected.Identity.Pin = pin
				return selected, nil
			},
			ensure: func(engineid.Identity, releaseasset.Target) (engine.Resolved, error) { return selected, nil },
		},
		InstallSDK: func(_ context.Context, _ engine.Resolved, request engine.SDKInstallRequest) (engine.SDKCommand, error) {
			pipelineDir, err := request.PipelineDirectory()
			return engine.SDKCommand{Directory: pipelineDir, Command: []string{logged}}, err
		},
	}
	return options, requests, processes
}

func processLines(t *testing.T, path string) []string {
	t.Helper()
	return strings.Split(strings.TrimSuffix(readRequests(t, path), "\n"), "\n")
}

func TestPlanPreviewReceivesTheCheckoutGitContext(t *testing.T) {
	repo, dir, sha := gitContextRepo(t)
	home := t.TempDir()
	planRun := func(t *testing.T) nodewire.RunContext {
		t.Helper()
		options, requests, _ := gitContextSDK(t, home)
		err := withLocalPipelineEntry(t.Context(), home, dir, "demo", runFlags{}, os.Environ(), options, func(ctx context.Context, entry pipelineInspection, _ string) error {
			_, err := planPipelineEntry(ctx, entry, "demo", []string{"--env=prod"})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		for line := range strings.Lines(readRequests(t, requests)) {
			var request nodewire.Request
			if err := json.Unmarshal([]byte(line), &request); err != nil {
				t.Fatal(err)
			}
			if request.Op == nodewire.OpPlan && request.Run != nil {
				return *request.Run
			}
		}
		t.Fatal("SDK received no plan request")
		return nodewire.RunContext{}
	}
	for _, tc := range []struct{ name, checkout, branch string }{
		{name: "default branch", checkout: "main", branch: "main"},
		{name: "feature branch", checkout: "feature", branch: "feature"},
		{name: "detached HEAD", checkout: "--detach", branch: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.checkout == "feature" {
				runSnapshotGit(t, repo, "checkout", "-qB", "feature")
			} else {
				runSnapshotGit(t, repo, "checkout", "-q", tc.checkout)
			}
			run := planRun(t)
			want := nodewire.Git{SHA: sha, Branch: tc.branch, DefaultBranch: "main", Repo: repo}
			if run.Git == nil || *run.Git != want {
				t.Fatalf("plan preview git context = %+v, want %+v", run.Git, want)
			}
			if run.Trigger == nil || run.Trigger.Source != "local" {
				t.Fatalf("plan preview trigger = %+v, want local", run.Trigger)
			}
		})
	}
}

func TestDiscoveryRunsWithoutGitContext(t *testing.T) {
	_, dir, sha := gitContextRepo(t)
	home := t.TempDir()
	options, _, processes := gitContextSDK(t, home)
	var described int
	err := withLocalPipelineEntry(t.Context(), home, dir, "demo", runFlags{}, os.Environ(), options, func(ctx context.Context, entry pipelineInspection, _ string) error {
		if _, err := entry.presentation(ctx); err != nil {
			return err
		}
		described = len(processLines(t, processes))
		_, err := planPipelineEntry(ctx, entry, "demo", []string{"--env=prod"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := processLines(t, processes)
	if described == 0 || len(lines) <= described+1 {
		t.Fatalf("SDK processes: show %d, total %v", described, lines)
	}
	for i, line := range lines[:described+1] {
		if line != "|||" {
			t.Fatalf("discovery process %d saw git variables %q", i, line)
		}
	}
	if last := lines[len(lines)-1]; !strings.Contains(last, "|"+sha+"|") || !strings.HasSuffix(last, "|local") {
		t.Fatalf("plan process git variables = %q, want commit %s from a local trigger", last, sha)
	}
}
