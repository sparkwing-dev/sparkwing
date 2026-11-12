package jobs

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestQueuedLandRequiresTheCheckedTreeBeforePushing(t *testing.T) {
	isolateTestHome(t)
	r := newLandRemote(t)
	marker := filepath.Join(t.TempDir(), "tested-source")
	r.commit(r.dev, map[string]string{
		"go.mod":                 "module fixture\n\ngo 1.26.0\n",
		".sparkwing/go.mod":      "module fixture-pipelines\n\ngo 1.26.0\n",
		".sparkwing/jobs.go":     "package pipelines\nfunc Sound() {}\n",
		"internal/sound.go":      "package internal\nfunc Sound() int { return 1 }\n",
		"internal/sound_test.go": fmt.Sprintf("package internal\nimport (\"os\"; \"testing\")\nfunc TestMergedSource(t *testing.T) { if Sound()!=1 { t.Fatal(\"wrong source\") }; if err:=os.WriteFile(%q,[]byte(\"pre-merge test body ran\"),0600);err!=nil { t.Fatal(err) } }\n", marker),
	}, "base tests")
	r.git(r.dev, "push", "--quiet", "origin", "HEAD:main")
	r.git(r.other, "pull", "--quiet", "--ff-only")
	submitted := r.commit(r.dev, map[string]string{"branch.txt": "candidate\n"}, "candidate")
	pinned := r.moveMain(map[string]string{"internal/sound.go": "package internal\nfunc Sound() int { return \"merged code does not compile\" }\n"})
	t.Chdir(r.dev)
	ctx := newTestContext(t)
	if err := prepareQueuedLanding(ctx, gitIn(r.dev), r.dev, submitted, pinned); err != nil {
		t.Fatal(err)
	}
	head := r.head()
	if head.Commit == submitted {
		t.Fatal("preconditions did not merge the owner")
	}
	snapshot := t.TempDir()
	runTestGit(t, snapshot, "init", "--quiet")
	runTestGit(t, snapshot, "fetch", "--quiet", "--no-tags", "--depth=1", r.dev, submitted)
	runTestGit(t, snapshot, "checkout", "--quiet", "--detach", "FETCH_HEAD")
	if count := strings.TrimSpace(gitOutput(t, snapshot, "rev-list", "--count", "HEAD")); count != "1" {
		t.Fatal(count)
	}
	if _, err := exec.Command("git", "-C", snapshot, "rev-parse", "--verify", "origin/main").CombinedOutput(); err == nil {
		t.Fatal("probe unexpectedly has origin/main")
	}
	t.Chdir(snapshot)
	host, plan, _ := prepareWork(t, &PrePush{})
	if _, err := host.RunStep(t.Context(), plan, "job", "build-touched"); err != nil {
		t.Fatalf("stale fast build: %v", err)
	}
	t.Setenv("SPARKWING_GATE_PUSH_REFS", "")
	if err := os.Unsetenv("SPARKWING_GATE_PUSH_REFS"); err != nil {
		t.Fatal(err)
	}
	if err := runAffectedPushTests(ctx, true); err != nil {
		t.Fatalf("stale affected tests: %v", err)
	}
	body, err := os.ReadFile(marker)
	if err != nil || string(body) != "pre-merge test body ran" {
		t.Fatalf("stale tests never ran their body: %q %v", body, err)
	}
	control, err := exec.Command("go", "-C", r.dev, "test", "-short", "./internal").CombinedOutput()
	if err == nil || !strings.Contains(string(control), "merged code does not compile") {
		t.Fatalf("merged control was not broken: %s %v", control, err)
	}
	review := reviewRecord{Branch: head.Branch, Commit: head.Commit, Tree: head.Tree, Base: head.Base, Result: resultPass}
	head.Checks = &landCheckRequirements{RunID: "stale-proof", Required: []string{"fast/build-touched", "affected-tests/test"}}
	checkedTree := strings.TrimSpace(gitOutput(t, snapshot, "rev-parse", "HEAD^{tree}"))
	common := strings.TrimSpace(gitOutput(t, r.dev, "rev-parse", "--path-format=absolute", "--git-common-dir"))
	for _, key := range head.Checks.Required {
		data, err := json.Marshal(landCheckReceipt{RunID: head.Checks.RunID, Check: key, Tree: checkedTree, Success: true})
		if err != nil {
			t.Fatal(err)
		}
		writeGoFile(t, filepath.Join(common, "sparkwing-land-checks", head.Checks.RunID, key+".json"), string(data))
	}
	if err := verifyLanding(ctx, gitIn(r.dev), r.dev, head, review); err == nil || !strings.Contains(err.Error(), "checked tree") {
		t.Fatalf("merged tree authorized using stale checks (checked %s, pushing %s): %v", checkedTree, head.Tree, err)
	}
	if r.remoteMain() != pinned {
		t.Fatal("rejected stale checks changed the fixture remote")
	}
}
