//go:build unix && rewrite_integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

const runAnywherePipeline = `package main

import (
    "context"
    sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type source struct{ sw.Produces[string] }

func (source) Work(work *sw.Work) (*sw.WorkStep, error) {
    return sw.Step(work, "name", func(ctx context.Context) (string, error) {
        sw.Info(ctx, "anywhere-source-ran")
        return "generated", nil
    }), nil
}

type pipeline struct{ sw.Base }

func (pipeline) Plan(_ context.Context, p *sw.Plan, _ sw.NoInputs, _ sw.RunContext) error {
    job := sw.Job(p, "source", source{})
    name := sw.RefTo[string](job)
    job.Generate(func(ctx context.Context, plan *sw.Plan) error {
        sw.Job(plan, name.Get(ctx), func(ctx context.Context) error { sw.Info(ctx, "anywhere-generated-ran"); return nil })
        return nil
    })
    sw.Job(p, "untyped", func(ctx context.Context) error { return nil }).Generate(func(_ context.Context, plan *sw.Plan) error {
        sw.Job(plan, "untyped-generated", func(ctx context.Context) error { sw.Info(ctx, "anywhere-untyped-generated-ran"); return nil })
        return nil
    })
    return nil
}

func main() {
    sw.Register("anywhere", func() sw.Pipeline[sw.NoInputs] { return pipeline{} })
    sw.Main()
}
`

func TestRunAnywhereRunnerHostsPlanEvaluationAndJobs(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		t.Skipf("runner hosting requires loopback sockets: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	cli, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repo := runAnywhereRepo(t)
	endpoint := "http://" + listener.Addr().String()
	state, controller, bearer := burstIntakeController(t, endpoint)
	var mu sync.Mutex
	var hosted []capability.UnitKind
	var intake []string
	runnerReady := make(chan struct{})
	var observed sync.Once
	handler := controller.Handler()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runs" && r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			mu.Lock()
			intake = append(intake, string(body))
			mu.Unlock()
		}
		if r.URL.Path != "/api/v1/runners/claim" {
			handler.ServeHTTP(w, r)
			return
		}
		recorded := httptest.NewRecorder()
		handler.ServeHTTP(recorded, r)
		var result controllerwire.ClaimResponse
		if recorded.Code == http.StatusOK {
			observed.Do(func() { close(runnerReady) })
		}
		if recorded.Code == http.StatusOK && json.Unmarshal(recorded.Body.Bytes(), &result) == nil && result.Claim != nil && result.Claim.Runner.Name == "burst-desktop" {
			mu.Lock()
			hosted = append(hosted, result.Claim.Unit)
			mu.Unlock()
		}
		for name, values := range recorded.Header() {
			w.Header()[name] = values
		}
		w.WriteHeader(recorded.Code)
		_, _ = w.Write(recorded.Body.Bytes())
	}))
	if err := server.Listener.Close(); err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	config := filepath.Join(os.Getenv("SPARKWING_HOME"), "config.yaml")
	if err := os.WriteFile(config, fmt.Appendf(nil, "profiles:\n  cloud:\n    controller:\n      url: %q\n      token: %q\n", endpoint, bearer), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_CONFIG", config)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	t.Cleanup(cancel)
	offlineStopDaemon(t, os.Getenv("SPARKWING_HOME"))
	if _, err := state.AsOperator().AcquireSingletonLease(ctx, "controller.owner", "run-anywhere-test", time.Hour); err != nil {
		t.Fatal(err)
	}
	runnerConfig := burstPublicPersonalRunner(t, ctx, cli, repo, state)
	section, err := userconfig.Node(runnerConfig, userconfig.Runner)
	if err != nil || section == nil {
		t.Fatalf("runner config: %v", err)
	}
	var runner userconfig.RunnerConfig
	if err := section.Decode(&runner); err != nil {
		t.Fatal(err)
	}
	token, err := os.ReadFile(filepath.Join(*runner.CredentialsDir, "agent-token"))
	if err != nil {
		t.Fatal(err)
	}
	cliConfig, err := os.OpenFile(config, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(cliConfig, "runner:\n  controller: %q\n  credentials_dir: %q\n", *runner.Controller, *runner.CredentialsDir)
	if err = errors.Join(err, cliConfig.Close()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runnerReady:
	case <-ctx.Done():
		t.Fatal("personal runner did not poll")
	}
	triggerCtx, stop := context.WithTimeout(ctx, 3*time.Minute)
	defer stop()
	command := exec.CommandContext(triggerCtx, cli, "pipeline", "trigger", "anywhere", "--profile", "cloud", "--working-tree", "-o=json")
	command.Dir = repo
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "anywhere-source-ran") || !strings.Contains(string(output), "anywhere-generated-ran") || !strings.Contains(string(output), "anywhere-untyped-generated-ran") {
		t.Fatalf("run-anywhere trigger: %v\n%s", err, output)
	}
	mu.Lock()
	defer mu.Unlock()
	secret := strings.TrimSpace(string(token))
	if len(intake) != 1 || strings.Contains(intake[0], secret) || !strings.Contains(intake[0], `"origin_machine":"`+secret[:16]+".") {
		t.Fatalf("the trigger must prove the runner credential without sending it: %v", intake)
	}
	runs, err := state.DB().QueryContext(t.Context(), `SELECT request_json FROM runs`)
	if err != nil {
		t.Fatal(err)
	}
	var origins []string
	for runs.Next() {
		var body []byte
		var sealed runspec.SealedRequest
		if err := runs.Scan(&body); err != nil || json.Unmarshal(body, &sealed) != nil {
			t.Fatal(err)
		}
		origins = append(origins, sealed.Request.OriginMachine)
	}
	if err := errors.Join(runs.Err(), runs.Close()); err != nil || len(origins) != 1 || origins[0] != "burst-desktop" {
		t.Fatalf("sealed here machine = %v %v", origins, err)
	}
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		if !slices.Contains(hosted, kind) {
			t.Fatalf("the personal runner hosted %v, missing %s", hosted, kind)
		}
	}
}

func runAnywhereRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GOMODCACHE", offlineGoEnv(t, "GOMODCACHE"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("SPARKWING_HOME", t.TempDir())
	repo := initSnapshotRepo(t)
	directory := filepath.Join(repo, ".sparkwing")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSDKFixture(t, directory, runAnywherePipeline)
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: anywhere\n    entrypoint: anywhere\n", 0o600)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "--quiet", "-m", "anywhere source")
	runSnapshotGit(t, repo, "remote", "add", "origin", "https://example.test/acme/anywhere.git")
	return repo
}

func TestLocalRunEvaluatesTheSameGeneratorsAsARunnerHost(t *testing.T) {
	cli, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repo := runAnywhereRepo(t)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	offlineStopDaemon(t, os.Getenv("SPARKWING_HOME"))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, cli, "run", "anywhere", "-o=json")
	command.Dir = repo
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "anywhere-generated-ran") || !strings.Contains(string(output), "anywhere-untyped-generated-ran") {
		t.Fatalf("local run: %v\n%s", err, output)
	}
}
