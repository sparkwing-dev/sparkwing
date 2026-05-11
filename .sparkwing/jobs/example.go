package jobs

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Example is the reference pipeline for this repo: a runnable tour
// of every major SDK feature in one cohesive shape.
//
//	Plan layer
//	  Job (single closure)               .................. configure, notify
//	  Job (multi-step, typed output)     .................. build (Produces[BuildOut])
//	  JobFanOut -> NodeGroup             .................. checks, publish-images
//	  GroupJobs -> NodeGroup             .................. post-deploy-verify
//	  JobApproval (auto-approve in 2s)   .................. approve-deploy
//	  modifiers: Retry · Timeout         .................. build, deploy
//	  modifiers: Inline                  .................. configure, notify, audit-*, docs-snapshot, *-publish, *-report
//	  modifiers: BeforeRun · AfterRun    .................. configure, notify
//	  modifiers: OnFailure               .................. deploy -> deploy-rollback
//	  Ref[T] / RefTo[T] typed values     .................. build -> publish, deploy
//
//	Work layer (inside build)
//	  Step (untyped + typed)             .................. fetch-*, compile, package
//	  GroupSteps -> StepGroup            .................. ci (lint | vet | test)
//	  Needs (sequential + parallel)      .................. throughout
//	  StepGet[T] mid-Work read           .................. strip, package read compile
//
//	Work layer (inside deploy)
//	  WorkStep.SkipIf                    .................. rollout (DRY_RUN=1)
//	  Ref[T].Get(ctx) from a step        .................. render reads buildRef
//
//	Cross-cutting
//	  Annotate (node-level summaries)    .................. every step
//
// Each step sleeps briefly so every node and step renders as its own
// bar in the dashboard waterfall. Total run ~4-6s.
type Example struct{ sparkwing.Base }

func (Example) ShortHelp() string {
	return "Reference pipeline: a runnable tour of every major SDK feature"
}

func (Example) Help() string {
	return "A self-contained pipeline that exercises the SDK end-to-end: Plan-layer Jobs and JobFanOut (NodeGroup), JobApproval gate (auto-approves after 2s for demo purposes), multi-step Work with GroupSteps (StepGroup), typed Ref[T] outputs via Produces[T] and StepGet[T], the full modifier set (Retry, Timeout, BeforeRun, AfterRun, OnFailure), Plan-layer and Work-layer SkipIf, and Annotate for persistent node-level summaries. Every step sleeps briefly and emits an Annotate so the dashboard surfaces a readable narrative without digging into logs."
}

func (Example) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Run the full tour", Command: "wing example"},
		{Comment: "Render the DAG without running", Command: "sparkwing pipeline explain --name example"},
		{Comment: "Skip the rollout step via Work-layer SkipIf", Command: "DRY_RUN=1 wing example"},
	}
}

// BuildOut is the typed value the build node produces. publish and
// deploy read it via sparkwing.Ref[BuildOut].
type BuildOut struct {
	Tag    string `json:"tag"`
	Digest string `json:"digest"`
	SizeMB int    `json:"size_mb"`
}

func (p *Example) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	// Single-closure Job with BeforeRun + AfterRun hooks. Inline so
	// the dispatcher runs it in-process -- typical of lightweight
	// setup/config work that doesn't need a runner boot.
	configure := sparkwing.Job(plan, "configure", configureFn).
		Inline().
		BeforeRun(func(ctx context.Context) error {
			sparkwing.Info(ctx, "BeforeRun: warming workspace")
			return nil
		}).
		AfterRun(func(ctx context.Context, err error) {
			sparkwing.Info(ctx, "AfterRun: cleanup (err=%v)", err)
		})

	// JobFanOut: three parallel Plan-layer Nodes returned as one
	// NodeGroup. The group is a single Needs() target downstream and
	// renders as a collapsible cluster.
	checks := sparkwing.JobFanOut(plan, "checks",
		[]string{"audit-secrets", "validate-config", "scan-deps"},
		func(name string) (string, any) {
			return name, checkFn(name)
		}).Needs(configure)

	// Off-shoot branches that depend on configure but never rejoin
	// the build/deploy/notify lane -- useful for testing taller DAG
	// layouts. Six nodes hang directly off configure; three of those
	// chain further to exercise deep columns.
	// Lightweight offshoot leaves run Inline -- they're sub-second
	// closures that don't justify spinning up a runner.
	sparkwing.Job(plan, "audit-permissions", offshootFn("audit-permissions", 3)).Needs(configure).Inline()
	sparkwing.Job(plan, "audit-licenses", offshootFn("audit-licenses", 3)).Needs(configure).Inline()
	sparkwing.Job(plan, "docs-snapshot", offshootFn("docs-snapshot", 2)).Needs(configure).Inline()
	repoStats := sparkwing.Job(plan, "repo-stats", offshootFn("repo-stats", 3)).Needs(configure)
	sparkwing.Job(plan, "repo-stats-publish", offshootFn("repo-stats-publish", 2)).Needs(repoStats).Inline()

	benchBaseline := sparkwing.Job(plan, "bench-baseline", offshootFn("bench-baseline", 4)).Needs(configure)
	benchRecord := sparkwing.Job(plan, "bench-record", offshootFn("bench-record", 3)).Needs(benchBaseline)
	sparkwing.Job(plan, "bench-publish", offshootFn("bench-publish", 2)).Needs(benchRecord).Inline()

	invCache := sparkwing.Job(plan, "inventory-cache", offshootFn("inventory-cache", 3)).Needs(configure)
	invWarm := sparkwing.Job(plan, "inventory-warm", offshootFn("inventory-warm", 4)).Needs(invCache)
	sparkwing.Job(plan, "inventory-report", offshootFn("inventory-report", 2)).Needs(invWarm).Inline()

	// Multi-step Job with typed output (Produces[BuildOut]). The
	// returned Node is the source for buildRef below; Retry is set
	// so a flake retries once.
	build := sparkwing.Job(plan, "build", &BuildJob{}).
		Needs(checks).
		Retry(1, sparkwing.RetryBackoff(200*time.Millisecond))
	buildRef := sparkwing.RefTo[BuildOut](build)

	// JobFanOut whose member closures consume the typed buildRef. The
	// group's .Needs(build) doubles as the data-availability gate.
	publishImages := sparkwing.JobFanOut(plan, "publish-images",
		[]string{"linux-amd64", "linux-arm64", "darwin-arm64"},
		func(target string) (string, any) {
			return "publish-" + target, publishFn(target, buildRef)
		}).Needs(build)

	// Approval gate between publish and deploy. Real pipelines block
	// here for a human; the demo uses a 2s Timeout with OnExpiry=
	// ApprovalApprove so the gate auto-approves and the run flows
	// through. Swap to ApprovalFail/Deny to see the blocked paths.
	approveDeploy := sparkwing.JobApproval(plan, "approve-deploy", sparkwing.ApprovalConfig{
		Message:  "Promote example:sha-abc1234 to prod?",
		Timeout:  2 * time.Second,
		OnExpiry: sparkwing.ApprovalApprove,
	}).Needs(publishImages)

	// deploy times out after 10s and registers a sibling rollback
	// node that only runs if deploy fails (OnFailure). The rollback
	// is wired but won't fire in the success path.
	deploy := sparkwing.Job(plan, "deploy", &DeployJob{Build: buildRef}).
		Needs(approveDeploy).
		Timeout(10*time.Second).
		OnFailure("deploy-rollback", rollbackFn)

	// GroupJobs: the Plan-layer analogue of GroupSteps. Takes
	// already-constructed sibling Nodes and folds them into a single
	// NodeGroup for collapsible dashboard rendering + one .Needs()
	// target downstream. Distinct from JobFanOut, which synthesizes
	// the members from a slice. Here we hand-write two heterogeneous
	// post-deploy verifications and cluster them.
	smokeTest := sparkwing.Job(plan, "smoke-test", smokeTestFn).Needs(deploy)
	metricsCheck := sparkwing.Job(plan, "metrics-check", metricsCheckFn).Needs(deploy)
	verify := sparkwing.GroupJobs(plan, "post-deploy-verify", smokeTest, metricsCheck)

	// Plan-layer SkipIf: notify is skipped when NO_NOTIFY=1. Inline
	// because the body is a single Slack webhook post.
	sparkwing.Job(plan, "notify", notifyFn).
		Needs(verify).
		Inline().
		SkipIf(func(ctx context.Context) bool {
			return os.Getenv("NO_NOTIFY") == "1"
		}).
		AfterRun(func(ctx context.Context, err error) {
			sparkwing.Annotate(ctx, "notify dispatched")
		})

	return nil
}

// --- Plan-layer node bodies (single closures) ---

func configureFn(ctx context.Context) error {
	if err := chatter(ctx, 800, []string{
		"reading .sparkwing/pipelines.yaml",
		"resolving 14 config keys",
		"merging trigger env (4 keys)",
		"writing $SPARKWING_HOME/config.json",
	}); err != nil {
		return err
	}
	sparkwing.Annotate(ctx, "loaded 14 config keys from .sparkwing/")
	return nil
}

func checkFn(name string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		if err := chatter(ctx, 600, []string{
			fmt.Sprintf("%s: starting", name),
			fmt.Sprintf("%s: scanning workspace", name),
			fmt.Sprintf("%s: applying ruleset", name),
		}); err != nil {
			return err
		}
		sparkwing.Annotate(ctx, fmt.Sprintf("%s: clean", name))
		return nil
	}
}

func publishFn(target string, buildRef sparkwing.Ref[BuildOut]) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		out := buildRef.Get(ctx)
		lines := []string{
			fmt.Sprintf("authenticating to registry for %s", target),
			fmt.Sprintf("inspecting local image %s", out.Tag),
			"pushing layer 1/4 (base os, 18.2 MiB)",
			"pushing layer 2/4 (runtime deps, 7.4 MiB)",
			"pushing layer 3/4 (app binary, 9.7 MiB)",
			"pushing layer 4/4 (config, 0.3 MiB)",
			fmt.Sprintf("signing manifest with cosign (digest %s)", out.Digest),
		}
		if err := chatter(ctx, 1800, lines); err != nil {
			return err
		}
		sparkwing.Annotate(ctx, fmt.Sprintf("%s: pushed %s", target, out.Tag))
		return nil
	}
}

func notifyFn(ctx context.Context) error {
	if err := chatter(ctx, 400, []string{
		"resolving Slack webhook from secrets",
		"posting message to #releases",
	}); err != nil {
		return err
	}
	sparkwing.Annotate(ctx, "Slack #releases · 1 message sent")
	return nil
}

func rollbackFn(ctx context.Context) error {
	sparkwing.Info(ctx, "rolling back to previous deployment")
	return nap(ctx, 200)
}

// offshootFn returns a closure that emits `lines` log lines spread
// over 600ms, then annotates the node with a one-line summary.
// Used for the taller-DAG off-shoot branches hanging off configure.
func offshootFn(id string, lines int) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		msgs := make([]string, lines)
		for i := range msgs {
			msgs[i] = fmt.Sprintf("%s: phase %d/%d", id, i+1, lines)
		}
		if err := chatter(ctx, 600, msgs); err != nil {
			return err
		}
		sparkwing.Annotate(ctx, fmt.Sprintf("%s: ok", id))
		return nil
	}
}

func smokeTestFn(ctx context.Context) error {
	if err := chatter(ctx, 1100, []string{
		"GET /healthz",
		"200 OK · uptime=4s",
		"GET /api/v1/version",
		"200 OK · sha=abc1234",
		"POST /api/v1/echo {ping}",
		"200 OK · {pong}",
	}); err != nil {
		return err
	}
	sparkwing.Annotate(ctx, "smoke-test: 3/3 probes passed")
	return nil
}

func metricsCheckFn(ctx context.Context) error {
	if err := chatter(ctx, 900, []string{
		"querying Prometheus: rate(http_requests_total[1m])",
		"querying Prometheus: histogram_quantile(0.95, http_request_duration_seconds_bucket)",
		"p95 latency: 42ms (threshold 500ms)",
		"error rate: 0.0% (threshold 1%)",
	}); err != nil {
		return err
	}
	sparkwing.Annotate(ctx, "metrics-check: p95=42ms · err=0% · within SLO")
	return nil
}

// --- BuildJob: multi-step Work + typed output ---

type BuildJob struct {
	sparkwing.Base
	sparkwing.Produces[BuildOut]
}

func (j *BuildJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	fetchRefs := chattyStep(w, "fetch-refs", 1200, "fetched 142 objects · 1.3 MiB", []string{
		"connecting to origin (git@github.com:example/app.git)",
		"negotiating protocol v2",
		"enumerating remote refs",
		"counting objects: 142",
		"receiving objects: 142/142 (1.3 MiB)",
		"resolving deltas: 38/38",
	})
	fetchCheckout := chattyStep(w, "fetch-checkout", 400, "worktree clean at sha abc1234", []string{
		"checking out abc1234",
		"git status: clean",
	}).Needs(fetchRefs)

	// GroupSteps: parallel CI gates folded into one collapsible
	// StepGroup. Downstream .Needs(ci) waits for every member.
	ci := sparkwing.GroupSteps(w, "ci",
		chattyStep(w, "lint", 1500, "gofmt: 0 files reformatted", []string{
			"scanning 87 .go files",
			"running gofmt -l",
			"running revive",
			"running misspell",
			"running ineffassign",
			"all linters: clean",
		}).Needs(fetchCheckout),
		chattyStep(w, "vet", 1200, "vet: 12 packages clean", []string{
			"loading package graph",
			"go vet ./pkg/...",
			"go vet ./internal/...",
			"go vet ./cmd/...",
			"vet: 12 packages, 0 findings",
		}).Needs(fetchCheckout),
		// Very chatty: 15 lines over 4.5s — what a "real" test runner feels like.
		chattyStep(w, "test", 4500, "33 tests passed across 3 packages", []string{
			"discovering test packages: 3 found",
			"compiling test binaries",
			"=== RUN   TestStore_CreateRun",
			"--- PASS: TestStore_CreateRun (0.18s)",
			"=== RUN   TestStore_FinishRun",
			"--- PASS: TestStore_FinishRun (0.21s)",
			"=== RUN   TestAPI_GetRun",
			"--- PASS: TestAPI_GetRun (0.34s)",
			"=== RUN   TestAPI_ListNodes",
			"--- PASS: TestAPI_ListNodes (0.27s)",
			"=== RUN   TestWorker_DispatchLoop",
			"--- PASS: TestWorker_DispatchLoop (0.91s)",
			"=== RUN   TestWorker_HeartbeatStall",
			"--- PASS: TestWorker_HeartbeatStall (0.42s)",
			"ok  github.com/example/app/pkg/store, pkg/api, pkg/worker",
		}).Needs(fetchCheckout),
	)

	// Typed step: returns (BuildOut, error). StepGet[BuildOut] reads
	// it from downstream steps in the same Work. Compile is the
	// other "really chatty" step: ~3.5s with one line per phase.
	compile := sparkwing.Step(w, "compile", func(ctx context.Context) (BuildOut, error) {
		lines := []string{
			"loading go.mod",
			"resolving module graph (87 modules)",
			"compiling internal/...",
			"compiling pkg/store",
			"compiling pkg/api",
			"compiling pkg/worker",
			"compiling cmd/app",
			"linking cmd/app",
			"emitting bin/app (14.2 MiB)",
		}
		if err := chatter(ctx, 3500, lines); err != nil {
			return BuildOut{}, err
		}
		out := BuildOut{Tag: "example:sha-abc1234", SizeMB: 14}
		sparkwing.Annotate(ctx, fmt.Sprintf("compiled %s (%d MiB)", out.Tag, out.SizeMB))
		return out, nil
	}).Needs(ci)

	strip := sparkwing.Step(w, "strip", func(ctx context.Context) error {
		out := sparkwing.StepGet[BuildOut](ctx, compile)
		if err := chatter(ctx, 600, []string{
			"reading bin/app",
			"removing debug symbols",
			"removing unused sections",
		}); err != nil {
			return err
		}
		stripped := out.SizeMB - 5
		sparkwing.Annotate(ctx, fmt.Sprintf("stripped %s: %d -> %d MiB", out.Tag, out.SizeMB, stripped))
		return nil
	}).Needs(compile)

	// Result step: its typed return value becomes the Job's Output,
	// which downstream Nodes read via Ref[BuildOut].
	return sparkwing.Step(w, "package", func(ctx context.Context) (BuildOut, error) {
		out := sparkwing.StepGet[BuildOut](ctx, compile)
		if err := chatter(ctx, 900, []string{
			"writing Dockerfile.runtime",
			"copying bin/app into stage",
			"writing layer manifest",
			"computing digest",
		}); err != nil {
			return BuildOut{}, err
		}
		out.Digest = "sha256:0fa1c0ffee..."
		sparkwing.Annotate(ctx, fmt.Sprintf("packaged %s · %s", out.Tag, out.Digest))
		return out, nil
	}).Needs(strip), nil
}

// --- DeployJob: multi-step Work with Work-layer SkipIf ---

type DeployJob struct {
	sparkwing.Base
	Build sparkwing.Ref[BuildOut]
}

func (j *DeployJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	render := sparkwing.Step(w, "render", func(ctx context.Context) error {
		out := j.Build.Get(ctx)
		return chatter(ctx, 700, []string{
			fmt.Sprintf("rendering manifests for %s", out.Tag),
			"templating k8s/deployment.yaml",
			"templating k8s/service.yaml",
			"templating k8s/configmap.yaml",
			"validating with kubeconform",
		})
	})
	// Apply is chatty: one line per resource (~10 lines over 3s).
	apply := sparkwing.Step(w, "apply", func(ctx context.Context) error {
		lines := []string{
			"kubectl apply --server-side -f -",
			"namespace/example-app configured",
			"configmap/example-app-config created",
			"secret/example-app-secret unchanged",
			"serviceaccount/example-app created",
			"role.rbac.authorization.k8s.io/example-app created",
			"rolebinding.rbac.authorization.k8s.io/example-app created",
			"service/example-app created",
			"deployment.apps/example-app created",
		}
		if err := chatter(ctx, 3000, lines); err != nil {
			return err
		}
		sparkwing.Annotate(ctx, "applied 6 manifests (Deployment, Service, ConfigMap, ...)")
		return nil
	}).Needs(render)

	// Rollout is the most chatty step: ~14 status pings over 5s
	// simulating a kubectl rollout watch. Skipped under DRY_RUN=1.
	return sparkwing.Step(w, "rollout", func(ctx context.Context) error {
		lines := []string{
			"watching deployment/example-app",
			"replicas: 0/3 ready, 3 updated, 0 available",
			"pod example-app-7d9c4-abc12 Pending",
			"pod example-app-7d9c4-def34 Pending",
			"pod example-app-7d9c4-ghi56 Pending",
			"pod example-app-7d9c4-abc12 ContainerCreating",
			"pod example-app-7d9c4-def34 ContainerCreating",
			"pod example-app-7d9c4-abc12 Running (readiness pending)",
			"pod example-app-7d9c4-ghi56 ContainerCreating",
			"pod example-app-7d9c4-def34 Running (readiness pending)",
			"pod example-app-7d9c4-abc12 Ready",
			"pod example-app-7d9c4-def34 Ready",
			"pod example-app-7d9c4-ghi56 Running (readiness pending)",
			"pod example-app-7d9c4-ghi56 Ready",
			"replicas: 3/3 ready",
		}
		if err := chatter(ctx, 5000, lines); err != nil {
			return err
		}
		sparkwing.Annotate(ctx, "rolled out 3/3 replicas to kind-local")
		return nil
	}).Needs(apply).SkipIf(func(ctx context.Context) bool {
		return strings.EqualFold(os.Getenv("DRY_RUN"), "1")
	}), nil
}

// chattyStep is the boilerplate for a sleep+log step that emits a
// list of progress lines over its total duration and annotates the
// node with a one-line summary. Used for leaf steps that don't need
// typed output or StepGet plumbing.
func chattyStep(w *sparkwing.Work, id string, totalMs int, summary string, lines []string) *sparkwing.WorkStep {
	return sparkwing.Step(w, id, func(ctx context.Context) error {
		if err := chatter(ctx, totalMs, lines); err != nil {
			return err
		}
		sparkwing.Annotate(ctx, summary)
		return nil
	})
}

// chatter emits one log line per "tick" with ticks evenly spread
// across totalMs. Empty lines list collapses to a single sleep.
// Cancels cleanly via ctx.
func chatter(ctx context.Context, totalMs int, lines []string) error {
	if len(lines) == 0 {
		return nap(ctx, totalMs)
	}
	tick := time.Duration(totalMs) * time.Millisecond / time.Duration(len(lines))
	for _, line := range lines {
		sparkwing.Info(ctx, "%s", line)
		select {
		case <-time.After(tick):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func nap(ctx context.Context, ms int) error {
	select {
	case <-time.After(time.Duration(ms) * time.Millisecond):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func init() {
	sparkwing.Register("example", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Example{} })
}
