package jobs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/cleanup"
	"github.com/sparkwing-dev/sparkwing/sparkwing/docker"
	"github.com/sparkwing-dev/sparkwing/sparkwing/inputs"
)

type ExampleInputs struct {
	SecretName    string `flag:"secret-name" default:"EXAMPLE_GATE_SECRET" enum:"EXAMPLE_GATE_SECRET" help:"Optional declared secret to resolve without logging its value"`
	FailOnce      bool   `flag:"fail-once" help:"Exercise failed-only retry with a deterministic first-attempt failure"`
	CLI           string `flag:"cli" default:"sparkwing" help:"CLI built from the same source as the running engine"`
	APIProfile    string `flag:"api-profile" help:"Controller profile for lifecycle probes, empty for the local controller"`
	SmokeProfile  string `flag:"smoke-profile" help:"Target environment profile for the separate reference run"`
	SmokeCheckout string `flag:"smoke-checkout" help:"Checkout of sparkwing-ci-smoke used by the trigger CLI"`
	SmokePipeline string `flag:"smoke-pipeline" default:"reference" help:"Reference pipeline containing hello and docker-test"`
	ChildBranch   string `flag:"child-branch" help:"Optional same-repository branch tip for the dynamic child"`
}

type exampleSecrets struct {
	Value string `sw:"EXAMPLE_GATE_SECRET,optional"`
}

func (Example) Secrets() any { return &exampleSecrets{} }

func exampleSkip(reason string) sparkwing.SkipPredicate {
	return func(ctx context.Context) bool {
		sparkwing.Annotate(ctx, sparkwing.Annotation{Message: "SKIP: " + reason})
		return true
	}
}

func exampleNeedsCLI(in ExampleInputs) sparkwing.SkipPredicate {
	return func(ctx context.Context) bool {
		if _, err := exec.LookPath(in.CLI); err != nil {
			return exampleSkip(fmt.Sprintf("CLI %q is not runnable on this host; pass --cli with a sparkwing binary to run lifecycle probes", in.CLI))(ctx)
		}
		return false
	}
}

func exampleIntegration(plan *sparkwing.Plan, in ExampleInputs, rc sparkwing.RunContext) *sparkwing.JobGroup {
	var all []*sparkwing.JobNode
	var matrix []*sparkwing.JobNode
	for _, osName := range []string{"linux", "darwin"} {
		for _, arch := range []string{"amd64", "arm64"} {
			value := osName + "/" + arch
			node := sparkwing.Job(plan, "matrix-"+osName+"-"+arch, &exampleValue{Value: value})
			matrix = append(matrix, node)
		}
	}
	matrixAssert := sparkwing.Job(plan, "matrix-assert", func(ctx context.Context) error {
		for i, node := range matrix {
			want := []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"}[i]
			if got := sparkwing.RefTo[string](node).Get(ctx); got != want {
				return fmt.Errorf("matrix %s: got %q, want %q", node.ID(), got, want)
			}
		}
		return nil
	}).Needs(sparkwing.GroupJobs(plan, "matrix", matrix...))
	all = append(all, matrixAssert)
	source := sparkwing.Job(plan, "output-source", &exampleValue{Value: "example-output-v1"})
	outputs := sparkwing.Job(plan, "outputs-assert", &exampleOutputJob{Source: sparkwing.RefTo[string](source)}).Needs(source)
	all = append(all, outputs)
	generator := source.Generate(func(ctx context.Context, generated *sparkwing.Plan) error {
		value := sparkwing.RefTo[string](source).Get(ctx)
		sparkwing.Job(generated, "generated-assert", func(context.Context) error {
			if value != "example-output-v1" {
				return fmt.Errorf("generated source output: %q", value)
			}
			return nil
		})
		return nil
	})
	all = append(all, sparkwing.Job(plan, "dynamic-child", func(ctx context.Context) error {
		pipeline := strings.Join([]string{"weather", "report"}, "-")
		var opts []sparkwing.AwaitOption
		if in.ChildBranch != "" {
			if rc.Git == nil || rc.Git.Repo == "" {
				return fmt.Errorf("dynamic-child: --child-branch needs repository provenance")
			}
			opts = append(opts, sparkwing.WithFreshRepo(rc.Git.Repo), sparkwing.WithFreshBranch(in.ChildBranch))
		}
		out, err := sparkwing.RunAndAwait[WeatherOut, sparkwing.NoInputs](ctx, pipeline, "stamp", opts...)
		if err != nil {
			return err
		}
		if out.Forecast == "" || out.At.IsZero() {
			return fmt.Errorf("dynamic child: missing weather output")
		}
		return nil
	}).NeedsGenerated(generator).StartsChildren("weather-report").Inline().Timeout(5*time.Minute))
	secret := sparkwing.Job(plan, "scoped-secrets", func(ctx context.Context) error {
		value, err := sparkwing.Secret(ctx, in.SecretName)
		if err != nil {
			return err
		}
		if value == "" {
			return fmt.Errorf("scoped-secrets: configured secret is empty")
		}
		sparkwing.Annotate(ctx, sparkwing.Annotation{Message: "scoped secret resolved and masked"})
		return nil
	}).SkipIf(func(ctx context.Context) bool {
		if in.SecretName == "" {
			return exampleSkip("no --secret-name configured")(ctx)
		}
		_, err := sparkwing.Secret(ctx, in.SecretName)
		if errors.Is(err, sparkwing.ErrSecretMissing) {
			return exampleSkip("selected optional secret is not stored")(ctx)
		}
		return false
	})
	all = append(all, secret)
	all = append(all, sparkwing.Job(plan, "oidc-claims", func(ctx context.Context) error {
		token, err := sparkwing.OIDCToken(ctx, "sparkwing-example-gate")
		if err != nil {
			return err
		}
		return exampleVerifyClaims(token, rc.RunID, rc.Pipeline)
	}).SkipIf(func(ctx context.Context) bool {
		_, err := sparkwing.OIDCToken(ctx, "sparkwing-example-gate")
		if errors.Is(err, sparkwing.ErrOIDCUnavailable) {
			return exampleSkip("this run has no OIDC issuer")(ctx)
		}
		return false
	}))
	cachePath := filepath.Join(".sparkwing/cache/example-deps", rc.RunID)
	cache := sparkwing.Dir(cachePath, sparkwing.KeyFromFile(".sparkwing/go.sum"))
	reset := sparkwing.Job(plan, "dependency-cache-reset", func(context.Context) error { return os.RemoveAll(sparkwing.Path(cachePath)) }).Inline()
	seed := sparkwing.Job(plan, "dependency-cache-seed", func(context.Context) error {
		return exampleWriteFile(filepath.Join(cachePath, "proof"), rc.RunID)
	}).Needs(reset).Cache(cache)
	clearCache := sparkwing.Job(plan, "dependency-cache-clear", func(context.Context) error { return os.RemoveAll(sparkwing.Path(cachePath)) }).Needs(seed).Inline()
	all = append(all, sparkwing.Job(plan, "dependency-cache-assert", func(context.Context) error {
		return exampleFileEquals(filepath.Join(cachePath, "proof"), rc.RunID)
	}).Needs(clearCache).Cache(cache))
	artifactPath := ".sparkwing/cache/example-artifact/proof"
	producer := sparkwing.Job(plan, "artifact-producer", func(context.Context) error { return exampleWriteFile(artifactPath, rc.RunID) }).Artifacts(artifactPath)
	all = append(all, sparkwing.Job(plan, "artifacts-assert", func(context.Context) error {
		return exampleFileEquals(filepath.Join(".sparkwing/cache/example-consumed", artifactPath), rc.RunID)
	}).Consumes(producer, sparkwing.Into(".sparkwing/cache/example-consumed")))
	all = append(all, sparkwing.Job(plan, "memoized-step", exampleMemoCheck(in, rc)).Inline().Timeout(90*time.Second).SkipIf(exampleNeedsCLI(in)))
	retry := sparkwing.Job(plan, "failed-only-retry", exampleRetryCheck(in, rc)).Inline().Timeout(90 * time.Second)
	if !in.FailOnce {
		retry.SkipIf(exampleSkip("pass --fail-once to exercise the expected first-attempt failure"))
	} else {
		retry.SkipIf(exampleNeedsCLI(in))
	}
	all = append(all, retry)
	all = append(all, sparkwing.Job(plan, "cancellation", exampleCancelCheck(in, rc)).Inline().Timeout(90*time.Second).SkipIf(exampleNeedsCLI(in)))
	all = append(all, sparkwing.Job(plan, "logs-history", exampleLogsHistory(in, rc)).Inline().Timeout(90*time.Second).SkipIf(exampleNeedsCLI(in)))
	all = append(all, sparkwing.Job(plan, "docker-assert", func(ctx context.Context) error {
		var out bytes.Buffer
		if err := docker.Run(ctx, docker.RunOptions{Image: "alpine:3.22", Cmd: []string{"printf", "example-docker-ok"}, Stdout: &out}); err != nil {
			return err
		}
		if out.String() != "example-docker-ok" {
			return fmt.Errorf("docker output: %q", out.String())
		}
		return nil
	}).SkipIf(func(ctx context.Context) bool {
		runner := sparkwing.Runner(ctx)
		if runner != nil && (runner.Type == "cloud" || runner.HasLabel("docker") || runner.HasLabel("docker=true")) {
			return false
		}
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		_, err := sparkwing.Exec(probeCtx, "docker", "info", "--format", "{{.ServerVersion}}").Run()
		if err != nil {
			return exampleSkip("runner has no usable Docker daemon or Docker capability")(ctx)
		}
		return false
	}))
	return sparkwing.GroupJobs(plan, "integration-assertions", all...)
}

type exampleValue struct {
	sparkwing.Base
	sparkwing.Produces[string]
	Value string
	Log   string
}

func (j *exampleValue) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "value", func(ctx context.Context) (string, error) {
		if j.Log != "" {
			sparkwing.Info(ctx, "%s", j.Log)
		}
		return j.Value, nil
	}), nil
}

type exampleOutputJob struct {
	sparkwing.Base
	sparkwing.Produces[string]
	Source sparkwing.Ref[string]
}

func (j *exampleOutputJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	first := sparkwing.Step(w, "predecessor", func(ctx context.Context) (string, error) { return j.Source.Get(ctx) + "/step", nil })
	return sparkwing.Step(w, "assert", func(ctx context.Context) (string, error) {
		value := sparkwing.StepGet[string](ctx, first)
		if value != "example-output-v1/step" {
			return "", fmt.Errorf("predecessor output: %q", value)
		}
		if j.Source.Get(ctx) != "example-output-v1" {
			return "", fmt.Errorf("job output changed")
		}
		return value, nil
	}).Needs(first), nil
}

func exampleWriteFile(path, value string) error {
	if err := os.MkdirAll(filepath.Dir(sparkwing.Path(path)), 0o700); err != nil {
		return err
	}
	return sparkwing.WriteFile(path, []byte(value))
}

func exampleFileEquals(path, want string) error {
	data, err := sparkwing.ReadFile(path)
	if err != nil {
		return err
	}
	if string(data) != want {
		return fmt.Errorf("%s: got %q, want %q", path, data, want)
	}
	return nil
}

func exampleVerifyClaims(token, run, pipeline string) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return fmt.Errorf("OIDC token is not a JWT")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("OIDC claims: %w", err)
	}
	var claims struct {
		Audience string `json:"aud"`
		Issuer   string `json:"iss"`
		Subject  string `json:"sub"`
		Run      string `json:"run_id"`
		Pipeline string `json:"pipeline"`
		Expiry   int64  `json:"exp"`
		Issued   int64  `json:"iat"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return fmt.Errorf("OIDC claims: %w", err)
	}
	if claims.Audience != "sparkwing-example-gate" || claims.Run != run || claims.Pipeline != pipeline || claims.Issuer == "" || claims.Subject == "" || claims.Expiry <= time.Now().Unix() || claims.Issued > time.Now().Unix() || claims.Expiry <= claims.Issued {
		return fmt.Errorf("OIDC claims do not match this run, audience or validity window")
	}
	return nil
}

type exampleRunDetail struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Jobs   []struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Outcome string `json:"outcome"`
		Attempt int    `json:"attempt"`
	} `json:"jobs"`
	Retry *struct {
		Mode   string   `json:"mode"`
		Reused []string `json:"reused"`
	} `json:"retry"`
}

func exampleCLI(ctx context.Context, in ExampleInputs, args ...string) (string, error) {
	result, err := sparkwing.Exec(ctx, in.CLI, args...).Capture()
	if err != nil {
		return "", fmt.Errorf("example CLI %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(result.Stdout), nil
}

type exampleProbeRun struct {
	id      string
	release func()
}

func (r exampleProbeRun) await(ctx context.Context, in ExampleInputs) (exampleRunDetail, error) {
	detail, err := exampleStatus(ctx, in, r.id, true)
	if err == nil && (detail.Status == "success" || detail.Status == "failed" || detail.Status == "cancelled") {
		r.release()
	}
	return detail, err
}

func exampleStart(ctx context.Context, in ExampleInputs, token string, extra ...string) (exampleProbeRun, error) {
	args := []string{"-C", sparkwing.WorkDir()}
	if in.APIProfile == "" {
		args = append(args, "run", "example-probe", "--sw-detached", "-o", "plain")
	} else {
		args = append(args, "pipeline", "trigger", "example-probe", "--profile", in.APIProfile, "--detach", "-o", "plain")
	}
	args = append(args, "--", "--token", token, "--cli", in.CLI, "--api-profile", in.APIProfile)
	args = append(args, extra...)
	id, err := exampleCLI(ctx, in, args...)
	if err != nil {
		return exampleProbeRun{}, err
	}
	return exampleTrackRun(ctx, in, id)
}

func exampleTrackRun(ctx context.Context, in ExampleInputs, id string) (exampleProbeRun, error) {
	args := []string{in.CLI, "runs", "cancel", "--run", id, "--profile=" + in.APIProfile}
	// safety: detached probes outlive their SDK process, so the host owns cancellation until a terminal observation.
	release, err := cleanup.Register(ctx, cleanup.Spec{Argv: args, Description: "Cancel example gate probe"})
	if err != nil {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, stopErr := exampleCLI(stopCtx, in, args[1:]...)
		return exampleProbeRun{}, errors.Join(err, stopErr)
	}
	return exampleProbeRun{id: id, release: release}, nil
}

func exampleStatus(ctx context.Context, in ExampleInputs, id string, follow bool) (exampleRunDetail, error) {
	args := []string{"runs", "status", "--run", id, "--profile=" + in.APIProfile, "--exit-zero", "-o", "json"}
	if follow {
		args = append(args, "--follow")
	}
	result, err := exampleObservation(ctx, in, args)
	if err != nil {
		return exampleRunDetail{}, err
	}
	return exampleDecodeStatus(result)
}

func exampleObservation(ctx context.Context, in ExampleInputs, args []string) (string, error) {
	result, err := sparkwing.Exec(ctx, in.CLI, args...).Capture()
	// safety: incomplete observations still carry current job attempts and persisted log records.
	var exit *sparkwing.ExecError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode == 3) {
		return "", err
	}
	return result.Stdout, nil
}

func exampleDecodeStatus(result string) (exampleRunDetail, error) {
	decoder := json.NewDecoder(strings.NewReader(result))
	var last exampleRunDetail
	for {
		var detail exampleRunDetail
		err := decoder.Decode(&detail)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return last, fmt.Errorf("decode CLI run status: %w", err)
		}
		last = detail
	}
	if last.ID == "" {
		return last, fmt.Errorf("CLI returned no run detail")
	}
	return last, nil
}

func exampleMemoCheck(in ExampleInputs, rc sparkwing.RunContext) func(context.Context) error {
	return func(ctx context.Context) error {
		for i := range 2 {
			run, err := exampleStart(ctx, in, rc.RunID+"-memo")
			if err != nil {
				return err
			}
			detail, err := run.await(ctx, in)
			if err != nil {
				return err
			}
			if detail.Status != "success" {
				return fmt.Errorf("memo probe %d: %s", i, detail.Status)
			}
			if i == 1 {
				found := false
				for _, job := range detail.Jobs {
					if job.ID == "memo" {
						found = job.Outcome == "memoized"
					}
				}
				if !found {
					return fmt.Errorf("memoized-step: second run did not reuse its memo job")
				}
			}
		}
		return nil
	}
}

func exampleRetryCheck(in ExampleInputs, rc sparkwing.RunContext) func(context.Context) error {
	return func(ctx context.Context) error {
		run, err := exampleStart(ctx, in, rc.RunID+"-retry", "--fail-once")
		if err != nil {
			return err
		}
		detail, err := run.await(ctx, in)
		if err != nil {
			return err
		}
		if detail.Status != "failed" {
			return fmt.Errorf("failed-only-retry: initial status %s, want failed", detail.Status)
		}
		retryID, err := exampleCLI(ctx, in, "runs", "retry", "--run", run.id, "--failed", "--profile="+in.APIProfile, "-o", "plain")
		if err != nil {
			return err
		}
		retry, err := exampleTrackRun(ctx, in, retryID)
		if err != nil {
			return err
		}
		retried, err := retry.await(ctx, in)
		if err != nil {
			return err
		}
		if retried.Status != "success" || retried.Retry == nil || retried.Retry.Mode != "failed" {
			return fmt.Errorf("failed-only-retry: retry did not succeed with failed mode")
		}
		for _, want := range []string{"source", "memo"} {
			found := false
			for _, reused := range retried.Retry.Reused {
				if reused == want {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("failed-only-retry: predecessor %s was not reused", want)
			}
		}
		return nil
	}
}

func exampleCancelCheck(in ExampleInputs, rc sparkwing.RunContext) func(context.Context) error {
	return func(ctx context.Context) error {
		run, err := exampleStart(ctx, in, rc.RunID+"-cancel", "--long")
		if err != nil {
			return err
		}
		for {
			detail, err := exampleStatus(ctx, in, run.id, false)
			if err != nil {
				return err
			}
			running := false
			for _, job := range detail.Jobs {
				if job.ID == "long" && job.Status == "running" {
					running = true
				}
			}
			if running {
				logs, err := exampleObservation(ctx, in, []string{"runs", "logs", "--run", run.id, "--profile=" + in.APIProfile, "-o", "json"})
				if err != nil {
					return err
				}
				if strings.Contains(logs, "example-probe-long-running") {
					break
				}
			}
			if err := nap(ctx, 100); err != nil {
				return fmt.Errorf("long job never started: %w", err)
			}
		}
		if _, err := exampleCLI(ctx, in, "runs", "cancel", "--run", run.id, "--profile="+in.APIProfile); err != nil {
			return err
		}
		detail, err := run.await(ctx, in)
		if err != nil {
			return err
		}
		if detail.Status != "cancelled" {
			return fmt.Errorf("cancellation: got %s", detail.Status)
		}
		for _, job := range detail.Jobs {
			if job.ID == "long" && job.Outcome == "cancelled" {
				return nil
			}
		}
		return fmt.Errorf("cancellation: long job did not report cancelled")
	}
}

func exampleLogsHistory(in ExampleInputs, rc sparkwing.RunContext) func(context.Context) error {
	return func(ctx context.Context) error {
		run, err := exampleStart(ctx, in, rc.RunID+"-logs")
		if err != nil {
			return err
		}
		detail, err := run.await(ctx, in)
		if err != nil {
			return err
		}
		if detail.Status != "success" {
			return fmt.Errorf("logs-history: probe ended %s", detail.Status)
		}
		logs, err := exampleCLI(ctx, in, "runs", "logs", "--run", run.id, "--profile="+in.APIProfile, "--follow", "-o", "json")
		if err != nil {
			return err
		}
		if !strings.Contains(logs, "example-probe-output-ok") {
			return fmt.Errorf("logs-history: source log marker missing")
		}
		history, err := exampleCLI(ctx, in, "runs", "list", "--pipeline", "example-probe", "--profile="+in.APIProfile, "-o", "plain", "--limit", "100")
		if err != nil {
			return err
		}
		for _, recorded := range strings.Fields(history) {
			if recorded == run.id {
				return nil
			}
		}
		return fmt.Errorf("logs-history: finished probe absent from run history")
	}
}

func exampleReference(in ExampleInputs) func(context.Context) error {
	return func(ctx context.Context) error {
		if in.SmokeProfile == "" {
			sparkwing.Annotate(ctx, sparkwing.Annotation{Message: "SKIP: no --smoke-profile configured for the separate sparkwing-ci-smoke run"})
			return nil
		}
		if in.SmokeCheckout == "" {
			return fmt.Errorf("reference-pipeline: --smoke-checkout is required with --smoke-profile")
		}
		repo, err := exampleCLI(ctx, in, "-C", in.SmokeCheckout, "pipeline", "trigger", in.SmokePipeline, "--profile", in.SmokeProfile, "--detach", "-o", "plain")
		if err != nil {
			return err
		}
		target := in
		target.APIProfile = in.SmokeProfile
		run, err := exampleTrackRun(ctx, target, repo)
		if err != nil {
			return err
		}
		detail, err := run.await(ctx, target)
		if err != nil {
			return err
		}
		if detail.Status != "success" {
			return fmt.Errorf("reference-pipeline: %s ended %s", repo, detail.Status)
		}
		for _, name := range []string{"hello", "docker-test"} {
			found := false
			for _, job := range detail.Jobs {
				if job.ID == name && job.Outcome == "success" {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("reference-pipeline: %s did not succeed", name)
			}
		}
		return nil
	}
}

type ExampleProbeInputs struct {
	Token      string `flag:"token" required:"true"`
	FailOnce   bool   `flag:"fail-once"`
	Long       bool   `flag:"long"`
	CLI        string `flag:"cli" default:"sparkwing"`
	APIProfile string `flag:"api-profile"`
}

type ExampleProbe struct{ sparkwing.Base }

func (ExampleProbe) Plan(_ context.Context, plan *sparkwing.Plan, in ExampleProbeInputs, rc sparkwing.RunContext) error {
	source := sparkwing.Job(plan, "source", &exampleValue{Value: in.Token, Log: "example-probe-output-ok"})
	memo := sparkwing.Job(plan, "memo", &exampleValue{Value: in.Token}).Memo(inputs.Const(in.Token))
	sparkwing.Job(plan, "probe", func(ctx context.Context) error {
		if sparkwing.RefTo[string](source).Get(ctx) != in.Token || sparkwing.RefTo[string](memo).Get(ctx) != in.Token {
			return fmt.Errorf("probe: predecessor output was lost")
		}
		if in.FailOnce {
			detail, err := exampleStatus(ctx, ExampleInputs{CLI: in.CLI, APIProfile: in.APIProfile}, rc.RunID, false)
			if err != nil {
				return err
			}
			for _, job := range detail.Jobs {
				if job.ID == "probe" && job.Attempt == 1 && detail.Retry == nil {
					return fmt.Errorf("requested deterministic failure on attempt 1")
				}
			}
		}
		return nil
	}).Needs(source, memo)
	long := sparkwing.Job(plan, "long", func(ctx context.Context) error {
		sparkwing.Info(ctx, "example-probe-long-running")
		<-ctx.Done()
		return ctx.Err()
	}).Timeout(45 * time.Second)
	if !in.Long {
		long.SkipIf(exampleSkip("long probe not requested"))
	}
	return nil
}

func init() {
	sparkwing.Register("ExampleProbe", func() sparkwing.Pipeline[ExampleProbeInputs] { return &ExampleProbe{} })
}
