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

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func protocolTestCodec(line []byte) (nodeproto.Frame, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return nodeproto.Frame{}, err
	}
	if _, ok := fields["reply"]; ok {
		var reply nodeproto.Reply
		if err := json.Unmarshal(line, &reply); err != nil {
			return nodeproto.Frame{}, err
		}
		return nodeproto.Frame{Reply: &reply}, nil
	}
	var log nodeproto.FrameLog
	if err := json.Unmarshal(line, &log); err != nil {
		return nodeproto.Frame{}, err
	}
	return nodeproto.Frame{Log: &log}, nil
}

func protocolTestSession(t *testing.T, mode string, edit func(*ProtocolSessionConfig)) *ProtocolSession {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := ProtocolSessionConfig{Command: []string{exe, "-test.run=^TestProtocolSessionChild$", "--", mode}, Environment: []string{"GORACE=atexit_sleep_ms=0"}, ParseFrame: protocolTestCodec, ValidateRequest: func(nodeproto.Request) error { return nil }, Revoke: func() error { return nil }}
	if edit != nil {
		edit(&cfg)
	}
	session, err := StartProtocolSession(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func protocolTestHello(t *testing.T, session *ProtocolSession) {
	t.Helper()
	reply, err := session.Request(t.Context(), nodeproto.Request{ID: "caller-hello", Op: nodeproto.OpHello, Protocol: nodeproto.Version, Engine: &nodeproto.EngineIdentity{Version: "test"}, Runner: &nodeproto.RunnerIdentity{Name: "granted", Type: "local"}, Binding: &nodeproto.ProcessBinding{RunID: "r", JobID: "job", Routes: nodeproto.ProcessRoutes{URL: "http://host", Bearer: "private-test-bearer"}}})
	if err != nil || !reply.OK || reply.ID != "caller-hello" {
		t.Fatalf("hello = %#v, %v", reply, err)
	}
}

func TestProtocolSessionConcurrentRepliesAndAttribution(t *testing.T) {
	var mu sync.Mutex
	var logs []nodeproto.FrameLog
	var progress atomic.Int64
	session := protocolTestSession(t, "concurrent", func(cfg *ProtocolSessionConfig) {
		cfg.Activity.Progress = func() { progress.Add(1) }
		cfg.Log = func(_ context.Context, log nodeproto.FrameLog) error {
			mu.Lock()
			defer mu.Unlock()
			logs = append(logs, log)
			return nil
		}
	})
	protocolTestHello(t, session)
	replies := make(chan nodeproto.Reply, 2)
	errs := make(chan error, 2)
	for _, step := range []string{"first", "second"} {
		go func() {
			reply, err := session.Request(t.Context(), nodeproto.Request{ID: step, Op: nodeproto.OpRunStep, Job: "job", Step: step, Attempt: 1})
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
		if !slices.ContainsFunc(logs, func(log nodeproto.FrameLog) bool {
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
		cfg.Log = func(_ context.Context, log nodeproto.FrameLog) error {
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
		_, err := session.Request(ctx, nodeproto.Request{ID: "step", Op: nodeproto.OpRunStep, Job: "job", Step: "body", Attempt: 1})
		result <- err
	}()
	<-entered
	if _, err := session.Request(t.Context(), nodeproto.Request{Op: nodeproto.OpRunStep, Job: "job", Step: "body", Attempt: 1}); err == nil {
		t.Fatal("accepted duplicate active step")
	}
	if _, err := session.Request(t.Context(), nodeproto.Request{Op: nodeproto.OpPlan}); err == nil {
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
	reply, err := session.Request(t.Context(), nodeproto.Request{ID: "cleanup", Op: nodeproto.OpEval, Job: "job", Closure: "finally"})
	if err != nil || !reply.OK {
		t.Fatalf("cleanup = %#v, %v", reply, err)
	}
	if _, err := session.Request(t.Context(), nodeproto.Request{Op: nodeproto.OpPlan}); err != nil {
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
			_, _ = session.Request(t.Context(), nodeproto.Request{ID: "describe", Op: nodeproto.OpDescribe})
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
		cfg.Log = func(_ context.Context, log nodeproto.FrameLog) error {
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
		_, err := session.Request(ctx, nodeproto.Request{ID: "caller-eval", Op: nodeproto.OpEval, Closure: "before_run"})
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
	if _, err := session.Request(t.Context(), nodeproto.Request{Op: nodeproto.OpDescribe}); err != nil {
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
	if _, err := session.Request(t.Context(), nodeproto.Request{Op: nodeproto.OpDescribe}); err == nil {
		t.Fatal("accepted request before hello")
	}
	protocolTestHello(t, session)
	if _, err := session.Request(t.Context(), nodeproto.Request{Op: nodeproto.OpHello}); err == nil {
		t.Fatal("accepted second hello")
	}
	if _, err := session.Request(t.Context(), nodeproto.Request{Op: nodeproto.OpPlan}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Request(t.Context(), nodeproto.Request{Op: nodeproto.OpPlan}); err == nil {
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
			_, err := session.Request(t.Context(), nodeproto.Request{Op: nodeproto.OpHello})
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
	if _, err := session.Request(t.Context(), nodeproto.Request{Op: nodeproto.OpDescribe}); err == nil {
		t.Fatal("request after Close succeeded")
	}
}

func TestProtocolSessionValidationStopsUnsentRequests(t *testing.T) {
	sentinel := errors.New("contract rejected request")
	session := protocolTestSession(t, "normal", func(cfg *ProtocolSessionConfig) {
		cfg.ValidateRequest = func(nodeproto.Request) error { return sentinel }
	})
	if _, err := session.Request(t.Context(), nodeproto.Request{Op: nodeproto.OpHello}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProtocolSessionBoundHelloRequiresRevocation(t *testing.T) {
	session := protocolTestSession(t, "normal", func(cfg *ProtocolSessionConfig) { cfg.Revoke = nil })
	_, err := session.Request(t.Context(), nodeproto.Request{Op: nodeproto.OpHello, Binding: &nodeproto.ProcessBinding{Routes: nodeproto.ProcessRoutes{Bearer: "bound"}}})
	if err == nil || !strings.Contains(err.Error(), "revocation") {
		t.Fatalf("bound hello without revocation = %v", err)
	}
	if _, err := session.Request(t.Context(), nodeproto.Request{Op: nodeproto.OpHello}); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProtocolSessionCancelledWriteReapsBlockedProcess(t *testing.T) {
	validated := make(chan struct{})
	session := protocolTestSession(t, "blocked", func(cfg *ProtocolSessionConfig) {
		cfg.ValidateRequest = func(request nodeproto.Request) error {
			if request.Op == nodeproto.OpEval {
				close(validated)
			}
			return nil
		}
	})
	protocolTestHello(t, session)
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := session.Request(ctx, nodeproto.Request{Op: nodeproto.OpEval, Job: "job", Closure: "blocked", Input: json.RawMessage(`"` + strings.Repeat("x", 1<<20) + `"`)})
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
	reply := func(request nodeproto.Request, result any) {
		data, _ := json.Marshal(result)
		send(nodeproto.Reply{ID: request.ID, OK: true, Result: data})
	}
	var held []nodeproto.Request
	var cleanup *nodeproto.Request
	scanner := bufio.NewScanner(requests)
	for scanner.Scan() {
		var request nodeproto.Request
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(3)
		}
		if request.Op == nodeproto.OpHello {
			for _, value := range append(os.Environ(), os.Args...) {
				if strings.Contains(value, "private-test-bearer") {
					os.Exit(4)
				}
			}
			if mode == "old" {
				send(nodeproto.Reply{ID: request.ID, Error: &apierr.Error{Code: apierr.UnknownOp, Message: "unknown op"}})
				continue
			}
			version := nodeproto.Version
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
			reply(request, nodeproto.HelloResult{Protocol: version, SDK: nodeproto.SDKIdentity{Language: "go", Version: sdkVersion}, Features: []string{}})
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
		case "startup_stable", "startup_change":
			switch request.Op {
			case nodeproto.OpPlan:
				job := sdkStartupJob(strings.TrimPrefix(mode, "startup_"))
				if os.Getenv("NODE_OPTIONS") == "change" {
					job.Steps[0].ID = "changed"
				}
				reply(request, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}})
			case nodeproto.OpRunStep:
				send(nodeproto.FrameLog{Message: "body entered"})
				reply(request, nodeproto.RunResult{Outcome: "success"})
			default:
				os.Exit(12)
			}
		case "environment":
			reply(request, os.Environ())
		case "claimed_node", "claimed_node_hold":
			switch request.Op {
			case nodeproto.OpPlan:
				reply(request, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}}}})
			case nodeproto.OpRunStep:
				if mode == "claimed_node_hold" {
					held = append(held, request)
					send(nodeproto.FrameLog{Level: "info", Message: "entered"})
				} else {
					reply(request, nodeproto.RunResult{Outcome: "success"})
				}
			case nodeproto.OpCancel:
				reply(request, map[string]bool{"cancelled": true})
				for _, pending := range held {
					reply(pending, nodeproto.RunResult{Outcome: "cancelled"})
				}
				held = nil
			default:
				os.Exit(11)
			}
		case "planner":
			switch request.Op {
			case nodeproto.OpDescribe:
				secret := "private-plan-default"
				send(nodeproto.FrameLog{Level: "info", Message: secret, Attrs: json.RawMessage(`{"escaped":"private-plan-def\u0061ult"}`)})
				fmt.Fprintln(os.Stdout, secret)
				fmt.Fprintln(os.Stderr, secret)
				reply(request, nodeproto.DescribeDoc{Pipelines: []nodeproto.PipelineDescription{{Name: "build", Args: []nodeproto.ArgumentDescription{{Name: "token", Secret: true, Default: &secret}}}}})
			case nodeproto.OpPlan:
				if request.Args["token"] != "private-plan-default" || request.Run.RunID != "run" {
					os.Exit(9)
				}
				reply(request, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job"}}})
			default:
				os.Exit(10)
			}
		case "owner_policy":
			switch request.Op {
			case nodeproto.OpPlan:
				reply(request, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{ownerPolicyJob()}})
			case nodeproto.OpEval:
				if request.Closure == "slow" {
					held = append(held, request)
					send(nodeproto.FrameLog{Level: "info", Message: "slow entered"})
				} else {
					reply(request, nodeproto.EvalResult{Value: json.RawMessage("true")})
				}
			case nodeproto.OpCancel:
				if len(held) != 1 || request.Target != held[0].ID || request.Job != "" || request.Step != "" {
					os.Exit(13)
				}
				reply(held[0], nodeproto.EvalResult{Value: json.RawMessage("null")})
				held = nil
				reply(request, nodeproto.ServiceAcknowledged{})
			default:
				os.Exit(7)
			}
		case "owner_expand":
			switch request.Op {
			case nodeproto.OpPlan:
				reply(request, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}, Closures: nodeproto.ClosureSpec{Generate: []string{"first", "empty", "last"}}}}})
			case nodeproto.OpEval:
				if request.Job != "build" || string(request.Input) != `{"count":2}` {
					os.Exit(6)
				}
				raw, _ := json.Marshal([]nodeproto.JobSpec{{ID: request.Closure, Groups: []string{request.Closure}}})
				reply(request, nodeproto.EvalResult{Value: raw})
			default:
				os.Exit(7)
			}
		case "eval_cancel":
			if request.Op == nodeproto.OpEval {
				held = append(held, request)
				send(nodeproto.FrameLog{TS: "now", Message: "entered"})
				continue
			}
			if request.Op == nodeproto.OpCancel {
				if len(held) != 1 || request.Target != held[0].ID || request.Target == "caller-eval" || request.Job != "" || request.Step != "" {
					os.Exit(13)
				}
				send(nodeproto.FrameLog{Message: "cancel_seen"})
			}
			reply(request, map[string]bool{})
			for _, pending := range held {
				reply(pending, nodeproto.EvalResult{Value: json.RawMessage("null")})
			}
			held = nil
		case "concurrent":
			held = append(held, request)
			if len(held) < 2 {
				continue
			}
			fmt.Fprintln(os.Stdout, "plain stdout")
			fmt.Fprintln(os.Stderr, "plain stderr")
			send(nodeproto.FrameLog{TS: "now", Level: "info", Step: "first", Message: "active"})
			send(nodeproto.FrameLog{TS: "now", Level: "info", Step: "unknown", Message: "forged"})
			for i := len(held) - 1; i >= 0; i-- {
				reply(held[i], map[string]string{"step": held[i].Step, "wire_id": held[i].ID})
			}
			send(nodeproto.FrameLog{TS: "now", Level: "info", Step: "first", Message: "late"})
		case "cancel":
			if request.Op == nodeproto.OpRunStep {
				held = append(held, request)
				send(nodeproto.FrameLog{TS: "now", Message: "entered"})
				continue
			}
			if request.Op == nodeproto.OpCancel {
				reply(request, map[string]bool{"cancelled": true})
				send(nodeproto.FrameLog{TS: "now", Message: "cancel_received"})
				send(nodeproto.FrameLog{TS: "now", Message: "body_joined"})
				for _, pending := range held {
					reply(pending, nodeproto.RunResult{Outcome: "failed"})
				}
				held = nil
				if cleanup != nil {
					reply(*cleanup, nodeproto.EvalResult{Value: json.RawMessage("null")})
					cleanup = nil
				}
				continue
			}
			if request.Op == nodeproto.OpEval && len(held) != 0 {
				cleanup = &request
				continue
			}
			reply(request, nodeproto.EvalResult{Value: json.RawMessage("null")})
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

func TestSDKPlanningNativeProcessesMaskStreamsAndReusePlanCache(t *testing.T) {
	p, input, f, _ := sdkPlanningFixture(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input.Entry.Command = []string{exe, "-test.run=^TestProtocolSessionChild$", "--", "planner"}
	input.Entry.Engine.Identity.Pin.SDK, input.Entry.Engine.Identity.Pin.Version = engine.GoSDK, "v1.0.0"
	p.EnsureEngine = func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
		return input.Entry.Engine, nil
	}
	var mu sync.Mutex
	var logs []nodeproto.FrameLog
	p.Log = func(_ context.Context, record nodeproto.FrameLog) error {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, record)
		return nil
	}
	var sessions []*ProtocolSession
	cleaned := 0
	cfg := ProtocolSessionConfig{Environments: sdkTestEnvironment(nil), ParseFrame: protocolTestCodec, ValidateRequest: func(nodeproto.Request) error { return nil }, RunCleanups: func(context.Context, CleanupScope) error { cleaned++; return nil }}
	p.Start = func(ctx context.Context, input PlanningProcessInput, log func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
		process, err := cfg.StartPlanning(ctx, input, log)
		if process != nil {
			sessions = append(sessions, process.(*ProtocolSession))
		}
		return process, err
	}
	for i := range 2 {
		prepared, err := p.Prepare(t.Context(), input, f.input)
		if err != nil {
			t.Fatal(err)
		}
		result, err := PlanInitial(t.Context(), prepared.Input, prepared.Functions)
		if err != nil || result.CacheHit != (i == 1) {
			t.Fatalf("plan %d = %+v, %v", i, result, err)
		}
	}
	if len(sessions) != 3 || cleaned != 3 {
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
				process, startErr := cfg.Start(ctx, NodeInput{Entry: &source.Entry{Command: cfg.Command}}, ExecutorActivity{})
				err = startErr
				if process != nil {
					t.Fatal("failed startup returned a typed nil process")
				}
			} else {
				session, startErr := StartProtocolSession(ctx, cfg)
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

func ownerPolicyJob() nodeproto.JobSpec {
	return nodeproto.JobSpec{ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}, Closures: nodeproto.ClosureSpec{SkipIf: []string{"slow", "skip"}}}
}

func TestOwnerPolicyNativeSkipTimeoutJoinsAndContinues(t *testing.T) {
	for _, mode := range []string{"continue", "revocation", "cleanup"} {
		t.Run(mode, func(t *testing.T) { ownerPolicyNativeSkip(t, mode) })
	}
}

func ownerPolicyNativeSkip(t *testing.T, mode string) {
	t.Helper()
	in, st, deps, _ := policyFixture(t, func(job *nodeproto.JobSpec) { *job = ownerPolicyJob() })
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
		ValidateRequest: func(nodeproto.Request) error { return nil },
		Log: func(_ context.Context, record nodeproto.FrameLog) error {
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
	deps.Start = cfg.StartEvaluation
	host, err := NewOwnerEvaluator(deps)
	if err != nil {
		t.Fatal(err)
	}
	result, err := host.SkipIf(t.Context(), in)
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
	in.RouteBinding.Routes = nodeproto.ProcessRoutes{URL: "http://host", Bearer: "private-test-bearer"}
	deps.Bind = func(_ context.Context, in EvaluationInput) (nodeproto.ProcessBinding, error) {
		binding := in.RouteBinding
		binding.Routes.Bearer = "private-test-bearer-child"
		return binding, nil
	}
	revoked, cleaned := false, false
	cfg := ProtocolSessionConfig{Environments: sdkTestEnvironment(st), ParseFrame: protocolTestCodec, ValidateRequest: func(nodeproto.Request) error { return nil }, Revoke: func() error { revoked = true; return nil }, RunCleanups: func(context.Context, CleanupScope) error {
		if !revoked {
			return errors.New("routes still live during cleanup")
		}
		cleaned = true
		return nil
	}}
	deps.Start = cfg.StartEvaluation
	h, err := NewOwnerEvaluator(deps)
	if err != nil {
		t.Fatal(err)
	}
	body, failure, err := h.Expand(t.Context(), in)
	if err != nil || failure != nil {
		t.Fatalf("failure = %+v, error = %v", failure, err)
	}
	var plan nodeproto.PlanDoc
	if err := json.Unmarshal(body, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Jobs) != 3 || !revoked || !cleaned {
		t.Fatalf("plan = %+v, revoked = %v, cleaned = %v", plan, revoked, cleaned)
	}
}
