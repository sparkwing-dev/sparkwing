package jobs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLandAPISnapshotPathsIgnoreScratchAndRejectTrackedDrift(t *testing.T) {
	source := sourceTreeRoot()
	tool := filepath.Join(t.TempDir(), "apidiff")
	build := exec.Command("go", "build", "-o", tool, "./cmd/apidiff")
	build.Dir = source
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build real API snapshot tool: %s: %v", out, err)
	}
	root := publishedSDKFixture(t)
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	writeGoFile(t, filepath.Join(root, "go.mod"), strings.Replace(string(mod), "module fixture", "module github.com/sparkwing-dev/sparkwing", 1))
	writeGoFile(t, filepath.Join(root, "cmd/apidiff/main.go"), fmt.Sprintf("package main\nimport (\"os\";\"os/exec\")\nfunc main(){c:=exec.Command(%q,os.Args[1:]...);c.Stdout=os.Stdout;c.Stderr=os.Stderr;if c.Run()!=nil{os.Exit(1)}}\n", tool))
	// safety: the land paths must not shell out to the removed worktree-walking checker.
	writeGoFile(t, filepath.Join(root, "bin/check-api-snapshot.sh"), "echo bin/check-api-snapshot.sh must not run >&2; exit 1\n")
	for _, name := range []string{"changelog", "api-spec"} {
		writeGoFile(t, filepath.Join(root, "bin/check-"+name+".sh"), "exit 0\n")
	}
	writeGoFile(t, filepath.Join(root, ".gitignore"), ".Codex-scratch/\nweb/node_modules/\n")
	generate := exec.Command(tool, filepath.Join(root, ".apidiff"))
	generate.Dir = root
	if out, err := generate.CombinedOutput(); err != nil {
		t.Fatalf("generate baseline: %s: %v", out, err)
	}
	gitCommitAll(t, root, "API baseline")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	if err := os.MkdirAll(filepath.Join(root, "web/node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	name := "pkg/cachecontrol/.Codex-scratch/bad.go"
	writeGoFile(t, filepath.Join(root, name), "package scratch\nfunc Broken( {\n")
	checks := map[string]func() error{
		"fast": func() error {
			host, plan, _ := prepareWork(t, &PrePush{})
			_, err := host.RunStep(newTestContext(t), plan, "job", "api-snapshot")
			return err
		},
		"merge-recheck": func() error { return recheckMergeDelta(newTestContext(t), []string{"README.md"}) },
	}
	for label, check := range checks {
		t.Run(label+"/ignored", func(t *testing.T) {
			if err := check(); err != nil {
				t.Fatalf("ignored scratch affected land: %v", err)
			}
		})
	}
	writeGoFile(t, filepath.Join(root, name), "package scratch\nfunc Extra() {}\n")
	runTestGit(t, root, "add", "-f", name)
	gitCommitAll(t, root, "tracked API drift")
	for label, check := range checks {
		t.Run(label+"/tracked", func(t *testing.T) {
			if err := check(); err == nil {
				t.Fatal("tracked API drift passed land")
			} else if !strings.Contains(err.Error(), "bash bin/regen-api-snapshot.sh") || !strings.Contains(err.Error(), "stage .apidiff/") || strings.Contains(err.Error(), "sparkwing-policy-files-") {
				t.Fatalf("snapshot failure lost its repair guidance or exposed temporary paths: %v", err)
			}
		})
	}
}
