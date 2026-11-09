package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwingtest"
)

func TestLandQueueSerializesOrdinaryAndFocusedLands(t *testing.T) {
	for _, args := range []map[string]string{nil, {"queued": "true", "brief": "Review the queue implementation"}} {
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
			if job.ID == "land" && !slices.Equal(job.Needs, []string{"preconditions", "policy", "review"}) {
				t.Fatalf("push dependencies: %v", job.Needs)
			}
			if job.ID == "review" && len(job.Steps) != len(inspectWork(t, &PrePush{}).Steps)+1 {
				t.Fatal("queue omitted pre-push checks")
			}
		}
		slices.Sort(ids)
		if !slices.Equal(ids, []string{"land", "policy", "preconditions", "review"}) {
			t.Fatalf("focused jobs = %v", ids)
		}
	}
}

func TestLandQueueRequiresGoal(t *testing.T) {
	_, err := newTestHost(t, sparkwingtest.Options{}).Plan(t.Context(), "Land", map[string]string{"queued": "true"})
	if err == nil || !strings.Contains(err.Error(), "requires --brief") {
		t.Fatalf("err = %v", err)
	}
}

func TestLandQueueMergeFailureDoesNotPreventNextEntry(t *testing.T) {
	r := newLandRemote(t)
	r.moveMain(map[string]string{"a/a.txt": "conflict\n"})
	submitted := r.git(r.dev, "rev-parse", "HEAD")
	err := prepareQueuedLanding(newTestContext(t), gitIn(r.dev), r.dev, submitted)
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
	if err := prepareQueuedLanding(newTestContext(t), gitIn(r.other), r.other, next); err != nil {
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
	if err := prepareQueuedLanding(newTestContext(t), gitIn(r.dev), r.dev, submitted); err != nil {
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
	for _, change := range []string{"head", "branch", "dirty", "main moved"} {
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
			case "main moved":
				g.fail["push --no-verify origin c0ffee00:refs/heads/main"] = errors.New("main moved")
			}
			_, err := pushQueuedLanding(t.Context(), g.run, "/repo", acceptedHead())
			if err == nil {
				t.Fatal("changed checkout was pushed")
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
	if err := prepareQueuedLanding(context.Background(), g.run, "/repo", "old-head"); err == nil {
		t.Fatal("changed submission accepted")
	}
	for _, call := range g.calls {
		if strings.HasPrefix(call, "merge ") {
			t.Fatalf("merged changed submission: %v", g.calls)
		}
	}
}

func TestLandQueueDocumentedSubmissionMatchesExample(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(sourceTreeRoot(), "DELIVERY.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, example := range (Land{}).Examples() {
		if !strings.Contains(example.Command, "--queued") {
			continue
		}
		command := strings.ReplaceAll(example.Command, "Describe the goal", "Describe the goal to review")
		if !strings.Contains(string(body), command) {
			t.Fatalf("DELIVERY.md omits queue submission: %s", command)
		}
		return
	}
	t.Fatal("land help omits queue submission")
}
