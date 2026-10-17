package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
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
	selected.source.Location.LocalCheckout = selected.checkout.directory
	selected.source.Location.RemoteURL = ""
	selected.source.CommittedCheckout = true
	spec, err := source.ForRequest(runspec.SealedRequest{Request: runspec.Request{Git: selected.git, Source: selected.source}})
	if err != nil {
		return "", err
	}
	selectedKind := engine.SDKKind("")
	boundary := errors.New("SDK selection reached")
	_, err = source.Acquire(t.Context(), spec, source.Options{Workspace: t.TempDir(), Engines: triggerSDKResolver{}, Target: releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, Base: procenv.Base{Local: true, Captured: os.Environ(), Masker: secrets.NewMasker()}, ReadPin: func(_ string, kind engine.SDKKind) (engine.Pin, error) {
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
