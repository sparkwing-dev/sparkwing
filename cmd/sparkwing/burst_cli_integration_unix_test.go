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

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/depcachekey"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
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
	if err != nil || !strings.Contains(string(output), "Cloud controller is unreachable; running locally.") || !strings.Contains(string(output), "burst-edited-ok secret=*** encoded=***") || !strings.Contains(string(output), "burst-cache-restored") || !strings.Contains(string(output), "burst-artifact-restored") || bytes.Contains(output, []byte(burstIntakeSecret)) || bytes.Contains(output, []byte("cache restore failed")) || bytes.Contains(output, []byte("cache save failed")) {
		t.Fatalf("native local fallback: %v\n%s", err, output)
	}
	var handle orchestrator.RunHandle
	body, err := os.ReadFile(handlePath)
	if err != nil || json.Unmarshal(body, &handle) != nil || handle.RunID == "" {
		t.Fatalf("fallback lost local ownership: %v", err)
	}
	read := exec.CommandContext(ctx, cli, "runs", "status", "--run", handle.RunID, "-o=json")
	read.Dir = repo
	var stderr bytes.Buffer
	read.Stderr = &stderr
	body, err = read.Output()
	var detail controllerwire.RunDetail
	if err != nil || json.Unmarshal(body, &detail) != nil || detail.Status != "success" || len(detail.Jobs) != 2 {
		t.Fatalf("fallback did not finish in local history: %v\n%s\n%s", err, body, stderr.String())
	}
}

func TestBurstPublicRunPublishesCompletedLocalHistory(t *testing.T) {
	for _, detached := range []bool{false, true} {
		t.Run(map[bool]string{false: "attached", true: "detached"}[detached], func(t *testing.T) {
			burstPublicLocalProfile(t, detached, false)
		})
	}
}

func TestBurstPublicRunReadsProfileCacheBeforeNativeExecution(t *testing.T) {
	for _, detached := range []bool{false, true} {
		t.Run(map[bool]string{false: "attached", true: "detached"}[detached], func(t *testing.T) {
			burstPublicLocalProfile(t, detached, true)
		})
	}
}

func burstPublicLocalProfile(t *testing.T, detached, cache bool) {
	t.Helper()
	t.Setenv("STRAY_REQUESTER_SHELL_NAME", "private")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		t.Skipf("native publication requires sockets: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	cli := buildSubmitCLI(t)
	t.Setenv("GOMODCACHE", offlineGoEnv(t, "GOMODCACHE"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("TOKEN", burstIntakeSecret)
	repo := burstIntakeRepository(t)
	endpoint := "http://" + listener.Addr().String()
	if err := os.Remove(filepath.Join(repo, ".env")); err != nil {
		t.Fatal(err)
	}
	state, controller, bearer := burstIntakeController(t, endpoint)
	published := make(chan struct{})
	var finished sync.Once
	handler := controller.Handler()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/finish") && !strings.Contains(r.URL.Path, "/local-host/") {
			finished.Do(func() { close(published) })
		}
	}))
	if err := server.Listener.Close(); err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	config := filepath.Join(home, "config.yaml")
	t.Setenv("SPARKWING_CONFIG", config)
	writeConfig := func(route string) {
		t.Helper()
		if err := os.WriteFile(config, []byte(fmt.Sprintf("profiles:\n  cloud:\n    controller:\n      url: %q\n      token: %q\nrun:\n  routing:\n    route: %s\n    profile: cloud\n    local_secrets: [TOKEN]\n", endpoint, bearer, route)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig("local")
	offlineStopDaemon(t, home)
	handleFile := filepath.Join(t.TempDir(), "local-run.json")
	t.Cleanup(func() {
		if t.Failed() {
			burstLocalDiagnostics(t, home, handleFile)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	invoke := func(args ...string) []byte {
		t.Helper()
		command := exec.CommandContext(ctx, cli, args...)
		command.Dir = repo
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("public profile CLI %v: %v\n%s", args, err, output)
		}
		return output
	}
	args := []string{"run", "burst", "--publish", "cloud", "-o=json"}
	wantRuns := 1
	if cache {
		writeConfig("cloud")
		invoke("run", "burst", "-o=json")
		burstVerifyDefaultCache(t, state, repo)
		writeConfig("local")
		source := strings.Replace(burstIntakePipeline, `if err := os.MkdirAll("modules", 0o700); err != nil { return err }`, `cached, err := os.ReadFile("modules/dependency")
        if err != nil || string(cached) != "cached dependency\n" { return errors.New("profile cache was not restored before the first step") }
        sw.Info(ctx, "burst-profile-cache-restored")
        if err := os.MkdirAll("modules", 0o700); err != nil { return err }`, 1)
		writeSnapshotFile(t, repo, ".sparkwing/main.go", source, 0o600)
		args = append(args, "--sw-cache-from", "cloud")
		wantRuns++
	}
	args = append(args, "--sw-run-handle-file", handleFile)
	if detached {
		args = append(args, "--sw-detached")
	}
	output := invoke(args...)
	if detached {
		var handle orchestrator.RunHandle
		body, err := os.ReadFile(handleFile)
		if err != nil || json.Unmarshal(body, &handle) != nil || handle.RunID == "" {
			t.Fatalf("detached profile run lost its identity: %s %v", body, err)
		}
		output = invoke("runs", "logs", "--run", handle.RunID, "--follow", "-o=json")
		select {
		case <-published:
		case <-ctx.Done():
			t.Fatalf("detached run never published completed history: %v\n%s", ctx.Err(), output)
		}
	}
	if !bytes.Contains(output, []byte("burst-edited-ok secret=*** encoded=***")) || bytes.Contains(output, []byte("publication failed")) || bytes.Contains(output, []byte(burstIntakeSecret)) || cache && !bytes.Contains(output, []byte("burst-profile-cache-restored")) {
		t.Fatalf("public local profile lost its masked execution or cache: %s", output)
	}
	reader, err := client.NewRunReader(client.EngineOptions{URL: endpoint, Bearer: func(context.Context) (string, error) { return bearer, nil }})
	if err != nil {
		t.Fatal(err)
	}
	page, err := reader.ListRuns(ctx, controllerwire.RunQuery{Pipeline: "burst", Limit: 10})
	if err != nil || len(page.Runs) != wantRuns || page.Runs[0].Status != "success" {
		t.Fatalf("published local history: %+v %v\n%s", page, err, output)
	}
	tenant, err := state.ForTeam(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := tenant.ReadSnapshot(ctx, page.Runs[0].ID)
	if err != nil || snapshot.Run.Owner != nil || snapshot.Run.Request.Request.Mode != "publish" || len(snapshot.Nodes) != 2 || snapshot.Nodes[0].ManifestRecord == nil {
		t.Fatalf("publication lost local result graph: %+v %v", snapshot, err)
	}
	persisted, err := json.Marshal(snapshot)
	if err != nil || bytes.Contains(persisted, []byte("STRAY_REQUESTER_SHELL_NAME")) || len(snapshot.Run.Request.Request.Env.Names) != 0 || len(snapshot.Run.Spec.Resolved.Env.Names) != 0 {
		t.Fatalf("publication uploaded private shell metadata: %s %v", persisted, err)
	}
}

func burstLocalDiagnostics(t *testing.T, home, handleFile string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	status, err := wingdclient.QueryLocalController(ctx, home)
	cancel()
	t.Logf("local controller at failure: %+v: %v", status, err)
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var handle orchestrator.RunHandle
	if body, err := os.ReadFile(handleFile); err == nil && json.Unmarshal(body, &handle) == nil && handle.RunID != "" {
		socket, err := wingd.APISocketPath(home)
		if err == nil {
			transport := wingdclient.NewAPIClient(socket)
			defer transport.CloseIdleConnections()
			reader, err := client.NewRunReader(client.EngineOptions{URL: wingdclient.APIBaseURL, HTTP: transport})
			if err == nil {
				detail, err := reader.ReadRun(ctx, handle.RunID)
				t.Logf("local run at failure: %+v: %v", detail, err)
			}
		}
	}
	if path, err := wingd.LogPath(home); err == nil {
		body, err := os.ReadFile(path)
		if len(body) > 16<<10 {
			body = body[len(body)-(16<<10):]
		}
		t.Logf("local controller stderr at failure: %v\n%s", err, body)
	}
}

func burstVerifyDefaultCache(t *testing.T, state *store.Store, repo string) {
	t.Helper()
	branch := strings.TrimSpace(runSnapshotGit(t, repo, "symbolic-ref", "--short", "HEAD"))
	runSnapshotGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+branch)
	key, err := depcachekey.Bytes("modules", "modules", []byte("edited\n"))
	if err != nil {
		t.Fatal(err)
	}
	ref := blob.RefScope{Ref: branch, Verified: true}
	namespace, err := blob.RecordNamespace(blob.RecordKey{Team: "default", Kind: blob.KindDepcache, Name: "modules", Scope: blob.Scope{Repo: "git:example.test/acme/burst", Ref: ref, Key: key}})
	if err != nil {
		t.Fatal(err)
	}
	facts, err := json.Marshal(blob.RefFacts{Scope: ref, SHA: strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD")), AppDeliveryKey: "fixture-app-delivery", DefaultBranch: branch, DefaultBranchVerified: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := state.DB().ExecContext(t.Context(), `UPDATE blob_records SET scope=?,ref_scope=?,verified_ref=1,verified_default_branch=1,ref_facts_json=? WHERE team='default' AND kind='depcache' AND name='modules'`, namespace, branch, facts)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		t.Fatalf("fixture did not produce one Cloud dependency archive: %d %v", count, err)
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
	runnerReady := make(chan struct{})
	var runnerObserved sync.Once
	handler := controller.Handler()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runners/claim" {
			handler.ServeHTTP(w, r)
			return
		}
		recorded := httptest.NewRecorder()
		handler.ServeHTTP(recorded, r)
		var result controllerwire.ClaimResponse
		if recorded.Code == http.StatusOK {
			runnerObserved.Do(func() { close(runnerReady) })
		}
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
		contents += "    prefer: [{target: machines, hold: 1m}]\n"
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
		select {
		case <-runnerReady:
		case <-ctx.Done():
			t.Fatal("registered personal machine did not complete its first authenticated poll")
		}
	}
	if contended {
		invoke("runs", "list", "-o=json")
		burstPublicHoldMachine(t, ctx, endpoint)
	}
	handleFile := filepath.Join(t.TempDir(), "run.json")
	args := []string{"run", "burst", "--sw-run-handle-file", handleFile, "-o=json"}
	var output []byte
	if interrupt {
		args = append(args, "--", "--block")
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
		t.Fatalf("Cloud owner did not retain native execution outcome %s: %+v %v\nCLI output:\n%s", wantStatus, detail, err, output)
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
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = runner.Process.Signal(os.Interrupt)
		select {
		case <-finished:
		case <-cleanup.Done():
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
