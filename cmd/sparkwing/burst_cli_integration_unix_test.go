//go:build unix && rewrite_integration

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestBurstPublicRunBuildsEditedSourceAndStreamsMaskedSecrets(t *testing.T) {
	burstPublicRun(t, false, false, false)
}

func TestBurstPublicRunExecutesOnRegisteredPersonalMachine(t *testing.T) {
	burstPublicRun(t, true, false, false)
}

func TestBurstPublicRunOverflowsToPersonalMachineWhileLocalCapacityIsHeld(t *testing.T) {
	burstPublicRun(t, true, false, true)
}

func TestBurstPublicRunFallsBackLocallyWithRequesterSecrets(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		t.Skipf("native fallback requires local sockets: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	cli := buildSubmitCLI(t)
	t.Setenv("GOMODCACHE", offlineGoEnv(t, "GOMODCACHE"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("TOKEN", burstIntakeSecret)
	config := filepath.Join(home, "config.yaml")
	t.Setenv("SPARKWING_CONFIG", config)
	contents := fmt.Sprintf("profiles:\n  cloud:\n    controller:\n      url: %q\n      token: swu_offline_fixture\nrun:\n  routing:\n    route: cloud\n    profile: cloud\n    local_secrets: [TOKEN]\n", endpoint)
	if err := os.WriteFile(config, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := burstIntakeRepository(t)
	if err := os.Remove(filepath.Join(repo, ".env")); err != nil {
		t.Fatal(err)
	}
	offlineStopDaemon(t, home)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	handlePath := filepath.Join(t.TempDir(), "run.json")
	command := exec.CommandContext(ctx, cli, "run", "burst", "--sw-run-handle-file", handlePath, "-o=json")
	command.Dir = repo
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "Cloud controller is unreachable; running locally.") || !strings.Contains(string(output), "burst-edited-ok secret=*** encoded=***") || !strings.Contains(string(output), "burst-cache-restored") || !strings.Contains(string(output), "burst-artifact-restored") || bytes.Contains(output, []byte(burstIntakeSecret)) {
		t.Fatalf("native local fallback: %v\n%s", err, output)
	}
	var handle orchestrator.RunHandle
	body, err := os.ReadFile(handlePath)
	if err != nil || json.Unmarshal(body, &handle) != nil || handle.RunID == "" {
		t.Fatalf("fallback lost local ownership: %v", err)
	}
	read := exec.CommandContext(ctx, cli, "runs", "status", "--run", handle.RunID, "-o=json")
	read.Dir = repo
	body, err = read.Output()
	var detail controllerwire.RunDetail
	if err != nil || json.Unmarshal(body, &detail) != nil || detail.Status != "success" || len(detail.Jobs) != 2 {
		t.Fatalf("fallback did not finish in local history: %v\n%s", err, body)
	}
}

func TestBurstPublicRunInterruptCancelsHereAndPersonalWork(t *testing.T) {
	for _, personal := range []bool{false, true} {
		t.Run(map[bool]string{false: "here", true: "personal"}[personal], func(t *testing.T) {
			burstPublicRun(t, personal, true, false)
		})
	}
}

func burstPublicRun(t *testing.T, personal, interrupt, contended bool) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		t.Skipf("native burst requires loopback and local-controller sockets: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	cli := buildSubmitCLI(t)
	if personal {
		// safety: replacement pins bind the current executable's digest across host roles.
		cli, err = os.Executable()
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GOMODCACHE", offlineGoEnv(t, "GOMODCACHE"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("TOKEN", burstIntakeSecret)
	repo := burstIntakeRepository(t)
	endpoint := "http://" + listener.Addr().String()
	state, controller, bearer := burstIntakeController(t, endpoint)
	var personalAwarded atomic.Bool
	handler := controller.Handler()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runners/claim" {
			handler.ServeHTTP(w, r)
			return
		}
		recorded := httptest.NewRecorder()
		handler.ServeHTTP(recorded, r)
		var result controllerwire.ClaimResponse
		if recorded.Code == http.StatusOK && json.Unmarshal(recorded.Body.Bytes(), &result) == nil && result.Claim != nil && result.Claim.Unit == capability.UnitNode && result.Claim.Runner.Name == "burst-desktop" {
			personalAwarded.Store(true)
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
	contents := fmt.Sprintf("profiles:\n  cloud:\n    controller:\n      url: %q\n      token: %q\nrun:\n  routing:\n    route: cloud\n    profile: cloud\n    local_secrets: [TOKEN]\n", endpoint, bearer)
	if contended {
		contents += "    prefer: [{target: here}, {target: machines}]\n"
	} else if personal {
		contents += "    prefer: [{target: machines}]\n"
	}
	if err := os.WriteFile(config, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_CONFIG", config)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	t.Cleanup(cancel)
	offlineStopDaemon(t, os.Getenv("SPARKWING_HOME"))
	invoke := func(args ...string) []byte {
		t.Helper()
		command := exec.CommandContext(ctx, cli, args...)
		command.Dir = repo
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("public burst CLI %v: %v\n%s", args, err, output)
		}
		return output
	}
	var overflow controllerwire.OverflowControl
	if err := json.Unmarshal(invoke("overflow", "show", "--profile", "cloud", "-o=json"), &overflow); err != nil || overflow.MonthlyMinutes != nil {
		t.Fatalf("new requester has an overflow cap: %+v %v", overflow, err)
	}
	invoke("overflow", "set", "--profile", "cloud", "--minutes", "0", "-o=json")
	if personal {
		burstPublicPersonalRunner(t, ctx, cli, repo, state)
	}
	if contended {
		invoke("runs", "list", "-o=json")
		burstPublicHoldMachine(t, ctx, endpoint)
	}
	handleFile := filepath.Join(t.TempDir(), "run.json")
	args := []string{"run", "burst", "--sw-run-handle-file", handleFile, "-o=json"}
	var output []byte
	if interrupt {
		args = append(args, "--block")
		command := exec.CommandContext(ctx, cli, args...)
		command.Dir = repo
		ready := make(chan struct{})
		stream := &burstCLIOutput{ready: ready, marker: "burst-awaiting-cancellation"}
		command.Stdout, command.Stderr = stream, stream
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		finished := make(chan error, 1)
		go func() { finished <- command.Wait() }()
		joined := false
		defer func() {
			if !joined {
				_ = command.Process.Kill()
				<-finished
			}
		}()
		select {
		case <-ready:
			if err := command.Process.Signal(os.Interrupt); err != nil {
				t.Fatal(err)
			}
		case err := <-finished:
			joined = true
			t.Fatalf("burst stopped before its cancellation marker: %v\n%s", err, stream.String())
		case <-ctx.Done():
			<-finished
			joined = true
			t.Fatalf("burst did not reach its cancellation marker: %v\n%s", ctx.Err(), stream.String())
		}
		err := <-finished
		joined = true
		if err == nil {
			t.Fatal("interrupted burst reported success")
		}
		output = []byte(stream.String())
	} else {
		output = invoke(args...)
	}
	if !strings.Contains(string(output), "burst-edited-ok secret=*** encoded=***") || strings.Contains(string(output), burstIntakeSecret) || strings.Contains(string(output), base64.StdEncoding.EncodeToString([]byte(burstIntakeSecret))) {
		t.Fatalf("native burst lost masked terminal output: %s", output)
	}
	var handle orchestrator.RunHandle
	body, err := os.ReadFile(handleFile)
	if err != nil || json.Unmarshal(body, &handle) != nil || handle.RunID == "" {
		t.Fatalf("public burst lost admitted run identity: %s %v", body, err)
	}
	member, err := client.NewEngineClient(client.EngineOptions{URL: endpoint, Bearer: func(context.Context) (string, error) { return bearer, nil }})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := member.ReadRun(ctx, handle.RunID)
	wantStatus := "success"
	if interrupt {
		wantStatus = "cancelled"
	}
	if err != nil || detail.Status != wantStatus || len(detail.Jobs) != 2 {
		t.Fatalf("Cloud owner did not retain native execution outcome %s: %+v %v", wantStatus, detail, err)
	}
	for _, job := range detail.Jobs {
		if job.Outcome != wantStatus && !(interrupt && job.ID == "consume" && job.Outcome == "skipped") {
			t.Fatalf("Cloud job did not finish after native execution: %+v", job)
		}
	}
	if !interrupt && !strings.Contains(string(output), "burst-artifact-restored") {
		t.Fatalf("native burst did not restore its published artifacts: %s", output)
	}
	if !interrupt && !strings.Contains(string(output), "burst-cache-restored") {
		t.Fatalf("native burst did not restore its declared dependency cache: %s", output)
	}
	if personal && !personalAwarded.Load() {
		t.Fatal("public burst did not award the job to its registered personal machine")
	}
	if local := invoke("runs", "list", "-o=json"); strings.Contains(string(local), handle.RunID) {
		t.Fatalf("burst copied ownership into the local controller: %s", local)
	}
}

func burstPublicHoldMachine(t *testing.T, ctx context.Context, endpoint string) {
	t.Helper()
	home := os.Getenv("SPARKWING_HOME")
	capacity, _, err := observeBurstCapacity(ctx, home)
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := wingdclient.EnsureDaemon(ctx, wingdclient.Options{Home: home, Version: installedVersion()})
	if err != nil {
		t.Fatal(err)
	}
	held, err := daemon.Acquire(ctx, wingwire.AdmissionRequest{
		RunID: wingwire.HolderRef(wingwire.RemoteRunID(endpoint, "competing-machine-work")), Pipeline: "machine-holder",
		Resources: wingwire.HostResources{Cores: capacity.Cores}, CostSource: wingwire.CostSourcePin,
	}, nil)
	if err != nil {
		_ = daemon.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- held.WatchControl(func(wingwire.Evicted) { t.Error("competing machine holder was evicted") }, func(wingwire.Cancel) { t.Error("burst cancelled unrelated machine work") })
	}()
	t.Cleanup(func() {
		if err := held.Release(); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	_, available, err := observeBurstCapacity(ctx, home)
	if err != nil || available.Cores != 0 {
		t.Fatalf("competing work did not occupy local capacity: %+v %v", available, err)
	}
}

func burstPublicPersonalRunner(t *testing.T, ctx context.Context, cli, repo string, state *store.Store) {
	t.Helper()
	session, _, _, err := state.CreateAccountSession(ctx, store.Account{ID: "burst-user"}, "default", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	runnerHome := t.TempDir()
	runnerState := filepath.Join(runnerHome, "state")
	config := filepath.Join(runnerHome, "runner.yaml")
	enroll := exec.CommandContext(ctx, cli, "runners", "add", "--personal", "--session-stdin", "--no-service", "--profile", "cloud", "--runner", "burst-desktop", "--repos", "example.test/acme/*", "--max-concurrent", "1", "--config", config, "-o=json")
	enroll.Dir, enroll.Stdin = repo, strings.NewReader(session+"\n")
	if output, err := enroll.CombinedOutput(); err != nil {
		t.Fatalf("public personal-machine enrollment: %v\n%s", err, output)
	}
	environment := append(os.Environ(), "HOME="+runnerHome, "SPARKWING_HOME="+runnerState,
		"XDG_CONFIG_HOME="+filepath.Join(runnerHome, "config"), "XDG_CACHE_HOME="+filepath.Join(runnerHome, "cache"), "SPARKWING_CONFIG="+config)
	offlineStopDaemon(t, runnerState)
	ready := exec.CommandContext(ctx, cli, "runs", "list", "-o=json")
	ready.Env, ready.Dir = environment, repo
	if output, err := ready.CombinedOutput(); err != nil {
		t.Fatalf("personal machine controller startup: %v\n%s", err, output)
	}
	runner := exec.CommandContext(ctx, cli, "__test-runner", "start", "--config", config)
	runner.Env = environment
	stream := &burstCLIOutput{}
	runner.Stdout, runner.Stderr = stream, stream
	if err := runner.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- runner.Wait() }()
	t.Cleanup(func() {
		_ = runner.Process.Signal(os.Interrupt)
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			_ = runner.Process.Kill()
			<-finished
			t.Error("registered runner did not drain after interrupt")
		}
		t.Logf("registered runner output: %s", stream.String())
	})
}

type burstCLIOutput struct {
	mu     sync.Mutex
	once   sync.Once
	bytes  bytes.Buffer
	ready  chan struct{}
	marker string
}

func (w *burstCLIOutput) Write(body []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.bytes.Write(body)
	if w.ready != nil && strings.Contains(w.bytes.String(), w.marker) {
		w.once.Do(func() { close(w.ready) })
	}
	return n, err
}

func (w *burstCLIOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bytes.String()
}
