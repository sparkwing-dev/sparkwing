package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwingtest"
)

func TestQueuedChecksBindTheOwnerAndRequireEveryReceipt(t *testing.T) {
	owner := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(owner, "AGENTS.md"), "# Fixture\n")
	gitCommitAll(t, owner, "owner tree")
	head := landHead{Branch: strings.TrimSpace(gitOutput(t, owner, "symbolic-ref", "--short", "HEAD")), Commit: strings.TrimSpace(gitOutput(t, owner, "rev-parse", "HEAD")), Tree: strings.TrimSpace(gitOutput(t, owner, "rev-parse", "HEAD^{tree}")), Checks: &landCheckRequirements{RunID: "run-receipt-proof", Required: []string{"fast/probe"}}}
	snapshot := t.TempDir()
	gitInit(t, snapshot)
	writeGoFile(t, filepath.Join(snapshot, "decoy"), "snapshot\n")
	gitCommitAll(t, snapshot, "snapshot")
	t.Chdir(snapshot)
	q := &queuedChecks{Owner: owner, RunID: head.Checks.RunID}
	ctx := newTestContext(t)
	if err := q.run(ctx, "fast/probe", head, func(context.Context) error {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		if cwd != owner {
			return fmt.Errorf("ran in %s, wanted %s", cwd, owner)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	review := reviewRecord{Tree: head.Tree, Result: resultPass}
	if err := verifyLanding(ctx, gitIn(owner), owner, head, review); err != nil {
		t.Fatalf("valid receipt: %v", err)
	}
	common := strings.TrimSpace(gitOutput(t, owner, "rev-parse", "--path-format=absolute", "--git-common-dir"))
	file, err := landCheckPath(common, head.Checks.RunID, "fast/probe")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing", "wrong-tree", "wrong-run", "wrong-check", "failed", "unreadable"} {
		t.Run(mode, func(t *testing.T) {
			receipt := landCheckReceipt{RunID: head.Checks.RunID, Check: "fast/probe", Tree: head.Tree, Success: true}
			switch mode {
			case "wrong-tree":
				receipt.Tree = "another-tree"
			case "wrong-run":
				receipt.RunID = "another-run"
			case "wrong-check":
				receipt.Check = "another/check"
			case "failed":
				receipt.Success = false
			}
			if err := writeLandCheckReceipt(file, receipt); err != nil {
				t.Fatal(err)
			}
			if mode == "missing" {
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unreadable" {
				writeGoFile(t, file, "not JSON")
			}
			err := verifyLanding(ctx, gitIn(owner), owner, head, review)
			if err == nil {
				t.Fatal("invalid authorizing receipt accepted")
			}
			if mode == "missing" || mode == "wrong-tree" {
				if !strings.Contains(err.Error(), queuedCheckRemedy) {
					t.Fatalf("missing queue remedy: %v", err)
				}
			}
			if mode == "wrong-run" || mode == "wrong-check" {
				for _, value := range []string{head.Checks.RunID, "fast/probe", receipt.RunID, receipt.Check} {
					if !strings.Contains(err.Error(), value) {
						t.Fatalf("missing identity %q: %v", value, err)
					}
				}
			}
		})
	}
}

func TestQueuedCheckNamesItsInvalidPreconditions(t *testing.T) {
	q := &queuedChecks{RunID: "executing-run"}
	for _, tc := range []struct {
		checks  *landCheckRequirements
		details []string
	}{
		{nil, []string{"missing", "executing-run"}},
		{&landCheckRequirements{RunID: "preconditions-run"}, []string{"preconditions-run", "executing-run"}},
		{&landCheckRequirements{RunID: q.RunID}, []string{"empty", q.RunID}},
	} {
		err := q.run(t.Context(), "fast/build", landHead{Checks: tc.checks}, func(context.Context) error { t.Fatal("invalid set ran a check"); return nil })
		if err == nil {
			t.Fatal("invalid preconditions accepted")
		}
		for _, detail := range append(tc.details, "fast/build", queuedCheckRemedy) {
			if !strings.Contains(err.Error(), detail) {
				t.Fatalf("missing %q: %v", detail, err)
			}
		}
	}
}

func queuedSourcePlan(t *testing.T, owner, submitted, pinned string, outputs map[string]json.RawMessage) (sparkwingtest.Host, *sparkwing.Plan, pipelineDocument) {
	t.Helper()
	name := fmt.Sprintf("queued-source-%d", testPipelineNumber.Add(1))
	var plan *sparkwing.Plan
	sparkwing.Register(name, func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return testPipeline{build: func(ctx context.Context, p *sparkwing.Plan) error {
			plan = p
			return (&Land{}).Plan(ctx, p, LandArgs{Queued: true, QueuedMain: pinned, ReviewArgs: ReviewArgs{Brief: "Judge merged source"}}, sparkwing.RunContext{RunID: "run-queued-source", Git: sparkwing.NewGit(sparkwing.WorkDir(), submitted, "work", "main", owner, "")})
		}}
	})
	host := newTestHost(t, sparkwingtest.Options{Outputs: outputs})
	raw, err := host.Plan(t.Context(), name, nil)
	if err != nil {
		t.Fatal(err)
	}
	return host, plan, decodePipeline(t, raw)
}

func TestQueuedLandReplanningInASnapshotChecksTheMergedOwner(t *testing.T) {
	isolateTestHome(t)
	r := newLandRemote(t)
	r.commit(r.dev, map[string]string{
		"AGENTS.md":              "# Fixture\n",
		"go.mod":                 "module fixture\n\ngo 1.26.0\n",
		".sparkwing/go.mod":      "module fixture-pipelines\n\ngo 1.26.0\n",
		".sparkwing/jobs.go":     "package pipelines\nfunc Jobs() {}\n",
		"internal/sound.go":      "package internal\nfunc Sound() int {return 1}\n",
		"internal/sound_test.go": "package internal\nimport \"testing\"\nfunc TestSound(t *testing.T){if Sound()!=1 {t.Fatal(\"bad merged source\")}}\n",
	}, "base")
	r.git(r.dev, "push", "--quiet", "origin", "HEAD:main")
	r.git(r.other, "pull", "--quiet", "--ff-only")
	submitted := r.commit(r.dev, map[string]string{"internal/branch.go": "package internal\nfunc Branch() {}\n"}, "candidate")
	pinned := r.moveMain(map[string]string{"internal/sound.go": "package internal\nfunc Sound() int {return \"merged owner compile error\"}\n"})
	t.Chdir(r.dev)
	host, plan, doc := queuedSourcePlan(t, r.dev, submitted, pinned, nil)
	raw, err := host.RunStep(t.Context(), plan, "preconditions", "check")
	if err != nil {
		t.Fatal(err)
	}
	var head landHead
	if err := json.Unmarshal(raw, &head); err != nil {
		t.Fatal(err)
	}
	var expected []string
	for _, job := range doc.Jobs {
		for _, step := range job.Steps {
			switch job.ID {
			case "policy":
				expected = append(expected, "policy/"+step.ID)
			case "review":
				if step.ID == "review" {
					expected = append(expected, "review/review")
				} else if step.ID != "budget" {
					expected = append(expected, "fast/"+step.ID)
				}
			case "affected-tests":
				expected = append(expected, "affected-tests/"+step.ID)
			}
		}
	}
	slices.Sort(expected)
	if head.Checks == nil || head.Checks.RunID != "run-queued-source" || !slices.Equal(head.Checks.Required, expected) {
		t.Fatalf("authorizing check inventory incomplete: %+v, wanted %v", head.Checks, expected)
	}
	snapshot := t.TempDir()
	runTestGit(t, snapshot, "init", "--quiet")
	runTestGit(t, snapshot, "fetch", "--quiet", "--no-tags", "--depth=1", r.dev, submitted)
	runTestGit(t, snapshot, "checkout", "--quiet", "--detach", "FETCH_HEAD")
	for _, which := range []string{"fast", "affected"} {
		t.Run(which, func(t *testing.T) {
			t.Chdir(snapshot)
			h, p, _ := queuedSourcePlan(t, r.dev, submitted, pinned, map[string]json.RawMessage{"preconditions": raw})
			job, step := "review", "build-touched"
			if which == "affected" {
				job, step = "affected-tests", "test"
			}
			_, err := h.RunStep(t.Context(), p, job, step)
			if err == nil || !strings.Contains(err.Error(), "merged owner compile error") {
				t.Fatalf("%s did not judge merged owner: %v", which, err)
			}
			cwd, cwdErr := os.Getwd()
			if cwdErr != nil || cwd != r.dev {
				t.Fatalf("check cwd=%s, expected owner=%s: %v", cwd, r.dev, cwdErr)
			}
		})
	}
	t.Chdir(r.dev)
	if err := verifyLanding(newTestContext(t), gitIn(r.dev), r.dev, head, reviewRecord{Tree: head.Tree, Result: resultPass}); err == nil {
		t.Fatal("failed/missing checks authorized merged push")
	}
	if r.remoteMain() != pinned {
		t.Fatal("rejected check updated the remote")
	}
}

func TestQueuedCheckFailureCannotLeaveASuccessReceipt(t *testing.T) {
	owner := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(owner, "AGENTS.md"), "# Fixture\n")
	gitCommitAll(t, owner, "owner")
	head := landHead{Branch: strings.TrimSpace(gitOutput(t, owner, "symbolic-ref", "--short", "HEAD")), Commit: strings.TrimSpace(gitOutput(t, owner, "rev-parse", "HEAD")), Tree: strings.TrimSpace(gitOutput(t, owner, "rev-parse", "HEAD^{tree}")), Checks: &landCheckRequirements{RunID: "run-failure", Required: []string{"policy/probe"}}}
	q := &queuedChecks{Owner: owner, RunID: head.Checks.RunID}
	ctx := newTestContext(t)
	if err := q.run(ctx, "policy/probe", head, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("authorizing check failed")
	if err := q.run(ctx, "policy/probe", head, func(context.Context) error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err := verifyLanding(ctx, gitIn(owner), owner, head, reviewRecord{Tree: head.Tree, Result: resultPass}); err == nil {
		t.Fatal("earlier success hid failed check")
	}
}

const queuedFixtureRunID = "run-queued-fixture"

// safety: scheduler tests omit execution of preconditions, but their source
// receipt must still bind real committed fixture bytes rather than a fallback.
func prepareQueuedFixturePlan(t *testing.T, build func(context.Context, *sparkwing.Plan) error) (sparkwingtest.Host, *sparkwing.Plan, pipelineDocument) {
	t.Helper()
	root := sparkwing.WorkDir()
	required := []string{"review/review", "affected-tests/test"}
	for _, step := range policySteps {
		required = append(required, "policy/"+step.id)
	}
	for _, id := range stepIDs(t, &PrePush{}) {
		if id != "budget" {
			required = append(required, "fast/"+id)
		}
	}
	slices.Sort(required)
	head := landHead{Branch: strings.TrimSpace(gitOutput(t, root, "symbolic-ref", "--short", "HEAD")), Commit: strings.TrimSpace(gitOutput(t, root, "rev-parse", "HEAD")), Tree: strings.TrimSpace(gitOutput(t, root, "rev-parse", "HEAD^{tree}")), Checks: &landCheckRequirements{RunID: queuedFixtureRunID, Required: required}}
	data, err := json.Marshal(head)
	if err != nil {
		t.Fatal(err)
	}
	return preparePlanWithOptions(t, build, sparkwingtest.Options{Outputs: map[string]json.RawMessage{"preconditions": data}})
}

func TestQueuedLandCompleteReceiptsAuthorizeOnlyTheMergedOwner(t *testing.T) {
	isolateTestHome(t)
	policy, err := os.ReadFile(filepath.Join(sourceTreeRoot(), ".review.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	r := newLandRemote(t)
	r.commit(r.dev, map[string]string{
		"AGENTS.md": "# Fixture\n", ".review.yaml": string(policy),
		"go.mod":                 "module fixture\n\ngo 1.26.0\n",
		".sparkwing/go.mod":      "module fixture-pipelines\n\ngo 1.26.0\n",
		".sparkwing/jobs.go":     "package pipelines\nfunc Jobs() {}\n",
		"internal/sound.go":      "package internal\nfunc Sound() int {return 1}\n",
		"bin/check-changelog.sh": "exit 0\n", "bin/check-api-spec.sh": "exit 0\n", "bin/check-api-snapshot.sh": "exit 0\n",
	}, "base")
	r.git(r.dev, "push", "--quiet", "origin", "HEAD:main")
	r.git(r.other, "pull", "--quiet", "--ff-only")
	r.git(r.dev, "commit", "--quiet", "--allow-empty", "-m", "candidate metadata")
	submitted := r.git(r.dev, "rev-parse", "HEAD")
	pinned := r.moveMain(map[string]string{"internal/sound.go": "package internal\nfunc Sound() int {return 2}\n"})
	oldSteps := policySteps
	t.Cleanup(func() { policySteps = oldSteps })
	policySteps = []policyStep{{"owner-source", func(context.Context) error {
		data, err := os.ReadFile("internal/sound.go")
		if err != nil {
			return err
		}
		if !strings.Contains(string(data), "return 2") {
			return errors.New("policy saw pre-merge bytes")
		}
		return nil
	}}}
	t.Chdir(r.dev)
	host, plan, _ := queuedSourcePlan(t, r.dev, submitted, pinned, nil)
	raw, err := host.RunStep(t.Context(), plan, "preconditions", "check")
	if err != nil {
		t.Fatal(err)
	}
	var head landHead
	if err := json.Unmarshal(raw, &head); err != nil {
		t.Fatal(err)
	}
	snapshot := t.TempDir()
	runTestGit(t, snapshot, "init", "--quiet")
	runTestGit(t, snapshot, "fetch", "--quiet", "--depth=1", r.dev, submitted)
	runTestGit(t, snapshot, "checkout", "--quiet", "--detach", "FETCH_HEAD")
	for _, jobID := range []string{"policy", "review", "affected-tests", "land"} {
		t.Chdir(snapshot)
		h, p, doc := queuedSourcePlan(t, r.dev, submitted, pinned, map[string]json.RawMessage{"preconditions": raw})
		for _, job := range doc.Jobs {
			if job.ID == jobID {
				for _, step := range job.Steps {
					if _, err := h.RunStep(t.Context(), p, jobID, step.ID); err != nil {
						t.Fatalf("%s/%s: %v", jobID, step.ID, err)
					}
				}
			}
		}
	}
	if r.remoteMain() != head.Commit {
		t.Fatal("complete source receipts did not authorize exact merged owner")
	}
	common := r.git(r.dev, "rev-parse", "--path-format=absolute", "--git-common-dir")
	for _, key := range head.Checks.Required {
		file, err := landCheckPath(common, head.Checks.RunID, key)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var receipt landCheckReceipt
		if err := json.Unmarshal(data, &receipt); err != nil {
			t.Fatal(err)
		}
		if receipt.Tree != head.Tree || !receipt.Success {
			t.Fatalf("wrong authorizing source for %s: %+v", key, receipt)
		}
	}
}

func TestQueuedChecksKeepParallelRolesSeparate(t *testing.T) {
	owner := gateFixtureRepo(t)
	gitCommitAll(t, owner, "owner")
	head := landHead{Branch: strings.TrimSpace(gitOutput(t, owner, "symbolic-ref", "--short", "HEAD")), Commit: strings.TrimSpace(gitOutput(t, owner, "rev-parse", "HEAD")), Tree: strings.TrimSpace(gitOutput(t, owner, "rev-parse", "HEAD^{tree}")), Checks: &landCheckRequirements{RunID: "run-parallel-roles", Required: []string{"review/review", "affected-tests/test"}}}
	q := &queuedChecks{Owner: owner, RunID: head.Checks.RunID}
	ctx := newTestContext(t)
	t.Chdir(t.TempDir())
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	results := make(chan error, 2)
	for _, key := range head.Checks.Required {
		go func() {
			results <- q.run(ctx, key, head, func(ctx context.Context) error {
				started <- struct{}{}
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		}()
	}
	for range 2 {
		select {
		case <-started:
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
	}
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if err := verifyLanding(ctx, gitIn(owner), owner, head, reviewRecord{Tree: head.Tree, Result: resultPass}); err != nil {
		t.Fatal(err)
	}
}

func TestQueuedCheckSourceChangesInvalidateItsReceipt(t *testing.T) {
	owner := gateFixtureRepo(t)
	gitCommitAll(t, owner, "owner")
	head := landHead{Branch: strings.TrimSpace(gitOutput(t, owner, "symbolic-ref", "--short", "HEAD")), Commit: strings.TrimSpace(gitOutput(t, owner, "rev-parse", "HEAD")), Tree: strings.TrimSpace(gitOutput(t, owner, "rev-parse", "HEAD^{tree}")), Checks: &landCheckRequirements{RunID: "run-dirty-check", Required: []string{"policy/probe"}}}
	file := filepath.Join(owner, "internal/sound.go")
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	q := &queuedChecks{Owner: owner, RunID: head.Checks.RunID}
	ctx := newTestContext(t)
	err = q.run(ctx, "policy/probe", head, func(context.Context) error {
		return os.WriteFile(file, []byte("package internal\nfunc Sound() int {return 2}\n"), 0o600)
	})
	if err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("source mutation accepted: %v", err)
	}
	writeGoFile(t, file, string(before))
	if err := verifyLanding(ctx, gitIn(owner), owner, head, reviewRecord{Tree: head.Tree, Result: resultPass}); err == nil {
		t.Fatal("restoring source hid an invalidated check")
	}
}
