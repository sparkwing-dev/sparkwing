package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwingtest"
)

func TestLandQueueSerializesOrdinaryAndFocusedLands(t *testing.T) {
	for _, args := range []map[string]string{nil, {"queued": "true", "queued-main": strings.Repeat("a", 40), "brief": "Review the queue implementation"}} {
		raw, err := newTestHost(t, sparkwingtest.Options{}).Plan(t.Context(), "Land", args)
		if err != nil {
			t.Fatal(err)
		}
		var plan struct {
			Concurrency []struct {
				Group    string
				Capacity *int
				OnLimit  string `json:"on_limit"`
			}
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &plan); err != nil {
			t.Fatal(err)
		}
		if len(plan.Concurrency) != 1 || plan.Concurrency[0].Group != "sparkwing-land" || plan.Concurrency[0].Capacity == nil || *plan.Concurrency[0].Capacity != 1 || (plan.Concurrency[0].OnLimit != "" && plan.Concurrency[0].OnLimit != "queue") {
			t.Fatalf("land must wait on the shared serial slot: %s", encoded)
		}
		doc := decodePipeline(t, raw)
		if args == nil {
			continue
		}
		var ids []string
		for _, job := range doc.Jobs {
			ids = append(ids, job.ID)
			if !job.Inline {
				t.Fatalf("queue checks must judge the merged checkout: %+v", job)
			}
			if job.ID == "land" && !slices.Equal(job.Needs, []string{"preconditions", "policy", "review", "affected-tests"}) {
				t.Fatalf("push dependencies: %v", job.Needs)
			}
			if job.ID == "review" && len(job.Steps) != len(inspectWork(t, &PrePush{}).Steps)+1 {
				t.Fatal("queue omitted pre-push checks")
			}
		}
		slices.Sort(ids)
		if !slices.Equal(ids, []string{"affected-tests", "land", "policy", "preconditions", "review"}) {
			t.Fatalf("focused jobs = %v", ids)
		}
	}
}

func TestLandQueueRequiresGoal(t *testing.T) {
	for _, brief := range []string{"", " \t\n"} {
		t.Run(brief, func(t *testing.T) {
			_, err := newTestHost(t, sparkwingtest.Options{}).Plan(t.Context(), "Land", map[string]string{"queued": "true", "queued-main": strings.Repeat("a", 40), "brief": brief})
			if err == nil || !strings.Contains(err.Error(), "requires a non-empty --brief") || !strings.Contains(err.Error(), "--brief 'BW"+"-1234: short intent'") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestLandQueueRequiresMainPin(t *testing.T) {
	_, err := newTestHost(t, sparkwingtest.Options{}).Plan(t.Context(), "Land", map[string]string{"queued": "true", "brief": "Review the queue"})
	if err == nil || !strings.Contains(err.Error(), "bin/queue-land.sh") {
		t.Fatalf("unpinned queue accepted: %v", err)
	}
}

func TestLandQueueRefusesMainThatMovedAfterSubmission(t *testing.T) {
	r := newLandRemote(t)
	pinned := r.remoteMain()
	submitted := r.git(r.dev, "rev-parse", "HEAD")
	r.moveMain(map[string]string{"b/b.txt": "new main gate\n"})
	err := prepareQueuedLanding(newTestContext(t), gitIn(r.dev), r.dev, submitted, pinned)
	if err == nil || !strings.Contains(err.Error(), "enqueue again through main's bin/queue-land.sh") {
		t.Fatalf("stale main plan accepted: %v", err)
	}
	if got := r.git(r.dev, "rev-parse", "HEAD"); got != submitted {
		t.Fatalf("refused submission was merged: %s", got)
	}
}

func TestLandQueueRefusesMainThatMovedOnTheSecondFetch(t *testing.T) {
	r := newLandRemote(t)
	pinned := r.remoteMain()
	submitted := r.git(r.dev, "rev-parse", "HEAD")
	advanced := r.commit(r.other, map[string]string{"b/b.txt": "main advanced\n"}, "advance main")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	tools := t.TempDir()
	seen := filepath.Join(tools, "first-fetch")
	writeDeliveryTool(t, filepath.Join(tools, "git"), "#!/bin/sh\n"+
		"if [ \"$*\" = 'fetch --quiet origin main' ]; then\n"+
		"  if [ -f "+shellQuote(seen)+" ]; then\n"+
		"    "+shellQuote(realGit)+" -C "+shellQuote(r.bare)+" fetch --quiet "+shellQuote(r.other)+" HEAD:refs/heads/main || exit\n"+
		"  else touch "+shellQuote(seen)+"; fi\nfi\nexec "+shellQuote(realGit)+" \"$@\"\n")
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	host, plan, _ := prepareWork(t, &landPreconditionsJob{RepoDir: r.dev, Queued: true, Submitted: submitted, QueuedMain: pinned})
	_, err = host.RunStep(t.Context(), plan, "job", "check")
	if err == nil || !strings.Contains(err.Error(), "origin/main changed from planned") {
		t.Fatalf("second fetch accepted a stale pin: %v", err)
	}
	if r.remoteMain() != advanced || r.git(r.dev, "rev-parse", "HEAD") != submitted {
		t.Fatal("second fetch did not advance main without changing the candidate")
	}
}

func TestLandQueueRefusesMainThatAdvancedToTheGatedCommit(t *testing.T) {
	r := newLandRemote(t)
	head := r.head()
	r.git(r.bare, "fetch", "--quiet", r.dev, "HEAD:refs/heads/main")
	var calls []string
	git := func(ctx context.Context, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		return gitIn(r.dev)(ctx, args...)
	}
	_, err := pushQueuedLanding(newTestContext(t), git, r.dev, head)
	if err == nil || !strings.Contains(err.Error(), "origin/main changed from planned") {
		t.Fatalf("post-gate target change accepted: %v", err)
	}
	for _, call := range calls {
		if strings.HasPrefix(call, "push ") {
			t.Fatalf("stale gate reached push: %s", call)
		}
	}
}

func TestLandQueuePipelineChangesRunTheirOwnFocusedTests(t *testing.T) {
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, ".sparkwing", "go.mod"), "module fixture/pipelines\n\ngo 1.26.0\n")
	writeGoFile(t, filepath.Join(root, ".sparkwing", "jobs", "land.go"), "package jobs\nfunc Gate() bool { return false }\n")
	writeGoFile(t, filepath.Join(root, ".sparkwing", "jobs", "land_test.go"), "package jobs\nimport \"testing\"\nfunc TestGate(t *testing.T) { if !Gate() { t.Fatal(\"proposed pipeline gate failed\") } }\n")
	gitCommitAll(t, root, "pipeline fixture")
	ctx := newTestContext(t)
	err := runAffectedTestFiles(ctx, []string{".sparkwing/jobs/land.go"}, "pipeline change", currentHost())
	if err == nil || !strings.Contains(err.Error(), "proposed pipeline gate failed") {
		t.Fatalf("candidate's pipeline tests did not run: %v", err)
	}
	writeGoFile(t, filepath.Join(root, ".sparkwing", "jobs", "land.go"), "package jobs\nfunc Gate() bool { return true }\n")
	if err := runAffectedTestFiles(ctx, []string{".sparkwing/jobs/land.go"}, "pipeline change", currentHost()); err != nil {
		t.Fatal(err)
	}
}

func TestLandQueueAcceptsExplicitGoal(t *testing.T) {
	brief := "Review only the queued landing goal requirement"
	_, err := newTestHost(t, sparkwingtest.Options{}).Plan(t.Context(), "Land", map[string]string{"queued": "true", "queued-main": strings.Repeat("a", 40), "brief": brief})
	if err != nil {
		t.Fatal(err)
	}
	fx := newReviewFixture(t)
	fx.def.replies = []string{`{"findings": []}`}
	rec, _, err := runReview(t.Context(), fx.deps, testPolicy(t), reviewInputs{branch: "bw" + "-1234-unrelated-goal", brief: brief})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Objective.Source != "brief" || !strings.Contains(fx.def.calls[0].prompt, brief) || len(fx.tickets) != 0 {
		t.Fatalf("explicit goal did not reach review: objective=%+v tickets=%v", rec.Objective, fx.tickets)
	}
}

func TestLandQueueMergeFailureDoesNotPreventNextEntry(t *testing.T) {
	r := newLandRemote(t)
	r.moveMain(map[string]string{"a/a.txt": "conflict\n"})
	submitted := r.git(r.dev, "rev-parse", "HEAD")
	err := prepareQueuedLanding(newTestContext(t), gitIn(r.dev), r.dev, submitted, r.remoteMain())
	if err == nil || !strings.Contains(err.Error(), "merge failed") {
		t.Fatalf("err = %v", err)
	}
	if got := r.git(r.dev, "rev-parse", "HEAD"); got != submitted {
		t.Fatalf("failed entry moved: %s", got)
	}
	if got := r.git(r.dev, "status", "--porcelain"); got != "" {
		t.Fatalf("merge left dirty checkout: %s", got)
	}
	r.git(r.other, "switch", "-c", "next")
	next := r.commit(r.other, map[string]string{"next.txt": "next entry\n"}, "next entry")
	if err := prepareQueuedLanding(newTestContext(t), gitIn(r.other), r.other, next, r.remoteMain()); err != nil {
		t.Fatal(err)
	}
	head, err := checkLandPreconditions(newTestContext(t), gitIn(r.other), r.other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pushQueuedLanding(newTestContext(t), gitIn(r.other), r.other, head); err != nil {
		t.Fatal(err)
	}
	if got := r.git(r.other, "ls-remote", "origin", "refs/heads/main"); !strings.HasPrefix(got, head.Commit) {
		t.Fatalf("next entry not pushed: %s", got)
	}
}

func TestLandQueueMergesMainBeforeCapturingGatedHead(t *testing.T) {
	r := newLandRemote(t)
	main := r.moveMain(map[string]string{"b/b.txt": "main change\n"})
	submitted := r.git(r.dev, "rev-parse", "HEAD")
	if err := prepareQueuedLanding(newTestContext(t), gitIn(r.dev), r.dev, submitted, r.remoteMain()); err != nil {
		t.Fatal(err)
	}
	head, err := checkLandPreconditions(newTestContext(t), gitIn(r.dev), r.dev)
	if err != nil {
		t.Fatal(err)
	}
	if head.Commit == submitted {
		t.Fatal("main was not merged before checks")
	}
	r.git(r.dev, "merge-base", "--is-ancestor", main, head.Commit)
	if _, err := pushQueuedLanding(newTestContext(t), gitIn(r.dev), r.dev, head); err != nil {
		t.Fatal(err)
	}
	if got := r.git(r.dev, "ls-remote", "origin", "refs/heads/main"); !strings.HasPrefix(got, head.Commit) {
		t.Fatalf("wrong pushed HEAD: %s", got)
	}
}

func TestLandQueueRefusesChangedCheckoutAndNeverMergesAtPush(t *testing.T) {
	const push = "push --porcelain --no-verify --force-with-lease=refs/heads/main:ba5e0000 origin c0ffee00:refs/heads/main"
	for _, change := range []string{"head", "branch", "dirty", "tree", "main moved"} {
		t.Run(change, func(t *testing.T) {
			g := landableGit()
			g.answers["rev-parse c0ffee00^{tree}"] = "7ee00000"
			switch change {
			case "head":
				g.answers["rev-parse HEAD"] = "new-head"
			case "branch":
				g.answers["symbolic-ref --short -q HEAD"] = "another-branch"
			case "dirty":
				g.answers["status --porcelain --untracked-files=normal"] = " M file"
			case "tree":
				g.answers["rev-parse c0ffee00^{tree}"] = "another-tree"
			case "main moved":
				g.fail[push] = errors.New("main moved")
			}
			_, err := pushQueuedLanding(t.Context(), g.run, "/repo", acceptedHead())
			if err == nil {
				t.Fatal("changed checkout was pushed")
			}
			if change == "main moved" && !slices.Contains(g.calls, push) {
				t.Fatalf("main movement did not reach the atomic push: %v", g.calls)
			}
			for _, call := range g.calls {
				if strings.HasPrefix(call, "merge ") || change != "main moved" && strings.HasPrefix(call, "push ") {
					t.Fatalf("unsafe git call: %s", call)
				}
			}
		})
	}
}

func TestLandQueueRefusesChangedSubmissionBeforeMerging(t *testing.T) {
	g := landableGit()
	if err := prepareQueuedLanding(context.Background(), g.run, "/repo", "old-head", "base0000"); err == nil {
		t.Fatal("changed submission accepted")
	}
	for _, call := range g.calls {
		if strings.HasPrefix(call, "merge ") {
			t.Fatalf("merged changed submission: %v", g.calls)
		}
	}
}
