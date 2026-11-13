package jobs

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	sdkserve "github.com/sparkwing-dev/sparkwing/sparkwing/runtime"
)

func TestAffectedPushTestsBlockHookAndQueuedLanding(t *testing.T) {
	for _, queued := range []bool{false, true} {
		name := "hook"
		if queued {
			name = "queued"
		}
		t.Run(name, func(t *testing.T) {
			plan := sparkwing.NewPlan()
			var err error
			if queued {
				err = (&Land{}).Plan(t.Context(), plan, LandArgs{Queued: true, QueuedMain: strings.Repeat("a", 40), ReviewArgs: ReviewArgs{Brief: "Check local test gating"}}, sparkwing.RunContext{Pipeline: "land"})
			} else {
				err = (&PrePush{}).Plan(t.Context(), plan, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: "pre-push"})
			}
			if err != nil {
				t.Fatal(err)
			}
			doc := decodePipeline(t, sdkserve.DefaultOperations.Document(plan))
			var tests, land *jobDocument
			for i := range doc.Jobs {
				job := &doc.Jobs[i]
				if job.ID == "affected-tests" {
					tests = job
				}
				if job.ID == "land" {
					land = job
				}
			}
			if tests == nil {
				t.Fatal("push path omits affected-tests")
			}
			reservation := float64(goStepParallelism(affectedPushHost()))
			if queued {
				reservation += lightJobCores
			}
			if tests.ContinueOnError || tests.Optional || time.Duration(tests.TimeoutMS)*time.Millisecond <= prePushBudget || tests.Cores != reservation {
				t.Fatalf("affected tests must block with their own deadline and reserved Go cores: %+v", tests)
			}
			want := []string{"pre-push"}
			if queued {
				want = []string{"preconditions", "policy", "fast"}
			}
			for _, prerequisite := range want {
				if !slices.Contains(tests.Needs, prerequisite) {
					t.Errorf("tests start before %s: %v", prerequisite, tests.Needs)
				}
			}
			if queued && (!tests.Inline || land == nil || !slices.Contains(land.Needs, "affected-tests")) {
				t.Fatalf("queued push can bypass checked checkout: tests=%+v land=%+v", tests, land)
			}
		})
	}
	plan := sparkwing.NewPlan()
	if err := (&Land{}).Plan(t.Context(), plan, LandArgs{}, sparkwing.RunContext{Pipeline: "land"}); err != nil {
		t.Fatal(err)
	}
	doc := decodePipeline(t, sdkserve.DefaultOperations.Document(plan))
	for _, job := range doc.Jobs {
		if job.ID == "affected-tests" {
			t.Fatal("ordinary Land repeats its Gate tests")
		}
	}
}

func TestAffectedPushTestsRunOnlyForMainHookUpdates(t *testing.T) {
	for _, tc := range []struct {
		name, refs string
		want       bool
	}{
		{"branch", "refs/heads/main abc refs/heads/backup def\n", false},
		{"main", "refs/heads/work abc refs/heads/main def\n", true},
		{"several refs", "refs/heads/work abc refs/heads/work def\nrefs/heads/other abc refs/heads/main def\n", true},
		{"no updates", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SPARKWING_GATE_PUSH_REFS", tc.refs)
			doc := inspectPlan(t, func(plan *sparkwing.Plan) error {
				return (&PrePush{}).Plan(t.Context(), plan, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: "pre-push"})
			})
			var tests, fast bool
			for _, job := range doc.Jobs {
				tests = tests || job.ID == "affected-tests"
				fast = fast || job.ID == "pre-push"
			}
			if tests != tc.want || !fast {
				t.Fatalf("hook refs %q: tests=%t fast=%t, want tests=%t and fast checks", tc.refs, tests, fast, tc.want)
			}
		})
	}
}

func TestAffectedPushTestsRunCommittedControllerBodies(t *testing.T) {
	if testing.Short() {
		t.Skip("real Go test subprocesses")
	}
	for _, change := range []string{"modify", "delete", "rename"} {
		t.Run(change, func(t *testing.T) {
			root := gateFixtureRepo(t)
			controller := filepath.Join(root, "internal", "controller")
			writeGoFile(t, filepath.Join(controller, "controller.go"), "package controller\n")
			writeGoFile(t, filepath.Join(controller, "removable.go"), "package controller\nconst Removed = 1\n")
			writeGoFile(t, filepath.Join(controller, "controller_test.go"), "package controller\nimport \"testing\"\nfunc TestController(t *testing.T) { t.Fatal(\"controller test body executed\") }\n")
			writeGoFile(t, filepath.Join(root, "critical", "guard_test.go"), "package critical\nimport \"testing\"\n//sparkwing:critical\nfunc TestGuard(t *testing.T) { if testing.Short() { t.Skip(\"short\") }; t.Fatal(\"critical test body executed\") }\n")
			writeGoFile(t, filepath.Join(root, "quiet", "quiet_test.go"), "package quiet\nimport \"testing\"\nfunc TestQuiet(t *testing.T) { t.Fatal(\"unrelated test body executed\") }\n")
			gitCommitAll(t, root, "base")
			runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
			switch change {
			case "modify":
				writeGoFile(t, filepath.Join(controller, "controller.go"), "package controller\nconst Changed = 1\n")
			case "delete":
				if err := os.Remove(filepath.Join(controller, "removable.go")); err != nil {
					t.Fatal(err)
				}
			case "rename":
				moved := filepath.Join(root, "moved", "removable.go")
				if err := os.MkdirAll(filepath.Dir(moved), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(controller, "removable.go"), moved); err != nil {
					t.Fatal(err)
				}
			}
			gitCommitAll(t, root, change)
			writeGoFile(t, filepath.Join(root, "quiet", "staged.go"), "package quiet\n")
			runTestGit(t, root, "add", "quiet/staged.go")
			host, plan, _ := preparePlan(t, func(ctx context.Context, plan *sparkwing.Plan) error {
				return (&PrePush{}).Plan(ctx, plan, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: "pre-push"})
			})
			_, err := host.RunStep(t.Context(), plan, "affected-tests", "test")
			if err == nil || !strings.Contains(err.Error(), "controller test body executed") || !strings.Contains(err.Error(), "critical test body executed") || strings.Contains(err.Error(), "unrelated test body executed") {
				t.Fatalf("committed %s must run controller and full critical bodies despite unrelated index: %v", change, err)
			}
		})
	}
}

func TestAffectedPushTestsSkipChangelogOnlyBodies(t *testing.T) {
	root := gateFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "critical", "guard_test.go"), "package critical\nimport \"testing\"\n//sparkwing:critical\nfunc TestGuard(t *testing.T) { t.Fatal(\"changelog ran critical body\") }\n")
	gitCommitAll(t, root, "base")
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	writeGoFile(t, filepath.Join(root, "CHANGELOG.md"), "# Changes\n")
	gitCommitAll(t, root, "notes")
	host, plan, _ := preparePlan(t, func(ctx context.Context, plan *sparkwing.Plan) error {
		return (&PrePush{}).Plan(ctx, plan, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: "pre-push"})
	})
	if _, err := host.RunStep(t.Context(), plan, "affected-tests", "test"); err != nil {
		t.Fatal(err)
	}
}
