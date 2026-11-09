//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type hostExecObservation struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	Exit       int    `json:"exit"`
	Error      string `json:"error"`
	Terminated string `json:"terminated"`
	NotFound   bool   `json:"not_found"`
	Survivor   bool   `json:"survivor"`
}

func observe(res sparkwing.ExecResult, err error) hostExecObservation {
	o := hostExecObservation{Stdout: res.Stdout, Stderr: res.Stderr, Exit: res.ExitCode}
	if err != nil {
		o.Error = err.Error()
		o.NotFound = errors.Is(err, exec.ErrNotFound)
		var failure *sparkwing.ExecError
		if errors.As(err, &failure) {
			o.Terminated, o.Exit = failure.Terminated, failure.ExitCode
		}
	}
	return o
}

type hostExecJob struct {
	sparkwing.Produces[hostExecObservation]
}

//nolint:contextcheck // sparkwing.Cmd carries the step context it was built with.
func (hostExecJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	dir := os.Getenv("HOSTEXEC_DIR")
	steps := map[string]func(context.Context) (hostExecObservation, error){
		"logged": func(ctx context.Context) (hostExecObservation, error) {
			return observe(sparkwing.Bash(ctx, `printf 'line one\n`+hostExecSecret+` two\n'; printf 'err text' >&2`).Run()), nil
		},
		"captured": func(ctx context.Context) (hostExecObservation, error) {
			return observe(sparkwing.Exec(ctx, "sh", "-c", "printf captured-out; printf captured-err >&2").Capture()), nil
		},
		"big": func(ctx context.Context) (hostExecObservation, error) {
			out, err := sparkwing.Bash(ctx, `head -c 300000 /dev/zero | tr '\0' a`).String()
			o := observe(sparkwing.ExecResult{}, err)
			o.Stdout = strconv.Itoa(len(out))
			return o, nil
		},
		"missing": func(ctx context.Context) (hostExecObservation, error) {
			return observe(sparkwing.Exec(ctx, "sparkwing-hostexec-missing-command").Run()), nil
		},
		"failing": func(ctx context.Context) (hostExecObservation, error) {
			return observe(sparkwing.Bash(ctx, "echo boom >&2; exit 3").Run()), nil
		},
		"env": func(ctx context.Context) (hostExecObservation, error) {
			if err := os.Setenv("HOSTEXEC_SET", "from-pipeline"); err != nil {
				return hostExecObservation{}, err
			}
			return observe(sparkwing.Bash(ctx, `printf '%s|%s|%s' "$HOSTEXEC_SET" "$EXPLICIT" "${SPARKWING_NODE_TOKEN-unset}"`).Env("EXPLICIT", "x").Env("SPARKWING_NODE_TOKEN", "leak").Capture()), nil
		},
		"dir": func(ctx context.Context) (hostExecObservation, error) {
			return observe(sparkwing.Exec(ctx, "pwd").Dir(filepath.Join(dir, "sub")).Capture()), nil
		},
		"measured": func(ctx context.Context) (hostExecObservation, error) {
			return observe(sparkwing.Bash(ctx, `cat "$F"`).Env("F", filepath.Join(dir, "measured.fifo")).Run()), nil
		},
		"background": func(ctx context.Context) (hostExecObservation, error) {
			return observe(sparkwing.Bash(ctx, `sleep 60 & echo $! > "$P"`).Env("P", filepath.Join(dir, "background.pid")).Run()), nil
		},
		"group": func(ctx context.Context) (hostExecObservation, error) {
			ctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
			defer cancel()
			o := observe(sparkwing.Bash(ctx, `timeout 60 sleep 60 & echo $! > "$P"; wait`).Env("P", filepath.Join(dir, "group.pid")).Run())
			o.Survivor = hostExecChildAlive(filepath.Join(dir, "group.pid"))
			return o, nil
		},
		"noshebang": func(ctx context.Context) (hostExecObservation, error) {
			return observe(sparkwing.Exec(ctx, filepath.Join(dir, "bin", "plain")).Capture()), nil
		},
		"lookup": func(ctx context.Context) (hostExecObservation, error) {
			return observe(sparkwing.Exec(ctx, "swlookup").Env("PATH", filepath.Join(dir, "bin")).Capture()), nil
		},
		"cancelled": func(ctx context.Context) (hostExecObservation, error) {
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			marker := filepath.Join(dir, "cancelled")
			o := observe(sparkwing.Bash(ctx, `touch "$F"`).Env("F", marker).Run())
			if _, err := os.Stat(marker); err == nil {
				o.Stdout = "ran"
			}
			return o, nil
		},
		"timeout": func(ctx context.Context) (hostExecObservation, error) {
			pidFile := filepath.Join(dir, "grandchild.pid")
			ctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
			defer cancel()
			o := observe(sparkwing.Bash(ctx, `sleep 30 & echo $! > "$P"; wait`).Env("P", pidFile).Run())
			o.Survivor = hostExecChildAlive(pidFile)
			return o, nil
		},
	}
	var last *sparkwing.WorkStep
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for name := range steps {
			if !yield(name) {
				return
			}
		}
	}) {
		last = sparkwing.Step(w, name, steps[name])
	}
	return last, nil
}

func hostExecChildAlive(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return true
	}
	processes, err := procgroup.List()
	if err != nil {
		return true
	}
	for _, process := range processes {
		if process.PID == pid {
			return !process.Terminated()
		}
	}
	return false
}

type hostExecPipeline struct{}

func (hostExecPipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "job", hostExecJob{})
	sparkwing.Job(plan, "bench", hostExecBenchJob{})
	return nil
}

// TestHostExecSDKChild is the Go SDK process the host exec tests drive.
func TestHostExecSDKChild(t *testing.T) {
	if !slices.Contains(os.Args, "hostexec-sdk") {
		return
	}
	sparkwing.Register[sparkwing.NoInputs]("hostexec", func() sparkwing.Pipeline[sparkwing.NoInputs] { return hostExecPipeline{} })
	if path := os.Getenv("HOSTEXEC_SDK_CPUPROFILE"); path != "" {
		profile, err := os.Create(path)
		if err != nil || pprof.StartCPUProfile(profile) != nil {
			os.Exit(10)
		}
		defer pprof.StopCPUProfile()
	}
	err := sdkserve.Serve(context.Background(), os.NewFile(3, "requests"), os.NewFile(4, "frames"), sdkserve.Config{SDKVersion: "test"})
	pprof.StopCPUProfile()
	if err != nil && !errors.Is(err, context.Canceled) {
		os.Exit(9)
	}
	os.Exit(0)
}

type hostExecHarness struct {
	session *protocolSession
	mu      sync.Mutex
	logs    []nodeproto.FrameLog
}

func hostExecCommand(t testing.TB, lang string) []string {
	t.Helper()
	if lang == "typescript" {
		node, err := exec.LookPath("node")
		if err != nil {
			t.Skip("node is not installed")
		}
		fixture, err := filepath.Abs(filepath.Join("testdata", "hostexec", "pipeline.ts"))
		if err != nil {
			t.Fatal(err)
		}
		return []string{node, fixture}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{exe, "-test.run=^TestHostExecSDKChild$", "--", "hostexec-sdk"}
}

func startHostExecHarness(t testing.TB, lang, dir string, extraEnv ...string) *hostExecHarness {
	t.Helper()
	return startHostExecHarnessWithSink(t, lang, "job", dir, nil, extraEnv...)
}

func startHostExecHarnessWithSink(t testing.TB, lang, job, dir string, sink func(context.Context, nodeproto.FrameLog) error, extraEnv ...string) *hostExecHarness {
	t.Helper()
	return startTrackedHostExecHarness(t, nil, lang, job, dir, sink, extraEnv...)
}

func startTrackedHostExecHarness(t testing.TB, tracker admissionProcessTracker, lang, job, dir string, sink func(context.Context, nodeproto.FrameLog) error, extraEnv ...string) *hostExecHarness {
	t.Helper()
	ctx := context.Background()
	if tracker != nil {
		ctx = context.WithValue(ctx, admissionProcessObserverKey{}, tracker)
	}
	var err error
	masker := secrets.NewMasker()
	if err := masker.Register(hostExecSecret); err != nil {
		t.Fatal(err)
	}
	h := &hostExecHarness{}
	cfg := ProtocolSessionConfig{
		Command:     hostExecCommand(t, lang),
		Environment: testProcessEnv(t, append([]string{"PATH=" + os.Getenv("PATH"), "HOSTEXEC_DIR=" + dir, "GORACE=atexit_sleep_ms=0"}, extraEnv...)...),
		Directory:   dir,
		Masks:       NewMaskRegistry(masker),
		Revoke:      func() error { return nil },
		Log: func(ctx context.Context, log nodeproto.FrameLog) error {
			if sink != nil {
				return sink(ctx, log)
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			h.logs = append(h.logs, log)
			return nil
		},
	}
	h.session, err = startProtocolSession(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.session.Close() })
	h.call(t, nodeproto.Request{Op: nodeproto.OpHello, Protocol: nodeproto.Version, Engine: &nodeproto.EngineIdentity{Version: "test"}, Runner: &nodeproto.RunnerIdentity{Name: "granted", Type: "local", Labels: []string{}}, Binding: &nodeproto.ProcessBinding{RunID: "r", JobID: job, Routes: nodeproto.ProcessRoutes{URL: "http://127.0.0.1:9", Bearer: "private-test-bearer"}}})
	h.call(t, nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: "hostexec", Args: map[string]string{}, Run: &nodeproto.RunContext{RunID: "r", Pipeline: "hostexec"}})
	return h
}

func (h *hostExecHarness) call(t testing.TB, req nodeproto.Request) nodeproto.Reply {
	t.Helper()
	req.ID = "caller"
	reply, err := h.session.Request(context.Background(), req)
	if err != nil || !reply.OK {
		h.mu.Lock()
		defer h.mu.Unlock()
		t.Fatalf("%s = %#v %+v, %v; logs %+v", req.Op, reply, reply.Error, err, h.logs)
	}
	return reply
}

func (h *hostExecHarness) step(t testing.TB, step string) hostExecObservation {
	t.Helper()
	var result nodeproto.RunResult
	if err := h.call(t, nodeproto.Request{Op: nodeproto.OpRunStep, Job: "job", Step: step, Attempt: 1, Settled: []string{}}).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "success" {
		t.Fatalf("%s: %#v", step, result.Error)
	}
	var o hostExecObservation
	if err := json.Unmarshal(result.Output, &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func (h *hostExecHarness) hosted() uint64 {
	h.session.mu.Lock()
	defer h.session.mu.Unlock()
	return h.session.execSeq
}

func (h *hostExecHarness) messages(step string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var lines []string
	for _, log := range h.logs {
		if log.Step == step && log.Event == "exec_line" {
			lines = append(lines, log.Message)
		}
	}
	return strings.Join(lines, "\n")
}

// Both language clients exercise the same host command contract.
func TestHostExecMatchesSDKExecution(t *testing.T) {
	for _, lang := range []string{"go", "typescript"} {
		t.Run(lang, func(t *testing.T) { checkHostExecScenarios(t, lang, "host") })
	}
}

func checkHostExecScenarios(t *testing.T, lang, mode string) {
	{
		{
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(dir, "bin"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "bin", "swlookup"), []byte("#!/bin/sh\necho rel-found\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "bin", "plain"), []byte("echo plain-ran\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			h := startHostExecHarness(t, lang, dir)

			logged := h.step(t, "logged")
			wantLogged := "line one\n" + hostExecSecret + " two\n"
			if mode == "host" {
				wantLogged = "line one\n*** two\n"
			}
			if logged.Error != "" || logged.Stdout != wantLogged || logged.Stderr != "err text" {
				t.Fatalf("logged result = %#v", logged)
			}
			if got := h.messages("logged"); !strings.Contains(got, "line one") || !strings.Contains(got, "err text") || strings.Contains(got, hostExecSecret) || !strings.Contains(got, "*** two") {
				t.Fatalf("logged output was not masked into the job log: %q", got)
			}

			captured := h.step(t, "captured")
			if captured.Error != "" || captured.Stdout != "captured-out" || captured.Stderr != "captured-err" || h.messages("captured") != "" {
				t.Fatalf("captured = %#v, logs %q", captured, h.messages("captured"))
			}
			if big := h.step(t, "big"); big.Error != "" || big.Stdout != "300000" {
				t.Fatalf("big capture = %#v", big)
			}
			if missing := h.step(t, "missing"); !missing.NotFound || missing.Exit != sparkwing.ExitNotStarted {
				t.Fatalf("missing = %#v", missing)
			}
			if failing := h.step(t, "failing"); failing.Exit != 3 || !strings.Contains(failing.Error, "command failed (exit 3)") || !strings.Contains(failing.Error, "boom") || failing.Terminated != "" {
				t.Fatalf("failing = %#v", failing)
			}
			wantToken := "unset"
			if env := h.step(t, "env"); env.Stdout != "from-pipeline|x|"+wantToken {
				t.Fatalf("env = %#v", env)
			}
			if dirResult := h.step(t, "dir"); dirResult.Stdout != filepath.Join(dir, "sub")+"\n" {
				t.Fatalf("dir = %#v", dirResult)
			}
			// safety: the host resolves both clients against the command environment.
			if lookup := h.step(t, "lookup"); lookup.Stdout != "rel-found\n" {
				t.Errorf("lookup = %#v", lookup)
			}
			// hack: executable text follows execvp semantics through the host shell.
			if plain := h.step(t, "noshebang"); plain.Stdout != "plain-ran\n" {
				t.Errorf("noshebang = %#v", plain)
			}
			if lang == "go" {
				before := h.hosted()
				if cancelled := h.step(t, "cancelled"); cancelled.Error == "" || cancelled.Stdout == "ran" || h.hosted() != before {
					t.Errorf("an already-cancelled context dispatched its command: %#v", cancelled)
				}
				if group := h.step(t, "group"); mode == "host" && group.Survivor {
					t.Errorf("cancellation left a child that moved to its own process group: %#v", group)
				}
			} else if mode == "host" {
				// hack: Node's own spawn waits for the background child or fails with EPIPE here, by timing.
				if stdin := h.step(t, "stdin"); stdin.Error != "" {
					t.Errorf("stdin held by a background child = %#v", stdin)
				}
			}
			// hack: TypeScript has no deadline of its own; an aborted signal reports cancellation.
			wantTerminated, wantMessage := "timeout", "command timed out"
			if lang == "typescript" {
				wantTerminated, wantMessage = "cancellation", "command terminated by cancellation"
			}
			h.session.mu.Lock()
			hosted := h.session.execSeq
			h.session.mu.Unlock()
			if (mode == "host") != (hosted > 0) {
				t.Fatalf("%s mode ran %d commands through the host", mode, hosted)
			}
			if timeout := h.step(t, "timeout"); timeout.Terminated != wantTerminated || timeout.Exit != -1 || timeout.Survivor || !strings.Contains(timeout.Error, wantMessage) {
				t.Fatalf("timeout = %#v", timeout)
			}
		}
	}
}

func pidAlive(t *testing.T, path string) (int, bool) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return pid, syscall.Kill(pid, 0) == nil
}

type commandMeasurementTracker struct {
	tracked chan [2]string
}

func (commandMeasurementTracker) TrackProcess(context.Context, int) error { return nil }

func (c commandMeasurementTracker) TrackCommand(_ context.Context, pid int, birth string) error {
	c.tracked <- [2]string{strconv.Itoa(pid), birth}
	return nil
}

// A command that outlives the registration delay joins the job's machine measurement under its own session leader.
func TestHostExecRegistersLongCommandsForMeasurement(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "measured.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	tracker := commandMeasurementTracker{tracked: make(chan [2]string, 1)}
	h := startTrackedHostExecHarness(t, tracker, "go", "job", dir, nil)
	release := make(chan error, 1)
	go func() {
		tracked := <-tracker.tracked
		pid, err := strconv.Atoi(tracked[0])
		if err == nil {
			var birth string
			birth, err = procgroup.ProcessBirth(pid)
			if err == nil && (birth != tracked[1] || pid == h.session.cmd.Process.Pid) {
				err = errors.New("tracked " + tracked[0] + " " + tracked[1] + ", live birth " + birth)
			}
		}
		if file, openErr := os.OpenFile(fifo, os.O_WRONLY, 0); openErr == nil {
			_ = file.Close()
		}
		release <- err
	}()
	if measured := h.step(t, "measured"); measured.Error != "" {
		t.Fatalf("measured = %#v", measured)
	}
	if err := <-release; err != nil {
		t.Fatal(err)
	}
}

// A daemon a command starts with & outlives the command, as it did when the SDK ran
// it, but not the SDK session that started it.
func TestHostExecBackgroundChildEndsWithTheSession(t *testing.T) {
	dir := t.TempDir()
	h := startHostExecHarness(t, "go", dir)
	if background := h.step(t, "background"); background.Error != "" {
		t.Fatalf("background = %#v", background)
	}
	pid, alive := pidAlive(t, filepath.Join(dir, "background.pid"))
	if !alive {
		t.Fatal("a background child died when its command's leader exited")
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if err := h.session.Close(); err != nil {
		t.Fatal(err)
	}
	if hostExecChildAlive(filepath.Join(dir, "background.pid")) {
		t.Fatal("a background child outlived the SDK session")
	}
}

// Close must finish even when the log sink stops accepting a command's output.
func TestHostExecCloseBoundsABlockedLogSink(t *testing.T) {
	blocked := make(chan struct{})
	var once sync.Once
	sink := func(ctx context.Context, log nodeproto.FrameLog) error {
		if log.Event != "exec_line" {
			return nil
		}
		once.Do(func() { close(blocked) })
		<-ctx.Done()
		return ctx.Err()
	}
	h := startHostExecHarnessWithSink(t, "go", "job", t.TempDir(), sink)
	go func() {
		_, _ = h.session.Request(context.Background(), nodeproto.Request{ID: "logged", Op: nodeproto.OpRunStep, Job: "job", Step: "logged", Attempt: 1, Settled: []string{}})
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatal("command output never reached the sink")
	}
	closed := make(chan error, 1)
	go func() { closed <- h.session.Close() }()
	closeCtx, closeCancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer closeCancel()
	select {
	case <-closed:
	case <-closeCtx.Done():
		t.Fatal("Close waited forever on a command blocked in the log sink")
	}
}
