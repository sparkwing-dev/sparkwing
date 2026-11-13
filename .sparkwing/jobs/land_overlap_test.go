package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	nodeproto "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	sdkserve "github.com/sparkwing-dev/sparkwing/sparkwing/runtime"
)

func TestQueuedLandOverlapsReviewAndTestsAfterFastChecks(t *testing.T) {
	doc := inspectPipeline(t, "Land", map[string]string{
		"queued": "true", "queued-main": strings.Repeat("a", 40), "brief": "Overlap queued checks",
	})
	jobs := map[string]jobDocument{}
	for _, job := range doc.Jobs {
		jobs[job.ID] = job
		if !job.Inline || job.ContinueOnError || job.Optional {
			t.Fatalf("queued prerequisite must judge this checkout and block on failure: %+v", job)
		}
	}
	fast, ok := jobs["fast"]
	if !ok {
		t.Fatal("queued land has no separate fast job")
	}
	if !slices.Equal(fast.Needs, []string{"preconditions"}) {
		t.Fatalf("fast dependencies = %v", fast.Needs)
	}
	if fast.Cores != float64(prePushReservationCores(runtime.NumCPU())) {
		t.Fatalf("fast reservation = %v", fast.Cores)
	}
	if fast.TimeoutMS != queuedFastTimeout.Milliseconds() || fast.NoProgressMS != queuedFastNoProgressTimeout.Milliseconds() {
		t.Fatalf("fast checks lost their own deadlines: %+v", fast)
	}
	tier := inspectWork(t, &PrePush{})
	if !slices.EqualFunc(fast.Steps, tier.Steps, func(a, b stepDocument) bool {
		return a.ID == b.ID && slices.Equal(a.Needs, b.Needs) && a.Finally == b.Finally && a.ContinueOnError == b.ContinueOnError
	}) || fast.FailurePolicy != sparkwing.FailFast {
		t.Fatalf("fast job must contain exactly the current pre-push tier: %+v", fast)
	}
	checks := jobs["affected-tests"]
	if !slices.Equal(checks.Needs, []string{"preconditions", "policy", "fast"}) || checks.FailurePolicy != sparkwing.FailFast {
		t.Fatalf("review and tests must share a fail-fast job after policy and fast: %+v", checks)
	}
	if checks.Cores != lightJobCores+float64(goStepParallelism(affectedPushHost())) {
		t.Fatalf("combined reservation = %v", checks.Cores)
	}
	if checks.TimeoutMS != affectedPushTestsTimeout.Milliseconds() || checks.NoProgressMS != affectedPushTestsTimeout.Milliseconds() {
		t.Fatalf("combined checks lost the affected tier's deadlines: %+v", checks)
	}
	if len(checks.Steps) != 2 || !slices.ContainsFunc(checks.Steps, func(step stepDocument) bool { return step.ID == "review" }) || !slices.ContainsFunc(checks.Steps, func(step stepDocument) bool { return step.ID == "test" }) {
		t.Fatalf("review and affected test bodies must both run: %+v", checks.Steps)
	}
	for _, step := range checks.Steps {
		if len(step.Needs) != 0 || step.Finally || step.ContinueOnError {
			t.Fatalf("review and tests must start together and cancel on either failure: %+v", step)
		}
	}
	if _, present := jobs["review"]; present {
		t.Fatal("queued review must not also run in an independent job")
	}
	if !slices.Equal(jobs["land"].Needs, []string{"preconditions", "policy", "fast", "affected-tests"}) {
		t.Fatalf("push must join every prerequisite: %v", jobs["land"].Needs)
	}
}

func TestQueuedLandReviewFailureCancelsAffectedTests(t *testing.T) {
	policy, err := os.ReadFile(filepath.Join(sourceTreeRoot(), ".review.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	finding, err := json.Marshal(map[string]any{"findings": []map[string]string{{"id": "F1", "severity": "P1", "file": "internal/sound.go:3", "summary": "fixture blocking finding"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, want string }{
		{name: "reviewer error", want: "exited 3: fixture reviewer usage limit"},
		{name: "blocking finding", want: "review blocked: 1 blocking finding(s) open"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := gateFixtureRepo(t)
			writeGoFile(t, filepath.Join(root, ".review.yaml"), string(policy))
			writeGoFile(t, filepath.Join(root, "internal", "sound_test.go"), "package internal\nimport \"testing\"\nfunc TestSound(t *testing.T) { _ = Sound() }\n")
			gitCommitAll(t, root, "base")
			runTestGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
			main := strings.TrimSpace(gitOutput(t, root, "rev-parse", "HEAD"))
			writeGoFile(t, filepath.Join(root, "internal", "sound.go"), "package internal\n\nfunc Sound() int { return 2 }\n")
			gitCommitAll(t, root, "candidate")

			tools := t.TempDir()
			replies := t.TempDir()
			fake := func(name, reply string) {
				body := "#!/bin/sh\ncat >/dev/null\necho 'fixture reviewer usage limit' >&2\nexit 3\n"
				if tc.name == "blocking finding" {
					file := filepath.Join(replies, name)
					if err := os.WriteFile(file, []byte(reply), 0o600); err != nil {
						t.Fatal(err)
					}
					body = fmt.Sprintf("#!/bin/sh\ncat >/dev/null\ncat %q\n", file)
				}
				if err := os.WriteFile(filepath.Join(tools, name), []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			claude, err := json.Marshal(map[string]any{"is_error": false, "session_id": "fixture", "result": string(finding)})
			if err != nil {
				t.Fatal(err)
			}
			codex, err := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]string{"type": "agent_message", "text": string(finding)}})
			if err != nil {
				t.Fatal(err)
			}
			fake("claude", string(claude))
			fake("codex", "{\"type\":\"thread.started\",\"thread_id\":\"fixture\"}\n"+string(codex)+"\n")
			for _, absent := range []string{"overwing", "bitwing", "gh"} {
				if err := os.WriteFile(filepath.Join(tools, absent), []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))

			host, plan, doc := prepareQueuedFixturePlan(t, func(ctx context.Context, plan *sparkwing.Plan) error {
				return (&Land{}).Plan(ctx, plan, LandArgs{Queued: true, QueuedMain: main, ReviewArgs: ReviewArgs{Brief: "Prove review failure cancels affected tests"}}, sparkwing.RunContext{RunID: queuedFixtureRunID})
			})
			var job jobDocument
			for _, candidate := range doc.Jobs {
				if candidate.ID == "affected-tests" {
					job = candidate
				}
			}
			if len(job.Steps) != 2 {
				t.Fatalf("combined job steps = %+v", job.Steps)
			}

			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			ctx = host.Context(ctx)
			realExec, ok := sdkserve.HostExec(ctx)
			if !ok {
				t.Fatal("test host has no command executor")
			}
			testsRunning, testsStopped := make(chan struct{}), make(chan struct{})
			ctx = sdkserve.WithHostExec(ctx, func(ctx context.Context, request nodeproto.ExecRequest) (sdkserve.HostExecResult, error) {
				if strings.Contains(strings.Join(request.Argv, " "), "go test -json ") {
					close(testsRunning)
					<-ctx.Done()
					close(testsStopped)
					return sdkserve.HostExecResult{}, ctx.Err()
				}
				return realExec(ctx, request)
			})
			ctx = sdkserve.DefaultOperations.Context(ctx, nodeproto.RunContext{}, nodeproto.RunnerIdentity{}, sdkserve.ServicesFrom(ctx))

			// safety: the executor starts both needless steps together and, under
			// FailFast, cancels every running sibling once one fails, then joins.
			// Starting tests first pins the interleaving where they are mid-run.
			type stepResult struct {
				id  string
				err error
			}
			results := make(chan stepResult, len(job.Steps))
			cancels := map[string]context.CancelFunc{}
			start := func(id string) {
				stepCtx, stop := context.WithCancel(ctx)
				cancels[id] = stop
				go func() {
					result, err := sdkserve.DefaultOperations.RunStep(stepCtx, plan, nodeproto.Request{Job: job.ID, Step: id, Attempt: 1, Settled: []string{}})
					if err == nil && result.Error != nil {
						err = result.Error
					}
					results <- stepResult{id, err}
				}()
			}
			start("test")
			select {
			case <-testsRunning:
			case <-ctx.Done():
				t.Fatal("affected tests never started their go test command")
			}
			start("review")
			errs := map[string]error{}
			for range job.Steps {
				result := <-results
				errs[result.id] = result.err
				if result.err != nil {
					if job.FailurePolicy != sparkwing.FailFast {
						t.Errorf("combined job policy %v lets %s fail without cancelling its sibling", job.FailurePolicy, result.id)
					}
					for _, stop := range cancels {
						stop()
					}
				}
			}
			if ctx.Err() != nil {
				t.Fatal("the combined job stopped only at the fixture deadline")
			}

			if errs["review"] == nil || !strings.Contains(errs["review"].Error(), tc.want) {
				t.Fatalf("review error = %v, want %q", errs["review"], tc.want)
			}
			select {
			case <-testsStopped:
			default:
				t.Fatal("review failure did not cancel the in-flight affected tests")
			}
			if errs["test"] == nil {
				t.Fatal("cancelled affected tests reported success")
			}
			if jobErr := errors.Join(errs["review"], errs["test"]); jobErr == nil {
				t.Fatal("combined job passed after review failed")
			}
			common := strings.TrimSpace(gitOutput(t, root, "rev-parse", "--path-format=absolute", "--git-common-dir"))
			for _, key := range []string{"review/review", "affected-tests/test"} {
				file, err := landCheckPath(common, queuedFixtureRunID, key)
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(file)
				if errors.Is(err, os.ErrNotExist) && key != "review/review" {
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				var receipt landCheckReceipt
				if err := json.Unmarshal(data, &receipt); err != nil {
					t.Fatal(err)
				}
				if receipt.Success {
					t.Fatalf("failed combined job left a passing %s receipt: %+v", key, receipt)
				}
			}
		})
	}
}
