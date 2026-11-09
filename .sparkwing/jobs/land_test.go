package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

type landGit struct {
	answers map[string]string
	fail    map[string]error
	calls   []string
}

func (g *landGit) run(_ context.Context, args ...string) (string, error) {
	key := strings.Join(args, " ")
	g.calls = append(g.calls, key)
	if err, ok := g.fail[key]; ok {
		return "", err
	}
	return g.answers[key], nil
}

func landableGit() *landGit {
	return &landGit{
		answers: map[string]string{
			"symbolic-ref --short -q HEAD": "work-branch",
			"rev-parse HEAD":               "c0ffee00",
			"rev-parse HEAD^{tree}":        "7ee00000",
			"rev-parse origin/main":        "ba5e0000",
		},
		fail: map[string]error{},
	}
}

func TestLandPreconditionsAcceptARebasedCleanBranch(t *testing.T) {
	g := landableGit()
	head, err := checkLandPreconditions(t.Context(), g.run, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	if head != (landHead{Branch: "work-branch", Commit: "c0ffee00", Tree: "7ee00000", Base: "ba5e0000"}) {
		t.Fatalf("head = %+v", head)
	}
	if !slices.Contains(g.calls, "fetch --quiet origin main") {
		t.Fatalf("preconditions judged a stale origin/main: %v", g.calls)
	}
}

func TestLandPreconditionsAcceptABranchBehindMainThatMergesCleanly(t *testing.T) {
	g := landableGit()
	g.fail["merge-base --is-ancestor origin/main HEAD"] = errors.New("exit 1")
	if _, err := checkLandPreconditions(t.Context(), g.run, "/repo"); err != nil {
		t.Fatalf("a branch whose merge with main is clean was refused: %v", err)
	}
	if !slices.Contains(g.calls, "merge-tree --write-tree --no-messages HEAD origin/main") {
		t.Fatalf("preconditions accepted a branch behind main without trying the merge: %v", g.calls)
	}
}

func TestLandPreconditionsRefuseWithTheFixingCommand(t *testing.T) {
	cases := map[string]struct {
		edit func(*landGit)
		want string
	}{
		"detached": {func(g *landGit) { g.fail["symbolic-ref --short -q HEAD"] = errors.New("exit 1") }, "git -C /repo switch <branch>"},
		"main":     {func(g *landGit) { g.answers["symbolic-ref --short -q HEAD"] = "main" }, "git -C /repo switch -c <branch>"},
		"dirty": {func(g *landGit) {
			g.answers["status --porcelain --untracked-files=normal"] = " M go.mod"
		}, "git -C /repo add <paths> && git -C /repo commit"},
		"fetch": {func(g *landGit) { g.fail["fetch --quiet origin main"] = errors.New("offline") }, "retry: git -C /repo fetch origin main"},
		"behind main with a conflict": {func(g *landGit) {
			g.fail["merge-base --is-ancestor origin/main HEAD"] = errors.New("exit 1")
			g.fail["merge-tree --write-tree --no-messages HEAD origin/main"] = errors.New("exit 1")
		}, "git -C /repo fetch origin main && git -C /repo merge origin/main, then rerun sparkwing run land"},
		"nothing to land": {func(g *landGit) { g.answers["rev-parse origin/main"] = "c0ffee00" }, "nothing to land"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g := landableGit()
			tc.edit(g)
			_, err := checkLandPreconditions(t.Context(), g.run, "/repo")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func passingReview() reviewRecord {
	return reviewRecord{Tree: "7ee00000", Result: resultPass, Branch: "work-branch"}
}

func acceptedHead() landHead {
	return landHead{Branch: "work-branch", Commit: "c0ffee00", Tree: "7ee00000", Base: "ba5e0000"}
}

func TestLandVerifyRefusesUnlessTheReviewPassedThisTree(t *testing.T) {
	cases := map[string]struct {
		review func(*reviewRecord)
		git    func(*landGit)
		want   string
	}{
		"review blocked": {review: func(r *reviewRecord) { r.Result, r.BlockingOpen = resultBlock, 2 }, want: `result is "block" with 2 blocking`},
		"review empty":   {review: func(r *reviewRecord) { r.Result = "" }, want: `result is ""`},
		"other tree":     {review: func(r *reviewRecord) { r.Tree = "0dd00000" }, want: "review judged tree"},
		"head moved":     {git: func(g *landGit) { g.answers["rev-parse HEAD"] = "beef0000" }, want: "HEAD moved"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g := landableGit()
			if tc.git != nil {
				tc.git(g)
			}
			review := passingReview()
			if tc.review != nil {
				tc.review(&review)
			}
			err := verifyLanding(t.Context(), g.run, "/repo", acceptedHead(), review)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
	if err := verifyLanding(t.Context(), landableGit().run, "/repo", acceptedHead(), passingReview()); err != nil {
		t.Fatalf("a passing review of HEAD's tree was refused: %v", err)
	}
}

func TestLandPushIsAFastForwardOfTheGatedCommit(t *testing.T) {
	for name, tc := range map[string]struct {
		tree string
		want string
	}{
		"gated tree skips the hook":  {tree: "7ee00000", want: "push --no-verify origin c0ffee00:refs/heads/main"},
		"another tree runs the hook": {tree: "0dd00000", want: "push origin c0ffee00:refs/heads/main"},
	} {
		t.Run(name, func(t *testing.T) {
			g := landableGit()
			g.answers["rev-parse c0ffee00^{tree}"] = tc.tree
			if err := pushLanding(t.Context(), g.run, "/repo", acceptedHead().Commit, acceptedHead().Tree); err != nil {
				t.Fatal(err)
			}
			push := g.calls[len(g.calls)-1]
			if push != tc.want {
				t.Fatalf("push = %q, want %q", push, tc.want)
			}
			for _, arg := range strings.Fields(push) {
				if strings.HasPrefix(arg, "+") || strings.HasPrefix(arg, "--force") || arg == "-f" {
					t.Fatalf("push forces with %q", arg)
				}
			}
		})
	}
}

func TestLandPushRefusalChecksFreshAncestry(t *testing.T) {
	for _, test := range []struct {
		name, reason, ancestor string
		moved                  bool
	}{
		{"main moved", " ! [rejected] c0ffee00 -> main (fetch first)", "older000", true},
		{"remote failure", " ! [remote rejected] c0ffee00 -> main (fatal error in commit_refs_rules)", "ba5e0000", false},
		{"main moved without rejection text", "remote update failed", "older000", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			g := landableGit()
			g.answers["rev-parse c0ffee00^{tree}"] = "7ee00000"
			g.answers["merge-base origin/main c0ffee00"] = test.ancestor
			g.fail["push --no-verify origin c0ffee00:refs/heads/main"] = errors.New(test.reason)
			err := pushLanding(t.Context(), g.run, "/repo", acceptedHead().Commit, acceptedHead().Tree)
			if err == nil || !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("err = %v, want the remote's reason %q", err, test.reason)
			}
			if moved := errors.As(err, new(errMainMoved)); moved != test.moved {
				t.Fatalf("err = %v, main moved = %v, want %v", err, moved, test.moved)
			}
			if test.moved {
				if !strings.Contains(err.Error(), "moved since the gate ran") || !strings.Contains(err.Error(), "rerun sparkwing run land") {
					t.Fatalf("err = %v, want the merge and rerun remedy", err)
				}
			} else if !strings.Contains(err.Error(), "push can be retried") || strings.Contains(err.Error(), "moved since the gate ran") {
				t.Fatalf("err = %v, want a retry without claiming main moved", err)
			}
			if !slices.Contains(g.calls, "fetch --quiet origin main") || !slices.Contains(g.calls, "merge-base origin/main c0ffee00") {
				t.Fatalf("calls = %v, want freshly fetched main's ancestry checked", g.calls)
			}
		})
	}
}

type landPlanStep struct {
	ID        string
	Needs     []string
	Risks     []string
	HasDryRun bool `json:"has_dry_run"`
}

type landPlanJob struct {
	ID     string
	Needs  []string `json:"deps"`
	Inline bool
	Cores  float64
	Steps  []landPlanStep
}

func landPlan(t *testing.T) map[string]landPlanJob {
	t.Helper()
	doc, err := newTestHost(t, sparkwingtest.Options{}).Plan(t.Context(), "Land", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var plan struct{ Jobs []landPlanJob }
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	jobs := map[string]landPlanJob{}
	for _, j := range plan.Jobs {
		jobs[j.ID] = j
	}
	return jobs
}

func TestLandOverlapsPolicyAndReviewBeforeTheGateAndPush(t *testing.T) {
	jobs := landPlan(t)
	gate := inspectPipeline(t, "Gate", nil)
	var gateIDs []string
	for _, j := range gate.Jobs {
		gateIDs = append(gateIDs, strings.ToLower(j.ID))
	}
	parallel := append(slices.Clone(gateIDs), "review")
	for _, id := range parallel {
		j, ok := jobs[id]
		if !ok {
			t.Fatalf("land has no %q job; jobs: %v", id, jobs)
		}
		switch id {
		case recordTreeJob:
			// safety: record-tree follows the gate jobs it records.
		case policyJobID, "review":
			if !slices.Equal(j.Needs, []string{"preconditions"}) {
				t.Fatalf("%s needs %v; policy and review should wait only for the preconditions", id, j.Needs)
			}
		default:
			if !slices.Equal(j.Needs, []string{policyJobID}) {
				t.Fatalf("%s needs %v; heavy gate jobs should wait for policy", id, j.Needs)
			}
		}
	}
	land := jobs["land"]
	needs := slices.Clone(land.Needs)
	slices.Sort(needs)
	want := append(slices.Clone(parallel), "preconditions")
	slices.Sort(want)
	if !slices.Equal(needs, want) {
		t.Fatalf("land needs %v, want every gate job, the review and the preconditions %v", needs, want)
	}
	if !land.Inline || !jobs["preconditions"].Inline || !jobs["review"].Inline {
		t.Fatal("preconditions, review and land must run in the developer's checkout")
	}
	for _, id := range []string{"preconditions", "review", recordTreeJob, "land"} {
		if jobs[id].Cores != lightJobCores {
			t.Fatalf("%s core request = %v, want %v so it starts beside running gates", id, jobs[id].Cores, lightJobCores)
		}
	}
	for _, step := range land.Steps {
		if step.ID == "push" {
			if !slices.Equal(step.Risks, []string{"destructive", "prod"}) || !step.HasDryRun {
				t.Fatalf("push step = %+v; it needs the release's risk labels and a dry-run body", step)
			}
			return
		}
	}
	t.Fatal("land job has no push step")
}

func TestLandReviewIsNeverSkipped(t *testing.T) {
	doc, err := newTestHost(t, sparkwingtest.Options{}).Plan(t.Context(), "Land", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Jobs []map[string]json.RawMessage
	}
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	for _, j := range plan.Jobs {
		var id string
		_ = json.Unmarshal(j["id"], &id)
		if id != "review" {
			continue
		}
		spec, err := json.Marshal(j)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"skip_if", "optional", "continue_on_error"} {
			if strings.Contains(string(spec), `"`+key+`"`) {
				t.Fatalf("review job declares %s; every change is reviewed: %s", key, spec)
			}
		}
		return
	}
	t.Fatal("land has no review job")
}
