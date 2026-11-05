//go:build unix

package nodehost

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/sdkcompat"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

var (
	sdkCompatMode  = flag.String("sdk-compat", "", "historical SDK matrix: warm or cold")
	sdkCompatCache = flag.String("sdk-compat-cache", "", "immutable historical SDK fixture cache directory")
)

func TestHistoricalSDKCompatibility(t *testing.T) {
	if *sdkCompatMode == "" {
		t.Skip("run the sdk-compat gate or pre-release step")
	}
	if *sdkCompatMode != "warm" && *sdkCompatMode != "cold" {
		t.Fatal("-sdk-compat must be warm or cold")
	}
	root, err := filepath.Abs("../../testdata/sdk-compat")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := sdkcompat.LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	hostDigest := historicalHostDigest(t)
	cache := *sdkCompatCache
	if cache == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			t.Fatal(err)
		}
		cache = filepath.Join(base, "sparkwing", "sdk-compat")
	}
	for _, fixture := range manifest.Fixtures {
		t.Run(fixture.Language+"/"+fixture.Version, func(t *testing.T) {
			started := time.Now()
			prepared, err := sdkcompat.Prepare(t.Context(), root, fixture, sdkcompat.Options{CacheDir: cache, Cold: *sdkCompatMode == "cold"})
			t.Cleanup(func() {
				record, err := json.Marshal(map[string]any{"sdk": fixture, "preparation": prepared, "host": engineVersion(), "host_executable_sha256": hostDigest, "host_go": runtime.Version(), "mode": *sdkCompatMode, "cell_ms": time.Since(started).Milliseconds(), "passed": !t.Failed()})
				if err != nil {
					t.Error(err)
					return
				}
				t.Log(string(record))
			})
			if err != nil {
				t.Fatal(err)
			}
			if *sdkCompatMode == "cold" {
				t.Cleanup(func() {
					if err := os.RemoveAll(prepared.Dir); err != nil {
						t.Error(err)
					}
				})
			}
			runHistoricalSDKCell(t, fixture, prepared)
		})
	}
}

func historicalHostDigest(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", digest.Sum(nil))
}

func historicalVector(t *testing.T, id, language string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile("../../testdata/sdk-compat/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Vectors []map[string]json.RawMessage `json:"vectors"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, vector := range corpus.Vectors {
		var name string
		if err := json.Unmarshal(vector["id"], &name); err != nil {
			t.Fatal(err)
		}
		if name != id {
			continue
		}
		var expectation map[string]json.RawMessage
		if err := json.Unmarshal(vector[language], &expectation); err != nil {
			t.Fatal(err)
		}
		return expectation
	}
	t.Fatalf("missing shared behavior vector %s/%s", id, language)
	return nil
}

type historicalLogs struct {
	mu         sync.Mutex
	frames     []nodeproto.FrameLog
	grandchild chan int
	rawRead    chan struct{}
	rawOnce    sync.Once
	outerRead  chan struct{}
	outerOnce  sync.Once
}

type historicalIdentityProcess struct {
	*protocolSession
	hello nodeproto.HelloResult
}

func (p *historicalIdentityProcess) Request(ctx context.Context, request nodeproto.Request) (nodeproto.Reply, error) {
	reply, err := p.protocolSession.Request(ctx, request)
	if err == nil && request.Op == nodeproto.OpHello {
		err = reply.Decode(&p.hello)
	}
	return reply, err
}

func (l *historicalLogs) write(_ context.Context, frame nodeproto.FrameLog) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.frames = append(l.frames, frame)
	if strings.Contains(frame.Message, "compat-raw-first") {
		l.rawOnce.Do(func() { close(l.rawRead) })
	}
	if strings.Contains(frame.Message, "compat-raw-outer-first") {
		l.outerOnce.Do(func() { close(l.outerRead) })
	}
	if _, suffix, ok := strings.Cut(frame.Message, "compat-grandchild:"); ok {
		pid, err := strconv.Atoi(strings.TrimSpace(suffix))
		if err != nil {
			return err
		}
		select {
		case l.grandchild <- pid:
		default:
		}
	}
	return nil
}

func historicalHelper(t *testing.T, mode string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "helper")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+quote(executable)+" -test.run='^TestHistoricalSDKHelper$' -- "+mode+" \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return wrapper
}

func runHistoricalSDKCell(t *testing.T, fixture sdkcompat.Fixture, prepared sdkcompat.Prepared) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	logs := &historicalLogs{grandchild: make(chan int, 1), rawRead: make(chan struct{}), outerRead: make(chan struct{})}
	barrier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-logs.rawRead:
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
		}
	}))
	defer barrier.Close()
	outerBarrier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-logs.outerRead:
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
		}
	}))
	defer outerBarrier.Close()
	masker := secrets.NewMasker()
	if err := masker.Register("compat-secret-value"); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	fakeTools := t.TempDir()
	for _, name := range []string{"git", "docker"} {
		data, err := os.ReadFile(historicalHelper(t, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fakeTools, name), data, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env := testProcessEnv(t, "PATH="+fakeTools+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+home, "SPARKWING_HOME="+home, "COMPAT_HELPER_EXE="+historicalHelper(t, ""), "COMPAT_RAW_BARRIER_URL="+barrier.URL, "COMPAT_OUTER_BARRIER_URL="+outerBarrier.URL)
	router := start(t)
	service := newService(t)
	service.RunID, service.Job.ID, service.Masks, service.Secrets = "compat", "task", NewMaskRegistry(masker), []string{"COMPAT_SECRET"}
	var unmasked atomic.Bool
	service.Secret = func(context.Context, capability.Authorized, string) (string, bool, error) {
		return "compat-stored-value", !unmasked.Load(), nil
	}
	service.OIDC = func(_ context.Context, _ capability.Authorized, audience string) (ProcessOIDCToken, error) {
		if audience != "compat-audience" {
			return ProcessOIDCToken{}, fmt.Errorf("unexpected audience %s", audience)
		}
		return ProcessOIDCToken{Token: "compat-local-oidc-token", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	bearer, revoke, err := router.Admit(routeNode{Service: service})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(revoke)
	serviceHost := httptest.NewServer(router.handler)
	defer serviceHost.Close()
	process, err := startProtocolSession(ctx, ProtocolSessionConfig{Command: prepared.Command, Directory: prepared.Dir, Environment: env, Masks: NewMaskRegistry(masker), Log: logs.write, Revoke: func() error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Error(err)
		}
	})
	observed := &historicalIdentityProcess{protocolSession: process}
	session := &executorSession{process: observed}
	identity := engineid.Identity{Version: engineVersion(), ManifestDigest: historicalHostDigest(t), Pin: engineid.Pin{SDK: engineid.SDKKind(fixture.Language), Version: fixture.Version}}
	runner := nodeproto.RunnerIdentity{Name: "compat", Type: "local", Labels: []string{}}
	binding := &nodeproto.ProcessBinding{RunID: "compat", JobID: "task", Routes: nodeproto.ProcessRoutes{URL: serviceHost.URL, Bearer: bearer}}
	if err := sdkHello(ctx, session, identity, runner, binding); err != nil {
		t.Fatal(err)
	}
	if observed.hello.Protocol != fixture.WireMajor || !slices.Equal(observed.hello.Features, fixture.Capabilities) {
		t.Fatalf("hello capabilities: %+v", observed.hello)
	}
	t.Logf("accepted hello: %+v; candidate host: %s", observed.hello, identity.Version)
	var described json.RawMessage
	if err := session.call(ctx, nodeproto.Request{Op: nodeproto.OpDescribe}, &described, ""); err != nil {
		t.Fatal(err)
	}
	var description nodeproto.DescribeDoc
	if err := json.Unmarshal(described, &description); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(description.Pipelines, func(p nodeproto.PipelineDescription) bool {
		return p.Name == "compat" && slices.ContainsFunc(p.Args, func(a nodeproto.ArgumentDescription) bool { return a.Name == "name" })
	}) {
		t.Fatalf("describe lost pipeline or args: %s", described)
	}
	args := map[string]string{"name": "compat-argument"}
	session.jobArgs = map[string]map[string]string{"task": args}
	var planned json.RawMessage
	if err := session.call(ctx, nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: "compat", Args: args, Run: &nodeproto.RunContext{RunID: "compat", Pipeline: "compat"}}, &planned, ""); err != nil {
		t.Fatal(err)
	}
	plan, err := nodeproto.ValidatePlan(planned, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: NewMaskRegistry(masker)})
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, planned)
	}
	if len(plan.Jobs) != 3 || plan.Jobs[0].ID != "task" {
		t.Fatalf("plan: %s", planned)
	}
	job := plan.Jobs[0]
	if job.Retry == nil || job.Retry.Attempts != 2 || job.TimeoutMS == nil || *job.TimeoutMS != 60000 || !slices.Contains(plan.Jobs[2].Needs, "task") || len(job.Closures.Generate) != 1 {
		t.Fatalf("lifecycle declarations: %s", planned)
	}
	for _, closure := range []struct{ name, kind string }{{job.Closures.MemoKey, "memo_key"}, {job.Closures.Generate[0], "generate"}} {
		var previous json.RawMessage
		for range 2 {
			var result nodeproto.EvalResult
			if err := session.call(ctx, nodeproto.Request{Op: nodeproto.OpEval, Closure: closure.name}, &result, closure.kind); err != nil {
				t.Fatal(err)
			}
			if result.Error != nil {
				t.Fatal(result.Error)
			}
			if closure.kind == "generate" {
				generated, err := nodeproto.ValidatePlan(append(append([]byte(`{"jobs":`), result.Value...), '}'), nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: NewMaskRegistry(masker), Expansion: true, SourceJob: "task", ExistingJobs: plan.Jobs})
				if err != nil || len(generated.Jobs) != 1 || generated.Jobs[0].ID != "generated" {
					t.Fatalf("generator: %s, %v", result.Value, err)
				}
			}
			if previous != nil && string(previous) != string(result.Value) {
				t.Fatalf("%s replay changed: %s != %s", closure.kind, previous, result.Value)
			}
			previous = result.Value
		}
	}
	output := historicalStorageLifecycle(t, ctx, session, job, plan.Jobs[1])
	historicalGeneratorReplay(t, ctx, session, plan, output)
	historicalExecutionLifecycle(t, ctx, prepared, identity, runner, plan, args, env)
	for _, step := range []string{"exec", "environment", "helper", "raw", "oidc", "secret", "git", "docker"} {
		result := historicalRun(t, ctx, session, step)
		if result.Outcome != "success" {
			t.Fatalf("%s: %+v", step, result)
		}
		if step == "helper" {
			want := historicalVector(t, "command-string-byte-order-mark", fixture.Language)["step_output"]
			var gotValue, wantValue any
			if err := json.Unmarshal(result.Output, &gotValue); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(want, &wantValue); err != nil {
				t.Fatal(err)
			}
			gotJSON, err := json.Marshal(gotValue)
			if err != nil {
				t.Fatal(err)
			}
			wantJSON, err := json.Marshal(wantValue)
			if err != nil {
				t.Fatal(err)
			}
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("trim vector: %s != %s", gotJSON, wantJSON)
			}
		}
		if step == "oidc" && string(result.Output) != `{"token":"compat-local-oidc-token"}` {
			t.Fatalf("local issuer result: %s", result.Output)
		}
		if step == "secret" && string(result.Output) != `{"secret":"compat-stored-value"}` {
			t.Fatalf("fake secret result: %s", result.Output)
		}
		if step == "git" {
			historicalAssertJSON(t, result.Output, historicalVector(t, "git-newline-bearing-path", fixture.Language)["paths"])
		}
		if step == "docker" {
			historicalAssertJSON(t, result.Output, historicalVector(t, "docker-build-result-shape", fixture.Language)["step_output"])
		}
	}
	failed := historicalRun(t, ctx, session, "fail")
	if failed.Outcome != "failed" || failed.Error == nil || !strings.Contains(failed.Error.Error(), "7") {
		t.Fatalf("nonzero exit: %+v", failed)
	}
	unmasked.Store(true)
	drift := historicalRunAttempt(t, ctx, session, "secret", 2)
	driftVector := historicalVector(t, "secret-kind-drift", fixture.Language)
	var wantOutcome string
	if err := json.Unmarshal(driftVector["outcome"], &wantOutcome); err != nil {
		t.Fatal(err)
	}
	if drift.Outcome != wantOutcome {
		t.Fatalf("secret kind drift: %+v, want %s", drift, wantOutcome)
	}
	if errorRaw, ok := driftVector["error_contains"]; ok {
		var message string
		if err := json.Unmarshal(errorRaw, &message); err != nil {
			t.Fatal(err)
		}
		if drift.Error == nil || !strings.Contains(drift.Error.Error(), message) {
			t.Fatalf("secret kind drift lost refusal: %+v", drift)
		}
	}
	if output, ok := driftVector["step_output"]; ok {
		historicalAssertJSON(t, drift.Output, output)
	}
	runCtx, stop := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() {
		var output nodeproto.RunResult
		result <- session.call(runCtx, nodeproto.Request{Op: nodeproto.OpRunStep, Job: "task", Step: "cancel", Attempt: 2, Settled: []string{}}, &output, "")
	}()
	var pid int
	select {
	case pid = <-logs.grandchild:
	case <-ctx.Done():
		stop()
		t.Fatal("grandchild did not become ready")
	case err := <-result:
		stop()
		t.Fatalf("cancel body ended before readiness: %v", err)
	}
	stop()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	processes, err := procgroup.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, process := range processes {
		if process.PID == pid && !process.Terminated() {
			t.Fatalf("grandchild %d survived Close", pid)
		}
	}
	logs.mu.Lock()
	defer logs.mu.Unlock()
	var all strings.Builder
	for _, frame := range logs.frames {
		all.WriteString(frame.Message)
	}
	for _, marker := range []string{"compat-exec-out", "compat-exec-err", "compat-raw-out", "compat-raw-chunk-out", "compat-raw-outer-first", "***"} {
		if !strings.Contains(all.String(), marker) {
			t.Errorf("logs lack %q: %s", marker, all.String())
		}
	}
	var environment string
	if err := json.Unmarshal(historicalVector(t, "callback-mutated-environment-inheritance", fixture.Language)["stdout"], &environment); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(all.String(), environment) {
		t.Fatalf("environment vector lost %q", environment)
	}
	if strings.Contains(all.String(), "compat-secret-") {
		t.Fatal("secret or its prefix leaked through host logs")
	}
	for _, secret := range []string{"compat-stored-value", "compat-local-oidc-token"} {
		if strings.Contains(all.String(), secret) {
			t.Fatalf("issued secret leaked into logs: %s", secret)
		}
	}
}

func historicalAssertJSON(t *testing.T, got, want json.RawMessage) {
	t.Helper()
	var actual, expected any
	if err := json.Unmarshal(got, &actual); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &expected); err != nil {
		t.Fatal(err)
	}
	actualJSON, _ := json.Marshal(actual)
	expectedJSON, _ := json.Marshal(expected)
	if string(actualJSON) != string(expectedJSON) {
		t.Fatalf("shared vector: %s != %s", actualJSON, expectedJSON)
	}
}

func historicalRun(t *testing.T, ctx context.Context, session *executorSession, step string) nodeproto.RunResult {
	t.Helper()
	return historicalRunAttempt(t, ctx, session, step, 1)
}

func historicalRunAttempt(t *testing.T, ctx context.Context, session *executorSession, step string, attempt int) nodeproto.RunResult {
	t.Helper()
	var result nodeproto.RunResult
	if err := session.call(ctx, nodeproto.Request{Op: nodeproto.OpRunStep, Job: "task", Step: step, Attempt: attempt, Settled: []string{}}, &result, ""); err != nil {
		t.Fatalf("step %s attempt %d: %v", step, attempt, err)
	}
	return result
}

func TestHistoricalSDKHelper(t *testing.T) {
	separator := slices.Index(os.Args, "--")
	if separator < 0 {
		return
	}
	mode := os.Args[separator+1]
	switch mode {
	case "exec":
		fmt.Fprintln(os.Stdout, "compat-exec-out")
		fmt.Fprintln(os.Stderr, "compat-exec-err")
	case "fail":
		fmt.Fprintln(os.Stderr, "compat-exit-7")
		os.Exit(7)
	case "env":
		fmt.Fprintln(os.Stdout, "compat-env:"+os.Getenv("COMPAT_MUTATED_ENV"))
	case "trim":
		fmt.Fprint(os.Stdout, "\ufeffcompat\ufeff")
	case "raw":
		fmt.Fprint(os.Stdout, "compat-raw-first\ncompat-secret-")
		response, err := http.Get(os.Getenv("COMPAT_RAW_BARRIER_URL"))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatal(response.Status)
		}
		fmt.Fprint(os.Stdout, "value\ncompat-raw-chunk-out\n")
	case "raw-outer-barrier":
		response, err := http.Get(os.Getenv("COMPAT_OUTER_BARRIER_URL"))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatal(response.Status)
		}
	case "concurrent":
		response, err := http.Get(os.Getenv("COMPAT_LIFECYCLE_BARRIER_URL"))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatal(response.Status)
		}
	case "retry":
		file, err := os.OpenFile(os.Getenv("COMPAT_RETRY_FILE"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			file.Close()
			os.Exit(7)
		}
		if !errors.Is(err, os.ErrExist) {
			t.Fatal(err)
		}
	case "git":
		if !slices.Contains(os.Args[separator+2:], "diff") {
			t.Fatalf("unexpected fake git operation: %v", os.Args)
		}
		if slices.Contains(os.Args[separator+2:], "-z") {
			fmt.Fprint(os.Stdout, "line\nbreak.txt\x00")
		} else {
			fmt.Fprintln(os.Stdout, `"line\nbreak.txt"`)
		}
	case "docker":
		if !slices.Equal(os.Args[separator+2:], []string{"build", "-f", "Dockerfile", "-t", "compat-image:fixture", "."}) {
			t.Fatalf("unexpected fake Docker operation: %v", os.Args)
		}
	case "cancel":
		child := exec.Command(os.Args[0], "-test.run=^TestHistoricalSDKHelper$", "--", "grandchild")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(os.Stdout, "compat-grandchild:%d\n", child.Process.Pid)
		historicalWaitSignal()
		if err := child.Wait(); err != nil {
			t.Log(err)
		}
	case "grandchild":
		historicalWaitSignal()
	default:
		t.Fatalf("unknown fixture helper mode %q", mode)
	}
	os.Exit(0)
}

func historicalWaitSignal() {
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(done)
	<-done
}
