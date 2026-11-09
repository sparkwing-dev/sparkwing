package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestScheduleProofUsesCommittedTypeScriptAndPreservesGoWorkingSource(t *testing.T) {
	for _, tc := range []struct{ name, manifest, file, committed, want string }{
		{"typescript", `{"dependencies":{"@sparkwing/sdk":"0.71.0"}}`, "index.ts", `console.log("committed");`, "committed"},
		{"typescript-dirty-config", `{"dependencies":{"@sparkwing/sdk":"0.71.0"}}`, "index.ts", `console.log("committed");`, "committed"},
		{"go", `{"dependencies":{}}`, "main.go", `package main; func main() { println("committed") }`, "dirty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, home := initSnapshotRepo(t), t.TempDir()
			directory := filepath.Join(root, ".sparkwing")
			writeSnapshotFile(t, root, ".sparkwing/package.json", tc.manifest, 0o600)
			writeSnapshotFile(t, root, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: ci\n    entrypoint: CI\n", 0o600)
			writeSnapshotFile(t, root, ".sparkwing/"+tc.file, tc.committed, 0o600)
			runSnapshotGit(t, root, "add", ".")
			runSnapshotGit(t, root, "commit", "-qm", "committed pipeline")
			if tc.name == "typescript-dirty-config" {
				old := strings.TrimSpace(runSnapshotGit(t, root, "rev-parse", "HEAD"))
				writeSnapshotFile(t, root, ".sparkwing/"+tc.file, tc.committed+" // new HEAD", 0o600)
				runSnapshotGit(t, root, "add", ".")
				runSnapshotGit(t, root, "commit", "-qm", "advance pipeline")
				writeSnapshotFile(t, root, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: ci\n    entrypoint: CI\n    source: "+old+"\n", 0o600)
				tc.want = "new HEAD"
			}
			writeSnapshotFile(t, root, ".sparkwing/"+tc.file, strings.ReplaceAll(tc.committed, "committed", "dirty"), 0o600)
			masker := secrets.NewMasker()
			selected, err := selectLocalRunSource(t.Context(), home, directory, "ci", runFlags{}, os.Environ(), masker)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := selected.execution.Close(); err != nil {
					t.Error(err)
				}
			})
			spec, err := source.ForRequest(runspec.SealedRequest{Request: runspec.Request{Git: selected.git, Source: selected.source}})
			if err != nil {
				t.Fatal(err)
			}
			options := source.Options{Workspace: filepath.Join(home, "workspaces"), GitAccess: source.GitAccessOwner, Base: procenv.Base{Local: true, Captured: os.Environ(), Masker: masker}}
			spec, err = localScheduleProofSource(t.Context(), spec, directory, "ci", options)
			if err != nil {
				t.Fatal(err)
			}
			entry, err := source.PrepareCheckout(t.Context(), spec, options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := entry.Close(); err != nil {
					t.Error(err)
				}
			})
			body, err := os.ReadFile(filepath.Join(entry.Directory, tc.file))
			if err != nil || !strings.Contains(string(body), tc.want) {
				t.Fatalf("prepared source = %s, %v; want %s", body, err, tc.want)
			}
		})
	}
}
