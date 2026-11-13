package jobs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTypeChangeRunsAffectedReader(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires file symlink privilege")
	}
	for _, mode := range []string{"main hook", "manual push", "gate committed", "gate staged", "gate unstaged", "merge delta"} {
		t.Run(mode, func(t *testing.T) {
			root := gateFixtureRepo(t)
			if mode == "merge delta" {
				configurePublishedSDKFixture(t, root)
			}
			input := filepath.Join(root, "internal", "input.txt")
			writeGoFile(t, input, "ok")
			writeGoFile(t, filepath.Join(root, "internal", "bad.txt"), "bad")
			writeGoFile(t, filepath.Join(root, "internal", "probe_test.go"), `package internal
import ("os"; "testing")
func TestReader(t *testing.T) { b,e:=os.ReadFile("input.txt"); if e!=nil {t.Fatal(e)}; if string(b)!="ok" {t.Fatal("typechange-reader-failed")} }
`)
			for _, script := range []string{"check-changelog.sh", "check-api-spec.sh", "check-api-snapshot.sh"} {
				writeGoFile(t, filepath.Join(root, "bin", script), "exit 0\n")
			}
			gitCommitAll(t, root, "good regular reader input")
			ctx := newTestContext(t)
			base, err := gitIn(root)(ctx, "rev-parse", "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			runTestGit(t, root, "update-ref", "refs/remotes/origin/main", base)
			if err := os.Remove(input); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("bad.txt", input); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "gate staged":
				runTestGit(t, root, "add", "internal/input.txt")
			case "gate unstaged":
			default:
				gitCommitAll(t, root, "replace regular input with symlink")
			}
			diff, err := gitIn(root)(ctx, "diff", "--name-status", base)
			if err != nil || diff != "T\tinternal/input.txt" {
				t.Fatalf("not a pure type change: %q %v", diff, err)
			}
			switch mode {
			case "main hook":
				head, headErr := gitIn(root)(ctx, "rev-parse", "HEAD")
				if headErr != nil {
					t.Fatal(headErr)
				}
				t.Setenv("SPARKWING_GATE_PUSH_REFS", "HEAD "+head+" refs/heads/main "+base)
				err = runAffectedPushTests(ctx, false)
			case "manual push":
				err = runAffectedPushTests(ctx, false)
			case "merge delta":
				files, listErr := changedBetween(ctx, root, base, "HEAD")
				if listErr != nil {
					t.Fatal(listErr)
				}
				if mkErr := os.MkdirAll(filepath.Join(root, "web", "node_modules"), 0o755); mkErr != nil {
					t.Fatal(mkErr)
				}
				err = recheckMergeDelta(ctx, files)
			default:
				err = runAffectedTest(ctx)
			}
			if err == nil || !strings.Contains(err.Error(), "typechange-reader-failed") {
				t.Fatalf("type-changed reader input escaped %s: %v", mode, err)
			}
		})
	}
}
