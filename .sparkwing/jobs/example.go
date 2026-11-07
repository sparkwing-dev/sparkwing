package jobs

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Example struct{ sparkwing.Base }

func (Example) ShortHelp() string {
	return "Staging integration gate and runnable SDK tour"
}

func (Example) Help() string {
	return "Runs the SDK tour alongside named integration assertions for matrix, job and step outputs, generated jobs, dynamic child pipelines, scoped secrets, OIDC, dependency cache restore, memo reuse, artifacts, cancellation and API logs/history. The optional error canary remains a dashboard demonstration. Secrets, OIDC and the CLI lifecycle probes skip with a reason when unavailable; the probes need --cli runnable on the job host. Docker runs on capable runners. Pass --fail-once to exercise failed-only retry and predecessor outputs. Pass --smoke-profile plus --smoke-checkout to trigger and await sparkwing-ci-smoke as a separate run. Memoization belongs to jobs, so the memoized job contains one step. The default local run needs no Cloud or Docker."
}

func (Example) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Run the local integration gate and SDK tour", Command: "sparkwing run example"},
		{Comment: "Assert failed-only retry and predecessor outputs", Command: "sparkwing run example -- --fail-once"},
		{Comment: "Include the second repository on staging", Command: "sparkwing run example -- --smoke-profile staging --smoke-checkout /path/to/sparkwing-ci-smoke"},
		{Comment: "Read the optional declared secret", Command: "sparkwing run example -- --secret-name EXAMPLE_GATE_SECRET"},
		{Comment: "Render the DAG without running", Command: "sparkwing pipeline plan --pipeline example"},
		{Comment: "Skip the rollout step via Work-layer SkipIf", Command: "DRY_RUN=1 sparkwing run example"},
	}
}

type BuildOut struct {
	Tag    string `json:"tag"`
	Digest string `json:"digest"`
	SizeMB int    `json:"size_mb"`
}

func (p *Example) Plan(_ context.Context, plan *sparkwing.Plan, in ExampleInputs, rc sparkwing.RunContext) error {
	features := exampleIntegration(plan, in, rc)
	configure := sparkwing.Job(plan, "configure", configureFn).
		Inline().
		BeforeRun(func(ctx context.Context) error {
			sparkwing.Info(ctx, "BeforeRun: warming workspace")
			return nil
		}).
		AfterRun(func(ctx context.Context, err error) {
			sparkwing.Info(ctx, "AfterRun: cleanup (err=%v)", err)
		})

	checks := sparkwing.JobFanOut(plan, "checks",
		[]string{"audit-secrets", "validate-config", "scan-deps"},
		func(name string) (string, any) {
			return name, checkFn(name)
		}).Needs(configure)

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

	sparkwing.Job(plan, "weather-check", weatherCheckFn).Needs(configure).Inline().StartsChildren("weather-report").Timeout(5 * time.Minute)

	sparkwing.Job(plan, "error-canary", &ExampleErrorCanary{}).Needs(configure).Optional()

	build := sparkwing.Job(plan, "build", &ExampleBuild{}).
		Needs(checks).
		Retry(1, sparkwing.Backoff(200*time.Millisecond))
	buildRef := sparkwing.RefTo[BuildOut](build)

	publishImages := sparkwing.JobFanOut(plan, "publish-images",
		[]string{"linux-amd64", "linux-arm64", "darwin-arm64"},
		func(target string) (string, any) {
			return "publish-" + target, publishFn(target, buildRef)
		}).Needs(build)

	approveDeploy := plan.Approval("approve-deploy", sparkwing.ApprovalConfig{
		Message:   "Promote example:sha-abc1234 to prod?",
		Timeout:   time.Second,
		OnTimeout: sparkwing.ApprovalApprove,
	}).Needs(publishImages)

	deploy := sparkwing.Job(plan, "deploy", &ExampleDeploy{Build: buildRef}).
		Needs(approveDeploy).
		Timeout(30 * time.Second)
	deploy.OnFailure("deploy-rollback", rollbackFn)

	smokeTest := sparkwing.Job(plan, "smoke-test", smokeTestFn).Needs(deploy)
	metricsCheck := sparkwing.Job(plan, "metrics-check", metricsCheckFn).Needs(deploy)
	verify := sparkwing.GroupJobs(plan, "post-deploy-verify", smokeTest, metricsCheck)

	notify := sparkwing.Job(plan, "notify", notifyFn).
		Needs(verify).
		Inline().
		SkipIf(func(ctx context.Context) bool {
			return os.Getenv("NO_NOTIFY") == "1"
		}).
		AfterRun(func(ctx context.Context, err error) {
			sparkwing.Annotate(ctx, sparkwing.Annotation{Message: "notify dispatched"})
		})

	reference := sparkwing.Job(plan, "reference-pipeline", exampleReference(in)).Needs(features, notify).Inline()
	if in.SmokeProfile == "" {
		reference.SkipIf(exampleSkip("no --smoke-profile configured for the separate sparkwing-ci-smoke run"))
	}
	return nil
}

func configureFn(ctx context.Context) error {
	if err := chatter(ctx, []string{
		"reading .sparkwing/sparkwing.yaml",
		"resolving 14 config keys",
		"merging trigger env (4 keys)",
		"writing $SPARKWING_HOME/config.json",
	}); err != nil {
		return err
	}
	sparkwing.Annotate(ctx, sparkwing.Annotation{Message: "loaded 14 config keys from .sparkwing/"})
	return nil
}

func checkFn(name string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		if err := chatter(ctx, []string{
			fmt.Sprintf("%s: starting", name),
			fmt.Sprintf("%s: scanning workspace", name),
			fmt.Sprintf("%s: applying ruleset", name),
		}); err != nil {
			return err
		}
		sparkwing.Annotate(ctx, sparkwing.Annotation{Message: fmt.Sprintf("%s: clean", name)})
		return nil
	}
}

func publishFn(target string, buildRef sparkwing.Ref[BuildOut]) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		out := buildRef.Get(ctx)
		if out.Tag != "example:sha-abc1234" || out.SizeMB != 14 || out.Digest == "" {
			return fmt.Errorf("publish-%s: invalid build output: %+v", target, out)
		}
		lines := []string{
			fmt.Sprintf("authenticating to registry for %s", target),
			fmt.Sprintf("inspecting local image %s", out.Tag),
			"pushing layer 1/4 (base os, 18.2 MiB)",
			"pushing layer 2/4 (runtime deps, 7.4 MiB)",
			"pushing layer 3/4 (app binary, 9.7 MiB)",
			"pushing layer 4/4 (config, 0.3 MiB)",
			fmt.Sprintf("signing manifest with cosign (digest %s)", out.Digest),
		}
		if err := chatter(ctx, lines); err != nil {
			return err
		}
		sparkwing.Annotate(ctx, sparkwing.Annotation{Message: fmt.Sprintf("%s: pushed %s", target, out.Tag)})
		return nil
	}
}

func notifyFn(ctx context.Context) error {
	if err := chatter(ctx, []string{
		"resolving Slack webhook from secrets",
		"posting message to #releases",
	}); err != nil {
		return err
	}
	sparkwing.Annotate(ctx, sparkwing.Annotation{Message: "Slack #releases · 1 message sent"})
	return nil
}

func rollbackFn(ctx context.Context) error {
	sparkwing.Info(ctx, "rolling back to previous deployment")
	return nap(ctx, 200)
}

type ExampleErrorCanary struct{ sparkwing.Base }

func (j *ExampleErrorCanary) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	w.FailurePolicy(sparkwing.CollectAll)
	sparkwing.Step(w, "shard-alpha", canaryShard("shard-alpha", 60, 2500,
		"connection reset by peer at item 042 (after 3 retries)"))
	sparkwing.Step(w, "shard-bravo", canaryShard("shard-bravo", 55, 2200,
		"timeout waiting for downstream service (10s deadline)"))
	sparkwing.Step(w, "shard-charlie", canaryShard("shard-charlie", 70, 2800,
		"checksum mismatch on item 051: want sha256:abc... got sha256:def..."))
	sparkwing.Step(w, "shard-delta", canaryShard("shard-delta", 50, 2000,
		"out of memory: allocated 1.4GiB of 1.0GiB quota"))
	return nil, nil
}

func canaryShard(label string, items, durMS int, fail string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		tick := time.Duration(durMS) * time.Millisecond / time.Duration(items)
		warnStart := items - 14
		errorStart := items - 4
		if warnStart < 1 {
			warnStart = 1
		}
		if errorStart < 1 {
			errorStart = 1
		}
		for i := 1; i <= items; i++ {
			switch {
			case i >= errorStart:
				sparkwing.Error(ctx, "%s: item %03d/%03d ... FAILED (%s)", label, i, items, fail)
			case i >= warnStart:
				sparkwing.Warn(ctx, "%s: item %03d/%03d ... slow (retry queue depth=%d)", label, i, items, i-warnStart+1)
			default:
				sparkwing.Info(ctx, "%s: item %03d/%03d ... ok", label, i, items)
			}
			select {
			case <-time.After(tick):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		sparkwing.Error(ctx, "%s: aborting batch -- too many failures", label)
		sparkwing.Annotate(ctx, sparkwing.Annotation{Message: fmt.Sprintf("%s: %d items processed before failure", label, items)})
		return fmt.Errorf("%s: %s", label, fail)
	}
}

func weatherCheckFn(ctx context.Context) error {
	sparkwing.Info(ctx, "calling weather-report pipeline via RunAndAwait")
	out, err := sparkwing.RunAndAwait[WeatherOut, sparkwing.NoInputs](
		ctx, "weather-report", "stamp",
	)
	if err != nil {
		return fmt.Errorf("weather-report: %w", err)
	}
	if out.Forecast == "" || out.TempF < 55 || out.TempF >= 90 || out.At.IsZero() {
		return fmt.Errorf("weather-check: invalid output: %+v", out)
	}
	sparkwing.Info(ctx, "weather-report returned: %s, %d°F", out.Forecast, out.TempF)
	sparkwing.Annotate(ctx, sparkwing.Annotation{Message: fmt.Sprintf("weather: %s · %d°F (from weather-report pipeline)", out.Forecast, out.TempF)})
	return nil
}

func offshootFn(id string, lines int) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		msgs := make([]string, lines)
		for i := range msgs {
			msgs[i] = fmt.Sprintf("%s: phase %d/%d", id, i+1, lines)
		}
		if err := chatter(ctx, msgs); err != nil {
			return err
		}
		sparkwing.Annotate(ctx, sparkwing.Annotation{Message: fmt.Sprintf("%s: ok", id)})
		return nil
	}
}

func smokeTestFn(ctx context.Context) error {
	if err := chatter(ctx, []string{
		"GET /healthz",
		"200 OK · uptime=4s",
		"GET /api/v1/version",
		"200 OK · sha=abc1234",
		"POST /api/v1/echo {ping}",
		"200 OK · {pong}",
	}); err != nil {
		return err
	}
	sparkwing.Annotate(ctx, sparkwing.Annotation{Message: "smoke-test: 3/3 probes passed"})
	sparkwing.Summary(ctx, `## Smoke test

- [x] `+"`GET /healthz`"+` → 200 OK (uptime 4s)
- [x] `+"`GET /api/v1/version`"+` → 200 OK (sha abc1234)
- [x] `+"`POST /api/v1/echo`"+` → 200 OK

**3/3 probes passed.**`)
	return nil
}

func metricsCheckFn(ctx context.Context) error {
	if err := chatter(ctx, []string{
		"querying Prometheus: rate(http_requests_total[1m])",
		"querying Prometheus: histogram_quantile(0.95, http_request_duration_seconds_bucket)",
		"p95 latency: 42ms (threshold 500ms)",
		"error rate: 0.0% (threshold 1%)",
	}); err != nil {
		return err
	}
	sparkwing.Annotate(ctx, sparkwing.Annotation{Message: "metrics-check: p95=42ms · err=0% · within SLO"})
	sparkwing.Summary(ctx, `## Metrics check

| signal | value | threshold | status |
|---|---|---|---|
| p95 latency | 42ms | 500ms | ✓ |
| error rate | 0.0% | 1.0% | ✓ |

Within SLO.`)
	return nil
}

type ExampleBuild struct {
	sparkwing.Base
	sparkwing.Produces[BuildOut]
}

func (j *ExampleBuild) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	fetchRefs := chattyStep(w, "fetch-refs", "fetched 142 objects · 1.3 MiB", []string{
		"connecting to origin (git@github.com:example/app.git)",
		"negotiating protocol v2",
		"enumerating remote refs",
		"counting objects: 142",
		"receiving objects: 142/142 (1.3 MiB)",
		"resolving deltas: 38/38",
	})
	fetchCheckout := chattyStep(w, "fetch-checkout", "worktree clean at sha abc1234", []string{
		"checking out abc1234",
		"git status: clean",
	}).Needs(fetchRefs)

	ci := sparkwing.GroupSteps(
		w, "ci",
		chattyStep(w, "lint", "gofmt: 0 files reformatted", []string{
			"scanning 87 .go files",
			"running gofmt -l",
			"running revive",
			"running misspell",
			"running ineffassign",
			"all linters: clean",
		}).Needs(fetchCheckout),
		chattyStep(w, "vet", "vet: 12 packages clean", []string{
			"loading package graph",
			"go vet ./pkg/...",
			"go vet ./internal/...",
			"go vet ./cmd/...",
			"vet: 12 packages, 0 findings",
		}).Needs(fetchCheckout),
		chattyStep(w, "test", "33 tests passed across 3 packages", []string{
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
		if err := chatter(ctx, lines); err != nil {
			return BuildOut{}, err
		}
		out := BuildOut{Tag: "example:sha-abc1234", SizeMB: 14}
		sparkwing.Annotate(ctx, sparkwing.Annotation{Message: fmt.Sprintf("compiled %s (%d MiB)", out.Tag, out.SizeMB)})
		return out, nil
	}).Needs(ci)

	strip := sparkwing.Step(w, "strip", func(ctx context.Context) error {
		out := sparkwing.StepGet[BuildOut](ctx, compile)
		if err := chatter(ctx, []string{
			"reading bin/app",
			"removing debug symbols",
			"removing unused sections",
		}); err != nil {
			return err
		}
		stripped := out.SizeMB - 5
		sparkwing.Annotate(ctx, sparkwing.Annotation{Message: fmt.Sprintf("stripped %s: %d -> %d MiB", out.Tag, out.SizeMB, stripped)})
		return nil
	}).Needs(compile)

	return sparkwing.Step(w, "package", func(ctx context.Context) (BuildOut, error) {
		out := sparkwing.StepGet[BuildOut](ctx, compile)
		if err := chatter(ctx, []string{
			"writing Dockerfile.runtime",
			"copying bin/app into stage",
			"writing layer manifest",
			"computing digest",
		}); err != nil {
			return BuildOut{}, err
		}
		out.Digest = "sha256:0fa1c0ffee..."
		sparkwing.Annotate(ctx, sparkwing.Annotation{Message: fmt.Sprintf("packaged %s · %s", out.Tag, out.Digest)})
		sparkwing.Summary(ctx, fmt.Sprintf(`## Build artifact

**Image** `+"`%s`"+`
**Digest** `+"`%s`"+`
**Size** %d MiB (stripped from %d MiB)

### Layers
| layer | size |
|---|---|
| base os | 18.2 MiB |
| runtime deps | 7.4 MiB |
| app binary | 9.7 MiB |
| config | 0.3 MiB |`,
			out.Tag, out.Digest, out.SizeMB-5, out.SizeMB))
		return out, nil
	}).Needs(strip), nil
}

type ExampleDeploy struct {
	sparkwing.Base
	Build sparkwing.Ref[BuildOut]
}

func (j *ExampleDeploy) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	render := sparkwing.Step(w, "render", func(ctx context.Context) error {
		out := j.Build.Get(ctx)
		return chatter(ctx, []string{
			fmt.Sprintf("rendering manifests for %s", out.Tag),
			"templating k8s/deployment.yaml",
			"templating k8s/service.yaml",
			"templating k8s/configmap.yaml",
			"validating with kubeconform",
		})
	})
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
		if err := chatter(ctx, lines); err != nil {
			return err
		}
		sparkwing.Annotate(ctx, sparkwing.Annotation{Message: "applied 6 manifests (Deployment, Service, ConfigMap, ...)"})
		return nil
	}).Needs(render)

	return sparkwing.Step(w, "rollout", func(ctx context.Context) error {
		out := j.Build.Get(ctx)
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
		if err := chatter(ctx, lines); err != nil {
			return err
		}
		sparkwing.Annotate(ctx, sparkwing.Annotation{Message: "rolled out 3/3 replicas to kind-local"})
		sparkwing.Summary(ctx, fmt.Sprintf(`## Rollout complete

**Image** `+"`%s`"+`
**Digest** `+"`%s`"+`
**Size** %d MiB · stripped

### Pods
| name | status |
|---|---|
| example-app-7d9c4-abc12 | Ready |
| example-app-7d9c4-def34 | Ready |
| example-app-7d9c4-ghi56 | Ready |

3/3 replicas ready on **kind-local**.`,
			out.Tag, out.Digest, out.SizeMB))
		return nil
	}).Needs(apply).SkipIf(func(ctx context.Context) bool {
		return strings.EqualFold(os.Getenv("DRY_RUN"), "1")
	}), nil
}

func chattyStep(w *sparkwing.Work, id, summary string, lines []string) *sparkwing.WorkStep {
	return sparkwing.Step(w, id, func(ctx context.Context) error {
		if err := chatter(ctx, lines); err != nil {
			return err
		}
		sparkwing.Annotate(ctx, sparkwing.Annotation{Message: summary})
		return nil
	})
}

func chatter(ctx context.Context, lines []string) error {
	for _, line := range lines {
		if err := ctx.Err(); err != nil {
			return err
		}
		sparkwing.Info(ctx, "%s", line)
	}
	return ctx.Err()
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
	sparkwing.Register("Example", func() sparkwing.Pipeline[ExampleInputs] { return &Example{} })
}
