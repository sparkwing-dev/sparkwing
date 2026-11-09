//go:build unix

package nodehost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func protocolTestCodec(line []byte) (nodewire.Frame, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return nodewire.Frame{}, err
	}
	if _, ok := fields["reply"]; ok {
		var reply nodewire.Reply
		if err := json.Unmarshal(line, &reply); err != nil {
			return nodewire.Frame{}, err
		}
		return nodewire.Frame{Reply: &reply}, nil
	}
	var log nodewire.FrameLog
	if err := json.Unmarshal(line, &log); err != nil {
		return nodewire.Frame{}, err
	}
	return nodewire.Frame{Log: &log}, nil
}

func protocolTestSession(t *testing.T, mode string, edit func(*ProtocolSessionConfig)) *protocolSession {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := ProtocolSessionConfig{Command: []string{exe, "-test.run=^TestProtocolSessionChild$", "--", mode}, Environment: testProcessEnv(t, "GORACE=atexit_sleep_ms=0"), ParseFrame: protocolTestCodec, ValidateRequest: func(nodewire.Request) error { return nil }, Revoke: func() error { return nil }}
	if edit != nil {
		edit(&cfg)
	}
	session, err := startProtocolSession(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func protocolTestHello(t *testing.T, session *protocolSession) {
	t.Helper()
	reply, err := session.Request(t.Context(), nodewire.Request{ID: "caller-hello", Op: nodewire.OpHello, Protocol: nodewire.Version, Engine: &nodewire.EngineIdentity{Version: "test"}, Runner: &nodewire.RunnerIdentity{Name: "granted", Type: "local"}, Binding: &nodewire.ProcessBinding{RunID: "r", JobID: "job", Routes: nodewire.ProcessRoutes{URL: "http://host", Bearer: "private-test-bearer"}}})
	if err != nil || !reply.OK || reply.ID != "caller-hello" {
		t.Fatalf("hello = %#v, %v", reply, err)
	}
}

func TestProtocolSessionConcurrentRepliesAndAttribution(t *testing.T) {
	var mu sync.Mutex
	var logs []nodewire.FrameLog
	var progress atomic.Int64
	session := protocolTestSession(t, "concurrent", func(cfg *ProtocolSessionConfig) {
		cfg.Activity.Progress = func() { progress.Add(1) }
		cfg.Log = func(_ context.Context, log nodewire.FrameLog) error {
			mu.Lock()
			defer mu.Unlock()
			logs = append(logs, log)
			return nil
		}
	})
	protocolTestHello(t, session)
	replies := make(chan nodewire.Reply, 2)
	errs := make(chan error, 2)
	for _, step := range []string{"first", "second"} {
		go func() {
			reply, err := session.Request(t.Context(), nodewire.Request{ID: step, Op: nodewire.OpRunStep, Job: "job", Step: step, Attempt: 1})
			replies <- reply
			errs <- err
		}()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for range 2 {
		reply := <-replies
		var result struct {
			Step   string `json:"step"`
			WireID string `json:"wire_id"`
		}
		if err := json.Unmarshal(reply.Result, &result); err != nil {
			t.Fatal(err)
		}
		if result.Step != reply.ID || len(result.WireID) != 32 || result.WireID == reply.ID {
			t.Fatalf("misrouted reply: %#v -> %#v", reply, result)
		}
		seen[reply.ID] = true
	}
	if !seen["first"] || !seen["second"] {
		t.Fatal(seen)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, want := range []struct{ message, step, level string }{{"active", "first", "info"}, {"forged", "", "info"}, {"late", "", "info"}, {"plain stdout", "", "info"}, {"plain stderr", "", "warn"}} {
		if !slices.ContainsFunc(logs, func(log nodewire.FrameLog) bool {
			return log.Message == want.message && log.Step == want.step && log.Level == want.level
		}) {
			t.Errorf("missing %v in %#v", want, logs)
		}
	}
	if progress.Load() < 5 {
		t.Fatal("log activity did not renew progress")
	}
}

func TestProtocolSessionCancelPreservesSessionForCleanup(t *testing.T) {
	entered := make(chan struct{})
	cancelSeen, releaseJoin := make(chan struct{}), make(chan struct{})
	var bodyJoined atomic.Bool
	session := protocolTestSession(t, "cancel", func(cfg *ProtocolSessionConfig) {
		cfg.Log = func(_ context.Context, log nodewire.FrameLog) error {
			if log.Message == "entered" {
				close(entered)
			}
			if log.Message == "cancel_received" {
				close(cancelSeen)
				<-releaseJoin
			}
			if log.Message == "body_joined" {
				bodyJoined.Store(true)
			}
			return nil
		}
	})
	protocolTestHello(t, session)
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := session.Request(ctx, nodewire.Request{ID: "step", Op: nodewire.OpRunStep, Job: "job", Step: "body", Attempt: 1})
		result <- err
	}()
	<-entered
	if _, err := session.Request(t.Context(), nodewire.Request{Op: nodewire.OpRunStep, Job: "job", Step: "body", Attempt: 1}); err == nil {
		t.Fatal("accepted duplicate active step")
	}
	if _, err := session.Request(t.Context(), nodewire.Request{Op: nodewire.OpPlan}); err == nil {
		t.Fatal("accepted plan while body is active")
	}
	cancel()
	<-cancelSeen
	select {
	case err := <-result:
		close(releaseJoin)
		t.Fatalf("cancelled request returned before body joined: %v", err)
	default:
	}
	close(releaseJoin)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !bodyJoined.Load() {
		t.Fatal("request returned before original body joined")
	}
	reply, err := session.Request(t.Context(), nodewire.Request{ID: "cleanup", Op: nodewire.OpEval, Job: "job", Closure: "finally"})
	if err != nil || !reply.OK {
		t.Fatalf("cleanup = %#v, %v", reply, err)
	}
	if _, err := session.Request(t.Context(), nodewire.Request{Op: nodewire.OpPlan}); err != nil {
		t.Fatal("refused plan consumed plan chance:", err)
	}
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProtocolSessionRejectsDuplicateAndUnknownReplies(t *testing.T) {
	for _, mode := range []string{"duplicate", "unknown", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			session := protocolTestSession(t, mode, nil)
			protocolTestHello(t, session)
			_, _ = session.Request(t.Context(), nodewire.Request{ID: "describe", Op: nodewire.OpDescribe})
			err := session.Close()
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.ProtocolError {
				t.Fatalf("close error = %v", err)
			}
		})
	}
}

func TestProtocolSessionCancelledEvalJoinsTargetAndKeepsSession(t *testing.T) {
	entered := make(chan struct{})
	var cancelSeen, cleaned atomic.Bool
	session := protocolTestSession(t, "eval_cancel", func(cfg *ProtocolSessionConfig) {
		cfg.Log = func(_ context.Context, log nodewire.FrameLog) error {
			if log.Message == "entered" {
				close(entered)
			}
			if log.Message == "cancel_seen" {
				cancelSeen.Store(true)
			}
			return nil
		}
		cfg.RunCleanups = func(context.Context, CleanupScope) error { cleaned.Store(true); return nil }
	})
	protocolTestHello(t, session)
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	result := make(chan error, 1)
	go func() {
		_, err := session.Request(ctx, nodewire.Request{ID: "caller-eval", Op: nodewire.OpEval, Closure: "before_run"})
		result <- err
	}()
	<-entered
	cause := errors.New("eval attempt deadline")
	cancel(cause)
	err := <-result
	if !errors.Is(err, cause) {
		t.Fatalf("eval cancellation lost its cause: %v", err)
	}
	if !cancelSeen.Load() {
		t.Fatal("eval cancellation did not target and join its private request")
	}
	if _, err := session.Request(t.Context(), nodewire.Request{Op: nodewire.OpDescribe}); err != nil {
		t.Fatalf("session unavailable after eval cancellation: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("intentional eval stop became a close failure: %v", err)
	}
	if err := session.Cleanup(t.Context()); err != nil || !cleaned.Load() {
		t.Fatalf("host cleanup did not run: %v", err)
	}
}

func TestProtocolSessionHelloEnforced(t *testing.T) {
	session := protocolTestSession(t, "normal", nil)
	if _, err := session.Request(t.Context(), nodewire.Request{Op: nodewire.OpDescribe}); err == nil {
		t.Fatal("accepted request before hello")
	}
	protocolTestHello(t, session)
	if _, err := session.Request(t.Context(), nodewire.Request{Op: nodewire.OpHello}); err == nil {
		t.Fatal("accepted second hello")
	}
	if _, err := session.Request(t.Context(), nodewire.Request{Op: nodewire.OpPlan}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Request(t.Context(), nodewire.Request{Op: nodewire.OpPlan}); err == nil {
		t.Fatal("accepted second plan")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProtocolSessionNegotiationFailureIsActionable(t *testing.T) {
	for _, mode := range []string{"mismatch", "old"} {
		t.Run(mode, func(t *testing.T) {
			session := protocolTestSession(t, mode, nil)
			_, err := session.Request(t.Context(), nodewire.Request{Op: nodewire.OpHello})
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.ProtocolMismatch || !strings.Contains(strings.ToLower(err.Error()+failure.Hint), "rebuild") {
				t.Fatalf("hello error = %v", err)
			}
		})
	}
}

func TestProtocolSessionCloseRevokesAndJoinsBeforeCleanup(t *testing.T) {
	var revoked, cleaned atomic.Bool
	session := protocolTestSession(t, "normal", func(cfg *ProtocolSessionConfig) {
		cfg.Revoke = func() error { revoked.Store(true); return nil }
		cfg.RunCleanups = func(context.Context, CleanupScope) error {
			if !revoked.Load() {
				return errors.New("cleanup before revocation")
			}
			cleaned.Store(true)
			return nil
		}
	})
	protocolTestHello(t, session)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if err := session.Close(); err != nil {
				t.Errorf("close = %v", err)
			}
		})
	}
	wg.Wait()
	if session.cmd.ProcessState == nil || !revoked.Load() {
		t.Fatal("Close returned without reaping or revoking")
	}
	if err := session.Cleanup(t.Context()); err != nil || !cleaned.Load() {
		t.Fatal(err)
	}
	if _, err := session.Request(t.Context(), nodewire.Request{Op: nodewire.OpDescribe}); err == nil {
		t.Fatal("request after Close succeeded")
	}
}

func TestProtocolSessionValidationStopsUnsentRequests(t *testing.T) {
	sentinel := errors.New("contract rejected request")
	session := protocolTestSession(t, "normal", func(cfg *ProtocolSessionConfig) {
		cfg.ValidateRequest = func(nodewire.Request) error { return sentinel }
	})
	if _, err := session.Request(t.Context(), nodewire.Request{Op: nodewire.OpHello}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProtocolSessionBoundHelloRequiresRevocation(t *testing.T) {
	session := protocolTestSession(t, "normal", func(cfg *ProtocolSessionConfig) { cfg.Revoke = nil })
	_, err := session.Request(t.Context(), nodewire.Request{Op: nodewire.OpHello, Binding: &nodewire.ProcessBinding{Routes: nodewire.ProcessRoutes{Bearer: "bound"}}})
	if err == nil || !strings.Contains(err.Error(), "revocation") {
		t.Fatalf("bound hello without revocation = %v", err)
	}
	if _, err := session.Request(t.Context(), nodewire.Request{Op: nodewire.OpHello}); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProtocolSessionCancelledWriteReapsBlockedProcess(t *testing.T) {
	validated := make(chan struct{})
	session := protocolTestSession(t, "blocked", func(cfg *ProtocolSessionConfig) {
		cfg.ValidateRequest = func(request nodewire.Request) error {
			if request.Op == nodewire.OpEval {
				close(validated)
			}
			return nil
		}
	})
	protocolTestHello(t, session)
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := session.Request(ctx, nodewire.Request{Op: nodewire.OpEval, Job: "job", Closure: "blocked", Input: json.RawMessage(`"` + strings.Repeat("x", 1<<20) + `"`)})
		result <- err
	}()
	<-validated
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	err := session.Close()
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.ProtocolError || session.cmd.ProcessState == nil {
		t.Fatalf("Close did not fail and reap: %v", err)
	}
}

func TestProtocolSessionChild(t *testing.T) {
	index := slices.Index(os.Args, "--")
	if index < 0 {
		return
	}
	mode := os.Args[index+1]
	requests := os.NewFile(3, "requests")
	frames := os.NewFile(4, "frames")
	defer requests.Close()
	defer frames.Close()
	writer := json.NewEncoder(frames)
	send := func(value any) {
		if err := writer.Encode(value); err != nil {
			os.Exit(2)
		}
	}
	reply := func(request nodewire.Request, result any) {
		data, _ := json.Marshal(result)
		send(nodewire.Reply{ID: request.ID, OK: true, Result: data})
	}
	var held []nodewire.Request
	var cleanup *nodewire.Request
	scanner := bufio.NewScanner(requests)
	for scanner.Scan() {
		var request nodewire.Request
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(3)
		}
		if request.Op == nodewire.OpHello {
			for _, value := range append(os.Environ(), os.Args...) {
				if strings.Contains(value, "private-test-bearer") {
					os.Exit(4)
				}
			}
			if mode == "old" {
				send(nodewire.Reply{ID: request.ID, Error: &apierr.Error{Code: apierr.UnknownOp, Message: "unknown op"}})
				continue
			}
			version := nodewire.Version
			if mode == "mismatch" {
				version++
			}
			sdkVersion := "test"
			if mode == "owner_expand" || mode == "owner_policy" || mode == "planner" || strings.HasPrefix(mode, "claimed_node") || strings.HasPrefix(mode, "startup_") {
				sdkVersion = "v1.0.0"
			}
			if mode == "planner" && request.Binding != nil {
				os.Exit(8)
			}
			reply(request, nodewire.HelloResult{Protocol: version, SDK: nodewire.SDKIdentity{Language: "go", Version: sdkVersion}})
			if mode == "blocked" {
				reader, writer, err := os.Pipe()
				if err != nil {
					os.Exit(5)
				}
				defer reader.Close()
				defer writer.Close()
				_, _ = reader.Read(make([]byte, 1))
			}
			continue
		}
		switch mode {
		case "partial_output":
			fmt.Fprint(os.Stdout, "starting")
		case "log_policy":
			send(nodewire.FrameLog{Level: "debug", Message: "debug detail"})
			send(nodewire.FrameLog{Level: "info", Message: "normal detail"})
			reply(request, map[string]bool{})
		case "startup_stable", "startup_change":
			switch request.Op {
			case nodewire.OpPlan:
				job := sdkStartupJob(strings.TrimPrefix(mode, "startup_"))
				if os.Getenv("NODE_OPTIONS") == "change" {
					job.Steps[0].ID = "changed"
				}
				reply(request, nodewire.PlanDoc{Jobs: []nodewire.JobSpec{job}})
			case nodewire.OpRunStep:
				send(nodewire.FrameLog{Message: "body entered"})
				reply(request, nodewire.RunResult{Outcome: "success"})
			default:
				os.Exit(12)
			}
		case "environment":
			reply(request, os.Environ())
		case "claimed_node", "claimed_node_hold":
			switch request.Op {
			case nodewire.OpPlan:
				reply(request, nodewire.PlanDoc{Jobs: []nodewire.JobSpec{{ID: "build", Steps: []nodewire.StepSpec{{ID: "body"}}}}})
			case nodewire.OpRunStep:
				if mode == "claimed_node_hold" {
					held = append(held, request)
					send(nodewire.FrameLog{Level: "info", Message: "entered"})
				} else {
					reply(request, nodewire.RunResult{Outcome: "success"})
				}
			case nodewire.OpCancel:
				reply(request, map[string]bool{"cancelled": true})
				for _, pending := range held {
					reply(pending, nodewire.RunResult{Outcome: "cancelled"})
				}
				held = nil
			default:
				os.Exit(11)
			}
		case "planner":
			switch request.Op {
			case nodewire.OpDescribe:
				secret := "private-plan-default"
				send(nodewire.FrameLog{Level: "info", Message: secret, Attrs: json.RawMessage(`{"escaped":"private-plan-def\u0061ult"}`)})
				fmt.Fprintln(os.Stdout, secret)
				fmt.Fprintln(os.Stderr, secret)
				reply(request, nodewire.DescribeDoc{Pipelines: []nodewire.PipelineDescription{{Name: "build", Args: []nodewire.ArgumentDescription{{Name: "token", Type: "string", Secret: true, Default: &secret}}}}})
			case nodewire.OpPlan:
				if request.Args["token"] != "private-plan-default" || request.Run.RunID != "run" {
					os.Exit(9)
				}
				reply(request, nodewire.PlanDoc{Jobs: []nodewire.JobSpec{{ID: "job", Steps: []nodewire.StepSpec{{ID: "body"}}}}})
			default:
				os.Exit(10)
			}
		case "owner_policy":
			switch request.Op {
			case nodewire.OpPlan:
				reply(request, nodewire.PlanDoc{Jobs: []nodewire.JobSpec{ownerPolicyJob()}})
			case nodewire.OpEval:
				if request.Closure == "slow" {
					held = append(held, request)
					send(nodewire.FrameLog{Level: "info", Message: "slow entered"})
				} else {
					reply(request, nodewire.EvalResult{Value: json.RawMessage("true")})
				}
			case nodewire.OpCancel:
				if len(held) != 1 || request.Target != held[0].ID || request.Job != "" || request.Step != "" {
					os.Exit(13)
				}
				reply(held[0], nodewire.EvalResult{Value: json.RawMessage("null")})
				held = nil
				reply(request, nodewire.ServiceAcknowledged{})
			default:
				os.Exit(7)
			}
		case "owner_expand":
			switch request.Op {
			case nodewire.OpPlan:
				reply(request, nodewire.PlanDoc{Jobs: []nodewire.JobSpec{{ID: "build", Steps: []nodewire.StepSpec{{ID: "body"}}, Closures: nodewire.ClosureSpec{Generate: []string{"first", "empty", "last"}}}}})
			case nodewire.OpEval:
				if request.Job != "" || string(request.Input) != `{"count":2}` {
					os.Exit(6)
				}
				raw, _ := json.Marshal(nodewire.PlanDoc{Jobs: []nodewire.JobSpec{{ID: request.Closure, Groups: []string{request.Closure}, Steps: []nodewire.StepSpec{{ID: "body"}}}}})
				reply(request, nodewire.EvalResult{Value: raw, DeclarationsKnown: true})
			default:
				os.Exit(7)
			}
		case "eval_cancel":
			if request.Op == nodewire.OpEval {
				held = append(held, request)
				send(nodewire.FrameLog{TS: "now", Message: "entered"})
				continue
			}
			if request.Op == nodewire.OpCancel {
				if len(held) != 1 || request.Target != held[0].ID || request.Target == "caller-eval" || request.Job != "" || request.Step != "" {
					os.Exit(13)
				}
				send(nodewire.FrameLog{Message: "cancel_seen"})
			}
			reply(request, map[string]bool{})
			for _, pending := range held {
				reply(pending, nodewire.EvalResult{Value: json.RawMessage("null")})
			}
			held = nil
		case "concurrent":
			held = append(held, request)
			if len(held) < 2 {
				continue
			}
			fmt.Fprintln(os.Stdout, "plain stdout")
			fmt.Fprintln(os.Stderr, "plain stderr")
			send(nodewire.FrameLog{TS: "now", Level: "info", Step: "first", Message: "active"})
			send(nodewire.FrameLog{TS: "now", Level: "info", Step: "unknown", Message: "forged"})
			for i := len(held) - 1; i >= 0; i-- {
				reply(held[i], map[string]string{"step": held[i].Step, "wire_id": held[i].ID})
			}
			send(nodewire.FrameLog{TS: "now", Level: "info", Step: "first", Message: "late"})
		case "cancel":
			if request.Op == nodewire.OpRunStep {
				held = append(held, request)
				send(nodewire.FrameLog{TS: "now", Message: "entered"})
				continue
			}
			if request.Op == nodewire.OpCancel {
				reply(request, map[string]bool{"cancelled": true})
				send(nodewire.FrameLog{TS: "now", Message: "cancel_received"})
				send(nodewire.FrameLog{TS: "now", Message: "body_joined"})
				for _, pending := range held {
					reply(pending, nodewire.RunResult{Outcome: "failed"})
				}
				held = nil
				if cleanup != nil {
					reply(*cleanup, nodewire.EvalResult{Value: json.RawMessage("null")})
					cleanup = nil
				}
				continue
			}
			if request.Op == nodewire.OpEval && len(held) != 0 {
				cleanup = &request
				continue
			}
			reply(request, nodewire.EvalResult{Value: json.RawMessage("null")})
		case "duplicate":
			reply(request, map[string]bool{})
			reply(request, map[string]bool{})
		case "unknown":
			request.ID = "unsolicited"
			reply(request, map[string]bool{})
		case "malformed":
			fmt.Fprintln(frames, "{")
		default:
			reply(request, map[string]bool{})
		}
	}
}

func TestSDKPlanningNativeProcessesMaskStreamsAndPlanEveryTime(t *testing.T) {
	p, input, f, _ := sdkPlanningFixture(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input.Entry.Command = []string{exe, "-test.run=^TestProtocolSessionChild$", "--", "planner"}
	input.Entry.Engine.Identity.Pin.SDK, input.Entry.Engine.Identity.Pin.Version = engineid.GoSDK, "v1.0.0"
	p.EnsureEngine = func(context.Context, engineid.Identity, releaseasset.Target) (engine.Resolved, error) {
		return input.Entry.Engine, nil
	}
	var mu sync.Mutex
	var logs []nodewire.FrameLog
	p.Log = func(_ context.Context, record nodewire.FrameLog) error {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, record)
		return nil
	}
	var sessions []*protocolSession
	cleaned := 0
	cfg := ProtocolSessionConfig{Environments: sdkTestEnvironment(nil), ParseFrame: protocolTestCodec, ValidateRequest: func(nodewire.Request) error { return nil }, RunCleanups: func(context.Context, CleanupScope) error { cleaned++; return nil }}
	p.Start = func(ctx context.Context, input PlanningProcessInput, log func(context.Context, nodewire.FrameLog) error) (ExecutorProcess, error) {
		process, err := cfg.StartPlanning(ctx, input, log)
		if process != nil {
			sessions = append(sessions, process.(*protocolSession))
		}
		return process, err
	}
	for i := range 2 {
		prepared, err := p.Prepare(t.Context(), input, f.input)
		if err != nil {
			t.Fatal(err)
		}
		result, err := planInitial(t.Context(), prepared.Input, prepared.Plan)
		if err != nil {
			t.Fatalf("plan %d = %+v, %v", i, result, err)
		}
	}
	if len(sessions) != 4 || cleaned != 4 {
		t.Fatalf("processes = %d, cleanups = %d", len(sessions), cleaned)
	}
	for _, session := range sessions {
		if session.cmd.ProcessState == nil {
			t.Fatal("planner returned with a live SDK process")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logs) < 6 {
		t.Fatalf("lost describe streams: %+v", logs)
	}
	for _, record := range logs {
		if strings.Contains(record.Message, "private") || strings.Contains(string(record.Attrs), "private") {
			t.Fatalf("unmasked planning log: %+v", record)
		}
	}
}

func TestProtocolSessionFailedStartupRevokesDelegatedRoute(t *testing.T) {
	for _, mode := range []string{"missing_command", "cancelled", "exec_failure", "adapter"} {
		t.Run(mode, func(t *testing.T) {
			revoked := 0
			revokeErr := errors.New("revocation failed")
			cfg := ProtocolSessionConfig{Command: []string{"/missing-sdk-entry"}, Revoke: func() error { revoked++; return revokeErr }}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "missing_command" {
				cfg.Command = nil
			}
			if mode == "cancelled" {
				cancel()
			}
			var err error
			if mode == "adapter" {
				process, startErr := cfg.startNode(ctx, NodeInput{Entry: &source.Entry{Command: cfg.Command}}, ExecutorActivity{})
				err = startErr
				if process != nil {
					t.Fatal("failed startup returned a typed nil process")
				}
			} else {
				session, startErr := startProtocolSession(ctx, cfg)
				err = startErr
				if session != nil {
					_ = session.Close()
					t.Fatal("unexpected process")
				}
			}
			if revoked != 1 || !errors.Is(err, revokeErr) {
				t.Fatalf("revocations = %d, error = %v", revoked, err)
			}
		})
	}
}

func ownerPolicyJob() nodewire.JobSpec {
	return nodewire.JobSpec{ID: "build", Steps: []nodewire.StepSpec{{ID: "body"}}, Closures: nodewire.ClosureSpec{SkipIf: []string{"slow", "skip"}}}
}

func TestExecutorPolicyNativeSkipTimeoutJoinsAndContinues(t *testing.T) {
	for _, mode := range []string{"continue", "revocation", "cleanup"} {
		t.Run(mode, func(t *testing.T) { executorPolicyNativeSkip(t, mode) })
	}
}

func executorPolicyNativeSkip(t *testing.T, mode string) {
	t.Helper()
	in, st, deps, _ := executorPolicyFixture(t, func(job *nodewire.JobSpec) { *job = ownerPolicyJob() })
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	in.Entry.Command = []string{exe, "-test.run=^TestProtocolSessionChild$", "--", "owner_policy"}
	deadlines := make(chan context.CancelCauseFunc, 2)
	deps.ChildContext = func(ctx context.Context, _, _ time.Duration) (context.Context, context.CancelFunc, error) {
		bounded, cancel := context.WithCancelCause(ctx)
		deadlines <- cancel
		return bounded, func() { cancel(context.Canceled) }, nil
	}
	var revoked, cleaned int
	cfg := ProtocolSessionConfig{
		Environments: sdkTestEnvironment(st), ParseFrame: protocolTestCodec,
		ValidateRequest: func(nodewire.Request) error { return nil },
		Log: func(_ context.Context, record nodewire.FrameLog) error {
			if record.Message == "slow entered" {
				(<-deadlines)(context.DeadlineExceeded)
			}
			return nil
		},
		Revoke: func() error {
			revoked++
			if mode == "revocation" {
				return errors.New("private revoke error")
			}
			return nil
		},
		RunCleanups: func(context.Context, CleanupScope) error {
			cleaned++
			if mode == "cleanup" {
				return errors.New("private cleanup error")
			}
			return nil
		},
	}
	deps.Start = cfg.startNode
	host, spec, job := executorPolicyHost(t, deps, in)
	result, err := host.skipIf(t.Context(), in, spec, job)
	if mode != "continue" {
		if err == nil || result.Skip || len(result.TimedOut) != 0 || revoked != 1 || cleaned != 1 || strings.Contains(err.Error(), "private") {
			t.Fatalf("skip = %+v, revocations = %d, cleanups = %d, error = %v", result, revoked, cleaned, err)
		}
		return
	}
	if err != nil || !result.Skip || !slices.Equal(result.TimedOut, []int{0}) || revoked != 2 || cleaned != 2 {
		t.Fatalf("skip = %+v, revocations = %d, cleanups = %d, error = %v", result, revoked, cleaned, err)
	}
}

func TestOwnerEvaluationNativeProcess(t *testing.T) {
	in, st, deps, _ := evaluationFixture(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	in.Entry.Command = []string{exe, "-test.run=^TestProtocolSessionChild$", "--", "owner_expand"}
	in.RouteBinding.Routes = nodewire.ProcessRoutes{URL: "http://host", Bearer: "private-test-bearer"}
	revoked, cleaned := false, false
	deps.Bind = func(_ context.Context, in EvaluationInput) (ProcessGrant, error) {
		binding := in.RouteBinding
		binding.Routes.Bearer = "private-test-bearer-child"
		return ProcessGrant{Binding: binding, Revoke: func() error { revoked = true; return nil }}, nil
	}
	deps.Masks = NewMaskRegistry(deps.Masker)
	cfg := ProtocolSessionConfig{Masks: deps.Masks, Log: func(context.Context, nodewire.FrameLog) error { return nil }, Environments: sdkTestEnvironment(st), ParseFrame: protocolTestCodec, ValidateRequest: func(nodewire.Request) error { return nil }, RunCleanups: func(context.Context, CleanupScope) error {
		if !revoked {
			return errors.New("routes still live during cleanup")
		}
		cleaned = true
		return nil
	}}
	deps.Start = cfg.StartEvaluation
	h, err := newOwnerEvaluator(deps)
	if err != nil {
		t.Fatal(err)
	}
	body, _, failure, err := h.evaluateExpansion(t.Context(), in)
	if err != nil || failure != nil {
		t.Fatalf("failure = %+v, error = %v", failure, err)
	}
	var plan nodewire.PlanDoc
	if err := json.Unmarshal(body, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Jobs) != 3 || !revoked || !cleaned {
		t.Fatalf("plan = %+v, revoked = %v, cleaned = %v", plan, revoked, cleaned)
	}
}

func TestProtocolProcessAdaptersHonorLogPolicy(t *testing.T) {
	for _, adapter := range []string{"direct", "attempt", "evaluation", "planning"} {
		for _, debug := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/debug=%t", adapter, debug), func(t *testing.T) {
				input, state, _, _ := executorFixture(t)
				var spec runspec.Spec
				if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
					t.Fatal(err)
				}
				spec.Resolved.Settings.Options.Debug.Value = debug
				var err error
				input.Accepted.Spec, err = json.Marshal(spec)
				if err != nil {
					t.Fatal(err)
				}
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				command := []string{exe, "-test.run=^TestProtocolSessionChild$", "--", "log_policy"}
				input.Entry.Command = command
				var mu sync.Mutex
				var levels []string
				log := func(_ context.Context, record nodewire.FrameLog) error {
					mu.Lock()
					defer mu.Unlock()
					if strings.HasSuffix(record.Message, " detail") {
						levels = append(levels, record.Level)
					}
					return nil
				}
				cfg := ProtocolSessionConfig{Command: command, Environment: testProcessEnv(t, "GORACE=atexit_sleep_ms=0"), Environments: sdkTestEnvironment(state), ParseFrame: protocolTestCodec, ValidateRequest: func(nodewire.Request) error { return nil }, Log: log, Debug: !debug, Revoke: func() error { return nil }}
				var process ExecutorProcess
				switch adapter {
				case "direct":
					cfg.Debug = debug
					process, err = startProtocolSession(t.Context(), cfg)
				case "attempt":
					process, err = cfg.startNode(t.Context(), input, ExecutorActivity{})
				case "evaluation":
					process, err = cfg.StartEvaluation(t.Context(), EvaluationInput{Entry: input.Entry, Accepted: input.Accepted, NodeID: input.NodeID, Generation: int64(input.Generation), SpecHash: input.SpecHash, GrantedRunner: input.GrantedRunner})
				case "planning":
					process, err = cfg.StartPlanning(t.Context(), PlanningProcessInput{Entry: input.Entry, Run: nodewire.RunContext{RunID: "run", Pipeline: "ci"}, Debug: debug}, log)
				}
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = process.Close() })
				session := process.(*protocolSession)
				protocolTestHello(t, session)
				if _, err := session.Request(t.Context(), nodewire.Request{Op: nodewire.OpDescribe}); err != nil {
					t.Fatal(err)
				}
				if err := session.Close(); err != nil {
					t.Fatal(err)
				}
				mu.Lock()
				defer mu.Unlock()
				want := []string{"info"}
				if debug {
					want = []string{"debug", "info"}
				}
				if !slices.Equal(levels, want) {
					t.Fatalf("levels=%v want=%v", levels, want)
				}
			})
		}
	}
}

func TestProtocolSessionIdleSinkFailureStopsAuthor(t *testing.T) {
	for _, registered := range []bool{false, true} {
		t.Run(fmt.Sprint(registered), func(t *testing.T) {
			failure := errors.New("log sink failed")
			session := protocolTestSession(t, "partial_output", func(cfg *ProtocolSessionConfig) {
				if registered {
					cfg.Masks = NewMaskRegistry(secrets.NewMasker())
				}
				cfg.Log = func(context.Context, nodewire.FrameLog) error { return failure }
			})
			protocolTestHello(t, session)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			_, err := session.Request(ctx, nodewire.Request{Op: nodewire.OpRunStep, Job: "job", Step: "body", Attempt: 1})
			if !errors.Is(err, failure) {
				t.Fatalf("idle flush did not fail the live request: %v", err)
			}
			if err := session.Close(); !errors.Is(err, failure) || session.cmd.ProcessState == nil {
				t.Fatalf("idle sink failure did not join the author: %v", err)
			}
		})
	}
}

func TestProtocolSessionObservesExitBeforeOutputAcknowledgement(t *testing.T) {
	var exited time.Time
	finishing, acknowledge := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-acknowledge:
		default:
			close(acknowledge)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	session := protocolTestSession(t, "normal", func(cfg *ProtocolSessionConfig) {
		cfg.exitedAt = &exited
		cfg.output = &planningLogs{masker: secrets.NewMasker(), buffer: []planningOutput{{text: "pending output"}}, sink: func(context.Context, nodewire.FrameLog) error {
			close(finishing)
			<-acknowledge
			return nil
		}}
	})
	protocolTestHello(t, session)
	closed := make(chan error, 1)
	go func() { closed <- session.Close() }()
	select {
	case <-finishing:
	case <-ctx.Done():
		t.Fatal("output finalization did not start")
	}
	blockedAt := time.Now().UTC()
	select {
	case err := <-closed:
		close(acknowledge)
		t.Fatalf("Close returned before output acknowledgement: %v", err)
	default:
	}
	observed := exited
	close(acknowledge)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if observed.IsZero() || observed.After(blockedAt) || !exited.Equal(observed) {
		t.Fatalf("exit observation = %v, after acknowledgement = %v, output blocked at %v", observed, exited, blockedAt)
	}
}
