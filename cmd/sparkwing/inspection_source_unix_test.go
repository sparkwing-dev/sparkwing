//go:build unix

package main

import (
	"context"
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
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

type inspectionEngines struct {
	resolve func(engine.Pin, releaseasset.Target) (engine.Resolved, error)
	ensure  func(engine.Identity, releaseasset.Target) (engine.Resolved, error)
}

func (e inspectionEngines) Resolve(_ context.Context, pin engine.Pin, target releaseasset.Target) (engine.Resolved, error) {
	return e.resolve(pin, target)
}

func (e inspectionEngines) Ensure(_ context.Context, id engine.Identity, target releaseasset.Target) (engine.Resolved, error) {
	return e.ensure(id, target)
}

func inspectionSourceRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := initSnapshotRepo(t)
	dir := filepath.Join(repo, ".sparkwing")
	writeRepoFile(t, filepath.Join(dir, "go.mod"), "module fixture\n\ngo 1.26\n\nrequire github.com/sparkwing-dev/sparkwing v1.2.3\n")
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
	writeRepoFile(t, hello, `"ok":true,"result":{"protocol":2,"sdk":{"language":"go","version":"v1.2.3"},"features":[]}}`)
	home := t.TempDir()
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	pin := engine.Pin{SDK: engine.GoSDK, Version: "v1.2.3"}
	selected := engine.Resolved{Identity: engine.Identity{Pin: pin, Version: "v1.2.3", ManifestDigest: "verified-selected"}, Target: target, Executable: bin}
	var operations []string
	options := source.Options{
		Target: target, Workspace: filepath.Join(home, "workspaces"), SDK: engine.TypeScriptSDK,
		Engines: inspectionEngines{
			resolve: func(got engine.Pin, gotTarget releaseasset.Target) (engine.Resolved, error) {
				if got.SDK != pin.SDK || got.Version != pin.Version || gotTarget != target {
					t.Fatalf("engine selected from invoking code: %+v %+v", got, gotTarget)
				}
				selected.Identity.Pin = got
				operations = append(operations, "resolve")
				return selected, nil
			},
			ensure: func(id engine.Identity, gotTarget releaseasset.Target) (engine.Resolved, error) {
				if id != selected.Identity || gotTarget != target {
					t.Fatal("inspection changed selected engine")
				}
				operations = append(operations, "ensure")
				return selected, nil
			},
		},
		InstallSDK: func(_ context.Context, got engine.Resolved, request engine.SDKInstallRequest) (engine.SDKCommand, error) {
			if got.Identity != selected.Identity || request.SDK != engine.GoSDK {
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
		var pin engine.Pin
		options := source.Options{Workspace: filepath.Join(home, "workspaces"), Target: target, Engines: inspectionEngines{
			resolve: func(got engine.Pin, _ releaseasset.Target) (engine.Resolved, error) {
				pin = got
				return engine.Resolved{}, denied
			},
			ensure: func(engine.Identity, releaseasset.Target) (engine.Resolved, error) {
				t.Fatal("ensured revoked engine")
				return engine.Resolved{}, nil
			},
		}, InstallSDK: func(context.Context, engine.Resolved, engine.SDKInstallRequest) (engine.SDKCommand, error) {
			t.Fatal("installed revoked code")
			return engine.SDKCommand{}, nil
		}}
		err := withLocalPipelineEntry(t.Context(), home, dir, "demo", runFlags{}, os.Environ(), options, func(context.Context, pipelineInspection, string) error { t.Fatal("executed revoked code"); return nil })
		return errors.Is(err, denied) && pin.SDK == engine.GoSDK && pin.Version == "v1.2.3"
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 16}); err != nil {
		t.Fatal(err)
	}
}
