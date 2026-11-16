package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeRecheckRejectsQATrustOnlyIntegrationBreak(t *testing.T) {
	isolateTestHome(t)
	r := newLandRemote(t)
	configurePublishedSDKFixture(t, r.dev)
	r.git(r.dev, "add", ".")
	base := map[string]string{
		"internal/api.go":           "package internal\nfunc Value() int { return 1 }\n",
		"bin/check-changelog.sh":    "exit 0\n",
		"bin/check-api-spec.sh":     "exit 0\n",
		"bin/check-api-snapshot.sh": "exit 0\n",
	}
	r.commit(r.dev, base, "Go base")
	r.git(r.dev, "push", "--quiet", "origin", "HEAD:main")
	r.git(r.other, "pull", "--quiet", "--ff-only")
	r.commit(r.dev, map[string]string{"internal/api.go": "package internal\nfunc Value(n int) int { return n }\n"}, "change signature")
	head := r.head()
	moved := r.moveMain(map[string]string{"qaonly/qa.go": "//go:build qatrust\n\npackage qaonly\nimport \"fixture/internal\"\nfunc Call() int { return internal.Value() }\n"})
	t.Chdir(r.dev)
	if err := os.MkdirAll(filepath.Join(r.dev, "web", "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := landOnto(newTestContext(t), r.dev, head, recheckMergeDelta)
	if err == nil || !strings.Contains(err.Error(), "vet-touched") || !strings.Contains(err.Error(), "not enough arguments") {
		t.Fatalf("merge-only qatrust call break escaped recheck: %v", err)
	}
	if got := r.remoteMain(); got != moved {
		t.Fatalf("broken merge reached main: %s", got)
	}
}

func TestMergeRecheckRetainsPushContractChecks(t *testing.T) {
	for _, name := range []string{"changelog", "api-spec", "api-snapshot"} {
		t.Run(name, func(t *testing.T) {
			root := publishedSDKFixture(t)
			for _, check := range []string{"changelog", "api-spec", "api-snapshot"} {
				body := "exit 0\n"
				if check == name {
					body = "echo 'merge contract " + name + " failed' >&2\nexit 1\n"
				}
				script := "check-" + check + ".sh"
				writeGoFile(t, filepath.Join(root, "bin", script), body)
			}
			gitCommitAll(t, root, "base")
			runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
			if err := os.MkdirAll(filepath.Join(root, "web", "node_modules"), 0o755); err != nil {
				t.Fatal(err)
			}
			err := recheckMergeDelta(newTestContext(t), []string{"internal/sound.go"})
			if err == nil || !strings.Contains(err.Error(), "merge contract "+name+" failed") {
				t.Fatalf("merged tree skipped %s contract: %v", name, err)
			}
		})
	}
}
