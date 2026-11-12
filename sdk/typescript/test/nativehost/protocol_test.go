package nativehost_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto/conformance"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestNativeProtocol(t *testing.T) {
	t.Run("pipe_object_identity", TestProtocolToolChecksPipeObjectIdentity)
	node, root := nativeSDKFixture(t)
	t.Run("refuses_regular_files", func(t *testing.T) {
		files := make([]*os.File, 2)
		for i := range files {
			file, err := os.CreateTemp(t.TempDir(), "protocol")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = file.Close() })
			files[i] = file
		}
		command := exec.CommandContext(t.Context(), node, filepath.Join(root, "test/nativehost/fixture.mjs"), "--sw-node-protocol")
		command.Dir = root
		attach(t, command, files[0], files[1])
		output, err := command.CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(string(output), "pipe") {
			t.Fatalf("non-pipe descriptors: %v\n%s", err, output)
		}
	})
	h := startSDK(t, node, root)
	nativeHello(t, h)
	plan := nativePlan(t, h)
	output := nativeProbe(t, h)
	nativeEvals(t, h, plan.Jobs[0], output)
	nativeCancel(t, h)
	h.finish()
	if !strings.Contains(h.stdout.String(), "forged") || strings.Contains(h.transcript.String(), "forged") || !strings.Contains(h.transcript.String(), "native-debug") {
		t.Fatal("stdout/frame isolation or debug forwarding failed")
	}
	for _, text := range []string{h.stdout.String(), h.stderr.String(), h.transcript.String()} {
		if strings.Contains(text, "hello-only-bearer") || strings.Contains(text, "legacy-env-bearer") {
			t.Fatal("protocol credential escaped into output")
		}
	}
}

func TestProtocolToolChecksPipeObjectIdentity(t *testing.T) {
	for _, mode := range []string{"closed", "inherited", "unrelated"} {
		t.Run(mode, func(t *testing.T) {
			pipe := func() (*os.File, *os.File) {
				t.Helper()
				read, write, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = read.Close(); _ = write.Close() })
				return read, write
			}
			request, _ := pipe()
			_, frame := pipe()
			other, _ := pipe()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(t.Context(), executable, "-test.run=^TestProtocolTool$")
			identities := attach(t, command, request, frame)
			var inherited *os.File
			if mode == "inherited" {
				inherited = request
			} else if mode == "unrelated" {
				inherited = other
			}
			identities = inheritProbePipe(t, command, inherited, identities)
			command.Args = []string{executable, "-test.run=^TestProtocolTool$", "--", identities}
			output, err := command.CombinedOutput()
			if mode == "inherited" {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(output), "inherited") {
					t.Fatalf("inherited protocol pipe was not detected: %v\n%s", err, output)
				}
				t.Logf("inherited protocol pipe rejected:\n%s", output)
			} else if err != nil {
				t.Fatalf("%s protocol pipe: %v\n%s", mode, err, output)
			}
		})
	}
}

func nativeHello(t *testing.T, h *host) {
	t.Helper()
	var hello nodewire.HelloResult
	h.call(nodewire.Request{
		ID: "hello", Op: nodewire.OpHello, Protocol: nodewire.Version,
		Engine: &nodewire.EngineIdentity{Version: "v0.71.0"},
		Runner: &nodewire.RunnerIdentity{Name: "granted", Type: "runner", Labels: []string{}},
		Binding: &nodewire.ProcessBinding{RunID: "run", JobID: "build", Routes: nodewire.ProcessRoutes{
			URL: "http://127.0.0.1:1", Bearer: "hello-only-bearer",
		}},
	}, &hello)
	if hello.Protocol != 2 || hello.SDK.Language != "typescript" || hello.SDK.Version == "" {
		t.Fatalf("hello: %+v", hello)
	}
	manifest, err := os.ReadFile(filepath.Join(h.command.Dir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var identity struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(manifest, &identity); err != nil {
		t.Fatal(err)
	}
	if hello.SDK.Version != "v"+identity.Version {
		t.Fatalf("hello SDK version %q, package version %q", hello.SDK.Version, identity.Version)
	}
	var describe nodewire.DescribeDoc
	h.call(nodewire.Request{ID: "describe", Op: nodewire.OpDescribe}, &describe)
	if len(describe.Pipelines) != 1 || describe.Pipelines[0].Name != "native" || len(describe.Pipelines[0].Secrets) != 1 {
		t.Fatalf("describe: %+v", describe)
	}
}

func nativePlan(t *testing.T, h *host) nodewire.PlanDoc {
	t.Helper()
	probe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var plan nodewire.PlanDoc
	h.call(nodewire.Request{
		ID: "plan", Op: nodewire.OpPlan, Pipeline: "native", Args: map[string]string{"probe": probe, "pipes": h.pipes},
		Run: &nodewire.RunContext{RunID: "run", Pipeline: "invoked-alias"},
	}, &plan)
	if len(plan.Jobs) != 1 || plan.Jobs[0].ID != "build" || len(plan.Jobs[0].Steps) != 2 || plan.Jobs[0].ResultStep != "probe" {
		t.Fatalf("plan: %+v", plan)
	}
	if plan.Priority == nil || *plan.Priority != 0 || plan.ClaimWaitMS == nil || *plan.ClaimWaitMS != 0 ||
		plan.Source == nil || plan.Source.FullHistory == nil || *plan.Source.FullHistory || plan.Source.Tags == nil || *plan.Source.Tags {
		t.Fatalf("explicit zero/false lost: %+v", plan)
	}
	job := plan.Jobs[0]
	if job.Retry == nil || job.Retry.Attempts != 0 || job.Retry.BackoffMS == nil || *job.Retry.BackoffMS != 0 ||
		len(plan.Concurrency) != 1 || (plan.Concurrency[0].Scope == nil || *plan.Concurrency[0].Scope != "box") {
		t.Fatalf("job or concurrency policy lost: %+v", plan)
	}
	return plan
}

func nativeProbe(t *testing.T, h *host) json.RawMessage {
	t.Helper()
	var run nodewire.RunResult
	h.call(nodewire.Request{ID: "probe", Op: nodewire.OpRunStep, Job: "build", Step: "probe", Attempt: 1, Settled: []string{}}, &run)
	if run.Outcome != "success" || run.Error != nil {
		t.Fatalf("probe: %+v", run)
	}
	var output struct {
		Tool struct {
			HandlesClosed bool   `json:"handles_closed"`
			Body          string `json:"body"`
			Token         string `json:"token"`
		} `json:"tool"`
		Runner   string `json:"runner"`
		Pipeline string `json:"pipeline"`
		Arch     string `json:"arch"`
	}
	decode(t, run.Output, &output)
	if !output.Tool.HandlesClosed || output.Tool.Body != "body-value" || output.Tool.Token != "" || output.Runner != "granted" || output.Pipeline != "invoked-alias" {
		t.Fatalf("body/tool context: %+v", output)
	}
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	} else if arch == "386" {
		arch = "ia32"
	}
	if output.Arch != arch {
		t.Fatalf("native %s lane requires %s Node, got %s", runtime.GOARCH, arch, output.Arch)
	}
	return run.Output
}

func nativeEvals(t *testing.T, h *host, job nodewire.JobSpec, output json.RawMessage) {
	t.Helper()
	for _, tc := range []struct{ id, closure, input, want string }{
		{"skip", job.Closures.SkipIf[0], "", "false"},
		{"before", job.Closures.BeforeRun[0], "", "null"},
		{"verify", job.Closures.Verify, "", "null"},
		{"after", job.Closures.AfterRun[0], `{"outcome":"success"}`, "null"},
		{"memo", job.Closures.MemoKey, "", `{"version":"native","enabled":true}`},
	} {
		var result nodewire.EvalResult
		h.call(nodewire.Request{ID: tc.id, Op: nodewire.OpEval, Closure: tc.closure, Input: json.RawMessage(tc.input)}, &result)
		if result.Error != nil || string(result.Value) != tc.want {
			t.Fatalf("%s: %+v", tc.id, result)
		}
	}
	var generated nodewire.EvalResult
	h.call(nodewire.Request{ID: "generate", Op: nodewire.OpEval, Closure: job.Closures.Generate[0], Input: output}, &generated)
	var document nodewire.PlanDoc
	decode(t, generated.Value, &document)
	jobs := document.Jobs
	if generated.Error != nil || len(jobs) != 1 || jobs[0].ID != "generated" || len(jobs[0].Groups) != 1 || jobs[0].Groups[0] != job.Closures.Generate[0] {
		t.Fatalf("generated: %+v, error: %v", jobs, generated.Error)
	}
}

func nativeCancel(t *testing.T, h *host) {
	t.Helper()
	h.send(nodewire.Request{ID: "wait", Op: nodewire.OpRunStep, Job: "build", Step: "wait", Attempt: 1, Settled: []string{"probe"}})
	for h.frame().Message != "waiting-for-cancel" {
	}
	h.send(nodewire.Request{ID: "cancel", Op: nodewire.OpCancel, Job: "build", Step: "wait"})
	replies := map[string]nodewire.Reply{}
	for len(replies) < 2 {
		frame := h.frame()
		if frame.ID == "" {
			continue
		}
		if frame.ID != "wait" && frame.ID != "cancel" {
			t.Fatalf("unexpected reply: %+v", frame.Reply)
		}
		if _, exists := replies[frame.ID]; exists || !frame.OK {
			t.Fatalf("duplicate or failed reply: %+v", frame.Reply)
		}
		replies[frame.ID] = frame.Reply
	}
	var run nodewire.RunResult
	decode(t, replies["wait"].Result, &run)
	if run.Outcome != "failed" || run.Error == nil {
		t.Fatalf("cancelled invocation: %+v", run)
	}
}

type host struct {
	recording                  *conformance.Recording
	t                          *testing.T
	command                    *exec.Cmd
	requests                   *os.File
	frames                     *bufio.Scanner
	pipes                      string
	stdout, stderr, transcript bytes.Buffer
	waited                     bool
}

func startSDK(t *testing.T, node, root string) *host {
	t.Helper()
	return startSDKAt(t, node, root, "fixture.mjs")
}

func startSDKAt(t *testing.T, node, root, fixture string) *host {
	t.Helper()
	// safety: A broken native transport must not leave a child holding the test pipes forever.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	h := &host{t: t}
	requestRead, requestWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = requestRead.Close(); _ = requestWrite.Close() })
	frameRead, frameWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = frameRead.Close(); _ = frameWrite.Close() })
	h.command = exec.CommandContext(ctx, node, filepath.Join(root, "test/nativehost", fixture), "--sw-node-protocol")
	h.command.Dir = root
	h.command.Env = append(os.Environ(), "SPARKWING_NODE_TOKEN=legacy-env-bearer")
	h.command.Stdout, h.command.Stderr = &h.stdout, &h.stderr
	h.pipes = attach(t, h.command, requestRead, frameWrite)
	h.requests, h.frames = requestWrite, bufio.NewScanner(frameRead)
	if err := h.command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		if !h.waited {
			_ = h.command.Wait()
		}
	})
	_ = requestRead.Close()
	_ = frameWrite.Close()
	return h
}

func (h *host) send(request nodewire.Request) {
	h.t.Helper()
	if h.recording != nil {
		h.recording.Request(h.t, request)
	}
	if err := json.NewEncoder(h.requests).Encode(request); err != nil {
		h.t.Fatal(err)
	}
}

type wireFrame struct {
	nodewire.Reply
	nodewire.FrameLog
}

func (h *host) frame() wireFrame {
	h.t.Helper()
	if !h.frames.Scan() {
		h.finish()
		h.t.Fatalf("frame EOF: %v\n%s", h.frames.Err(), h.stderr.String())
	}
	line := h.frames.Bytes()
	if h.recording != nil {
		h.recording.Frame(h.t, line)
	}
	parsed, err := nodewire.ParseFrame(line)
	if err != nil {
		h.t.Fatalf("frame: %v\n%s", err, line)
	}
	if parsed.Exec != nil {
		req := *parsed.Exec
		write := func(value any) error {
			body, err := json.Marshal(value)
			if err != nil {
				return err
			}
			_, err = h.requests.Write(append(body, '\n'))
			return err
		}
		done := nodehost.ExecuteCommand(h.t.Context(), req, func(stream string, data []byte, eof bool) error {
			if !req.Capture || eof {
				return nil
			}
			return write(nodewire.ExecOutput{ID: req.ID, Stream: stream, Data: data})
		})
		if err := write(done); err != nil {
			h.t.Fatal(err)
		}
		return wireFrame{}
	}
	h.transcript.Write(line)
	h.transcript.WriteByte('\n')
	var frame wireFrame
	if parsed.Reply != nil {
		frame.Reply = *parsed.Reply
	}
	if parsed.Log != nil {
		frame.FrameLog = *parsed.Log
	}
	return frame
}

func (h *host) call(request nodewire.Request, result any) {
	h.t.Helper()
	h.send(request)
	for {
		frame := h.frame()
		if frame.ID == "" {
			continue
		}
		if frame.ID != request.ID || !frame.OK || frame.Error != nil {
			h.t.Fatalf("%s: %+v", request.ID, frame.Reply)
		}
		if err := frame.Reply.Decode(result); err != nil {
			h.t.Fatalf("reply %s: %v", request.ID, err)
		}
		return
	}
}

func (h *host) finish() {
	h.t.Helper()
	if h.waited {
		return
	}
	_ = h.requests.Close()
	err := h.command.Wait()
	h.waited = true
	if err != nil {
		h.t.Fatalf("SDK exit: %v\n%s", err, h.stderr.String())
	}
}

func decode(t *testing.T, raw []byte, target any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("decode %T: %v\n%s", target, err, raw)
	}
}

func TestProtocolTool(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == "--" {
			index = i + 1
		}
	}
	if index == -1 {
		return
	}
	if index >= len(os.Args) {
		t.Fatal("missing pipe identities")
	}
	checkClosed(t, os.Args[index])
	output := map[string]any{"handles_closed": true, "body": os.Getenv("W5_NATIVE_BODY"), "token": os.Getenv("SPARKWING_NODE_TOKEN")}
	if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
