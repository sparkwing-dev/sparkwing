package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
)

func TestTriggerSDKUsesPipelineCommit(t *testing.T) {
	for _, manifest := range []string{"", `{"dependencies":{"@sparkwing/sdk":"1.2.3"}}`, `{"devDependencies":{"@sparkwing/sdk":"1.2.3"}}`, "{"} {
		t.Run(manifest, func(t *testing.T) {
			repo, _ := triggerPreparationRepo(t)
			if manifest != "" {
				writeSnapshotFile(t, repo, ".sparkwing/package.json", manifest, 0o600)
				runSnapshotGit(t, repo, "add", ".")
				runSnapshotGit(t, repo, "commit", "--quiet", "-m", "pipeline SDK")
			}
			runSnapshotGit(t, repo, "tag", "pipeline-sdk")
			writeSnapshotFile(t, repo, ".sparkwing/package.json", `{"dependencies":{"@sparkwing/sdk":"9.9.9"}}`, 0o600)
			runSnapshotGit(t, repo, "add", ".")
			runSnapshotGit(t, repo, "commit", "--quiet", "--allow-empty", "-m", "execution SDK")
			writeSnapshotFile(t, repo, ".sparkwing/package.json", "invalid working tree", 0o600)
			input := triggerPreparationInput(repo)
			input.pipelineRef = "pipeline-sdk"
			selected, err := prepareTriggerSource(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			before := runSnapshotGit(t, repo, "status", "--porcelain=v1")
			got, err := acquiredTriggerSDK(t, selected)
			if manifest == "{" {
				if err == nil {
					t.Fatal("malformed selected manifest became a Go pipeline")
				}
			} else {
				want := engine.GoSDK
				if manifest != "" {
					want = engine.TypeScriptSDK
				}
				if err != nil || got != want {
					t.Fatalf("SDK = %q, %v; want %q", got, err, want)
				}
			}
			if after := runSnapshotGit(t, repo, "status", "--porcelain=v1"); after != before {
				t.Fatalf("SDK detection modified checkout: %q -> %q", before, after)
			}
		})
	}
}

func FuzzTriggerSDKIgnoresWorkingTree(f *testing.F) {
	f.Add(`{"dependencies":{"@sparkwing/sdk":"9.9.9"}}`, false)
	f.Add("{", true)
	f.Fuzz(func(t *testing.T, dirty string, typescript bool) {
		repo, _ := triggerPreparationRepo(t)
		if typescript {
			writeSnapshotFile(t, repo, ".sparkwing/package.json", `{"dependencies":{"@sparkwing/sdk":"1.2.3"}}`, 0o600)
			runSnapshotGit(t, repo, "add", ".")
			runSnapshotGit(t, repo, "commit", "--quiet", "-m", "TypeScript")
		}
		selected, err := prepareTriggerSource(t.Context(), triggerPreparationInput(repo))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, ".sparkwing/package.json"), []byte(dirty), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := acquiredTriggerSDK(t, selected)
		want := engine.GoSDK
		if typescript {
			want = engine.TypeScriptSDK
		}
		if err != nil || got != want {
			t.Fatalf("working tree changed SDK selection to %q, %v; want %q", got, err, want)
		}
	})
}

func acquiredTriggerSDK(t *testing.T, selected preparedTriggerSource) (engine.SDKKind, error) {
	t.Helper()
	selectedKind := engine.SDKKind("")
	boundary := errors.New("SDK selection reached")
	_, err := inspectTriggerSource(t.Context(), selected, os.Environ(), source.Options{Workspace: t.TempDir(), Engines: triggerSDKResolver{}, Target: releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, Base: procenv.Base{Local: true, Captured: os.Environ(), Masker: secrets.NewMasker()}, ReadPin: func(_ string, kind engine.SDKKind) (engine.Pin, error) {
		selectedKind = kind
		return engine.Pin{}, boundary
	}})
	if errors.Is(err, boundary) {
		err = nil
	}
	return selectedKind, err
}

type triggerSDKResolver struct{}

func (triggerSDKResolver) Resolve(context.Context, engine.Pin, releaseasset.Target) (engine.Resolved, error) {
	return engine.Resolved{}, errors.New("unexpected resolve")
}

func (triggerSDKResolver) Ensure(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
	return engine.Resolved{}, errors.New("unexpected ensure")
}

func FuzzTriggerInspectionAcquiresOriginalResolvedPair(f *testing.F) {
	f.Add("dirty code", false)
	f.Add("other bytes\n", true)
	f.Fuzz(func(t *testing.T, dirty string, workingTree bool) {
		if len(dirty) > 4096 {
			t.Skip()
		}
		repo, _ := triggerPreparationRepo(t)
		writeSnapshotFile(t, repo, ".sparkwing/payload", "sealed code\n", 0o600)
		runSnapshotGit(t, repo, "add", ".")
		runSnapshotGit(t, repo, "commit", "-qm", "code")
		runSnapshotGit(t, repo, "tag", "code")
		code := strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD"))
		writeSnapshotFile(t, repo, ".sparkwing/payload", "execution code\n", 0o600)
		runSnapshotGit(t, repo, "commit", "-qam", "execution")
		head := strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD"))
		runSnapshotGit(t, repo, "replace", code, head)
		writeSnapshotFile(t, repo, ".sparkwing/payload", fmt.Sprintf("%x", dirty), 0o600)
		input := triggerPreparationInput(repo)
		input.pipelineRef = "code"
		bundle := ""
		if workingTree {
			snapshot, err := captureWorktreeSnapshot(t.Context(), os.Environ(), repo, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := snapshot.close(); err != nil {
					t.Error(err)
				}
			})
			if err := snapshot.includeRevisions(t.Context(), "", code); err != nil {
				t.Fatal(err)
			}
			input.snapshot, bundle, head = snapshot, snapshot.BundlePath, snapshot.SHA
		}
		selected, err := prepareTriggerSource(t.Context(), input)
		if err != nil {
			t.Fatal(err)
		}
		if selected.git.SHA != head || selected.source.Ref != code {
			t.Fatal("selection changed resolved pair")
		}
		before := runSnapshotGit(t, repo, "status", "--porcelain=v1")
		boundary := errors.New("selected code reached engine")
		options := source.Options{Workspace: t.TempDir(), Engines: triggerSDKResolver{}, Target: releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, Base: procenv.Base{Local: true, Captured: os.Environ(), Masker: secrets.NewMasker()}, ReadPin: func(dir string, _ engine.SDKKind) (engine.Pin, error) {
			raw, err := os.ReadFile(filepath.Join(dir, "payload"))
			if err != nil || string(raw) != "sealed code\n" {
				t.Fatalf("inspection substituted selected code: %q %v", raw, err)
			}
			return engine.Pin{}, boundary
		}}
		options.LocalBundle = bundle
		_, err = inspectTriggerSource(t.Context(), selected, os.Environ(), options)
		if !errors.Is(err, boundary) {
			t.Fatalf("original resolved pair did not reach engine: %v", err)
		}
		if after := runSnapshotGit(t, repo, "status", "--porcelain=v1"); after != before {
			t.Fatal("inspection changed original checkout")
		}
	})
}
