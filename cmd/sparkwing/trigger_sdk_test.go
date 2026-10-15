package main

import (
	"os"
	"path/filepath"
	"testing"

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
			got, err := selected.sdk(t.Context())
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
		got, err := selected.sdk(t.Context())
		want := engine.GoSDK
		if typescript {
			want = engine.TypeScriptSDK
		}
		if err != nil || got != want {
			t.Fatalf("working tree changed SDK selection to %q, %v; want %q", got, err, want)
		}
	})
}
