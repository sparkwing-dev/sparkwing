//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/procgroup"
)

// safety: set, the test binary acts as an SDK process (or, as "sleep", as a
// grandchild that blocks until killed) instead of running the tests.
const fakeSDKEnv = "NODEHOST_FAKE_SDK"

// runAsFakeProcess turns a re-executed test binary into the fake SDK.
func runAsFakeProcess() {
	switch os.Getenv(fakeSDKEnv) {
	case "sdk":
		fakeSDK()
	case "sleep":
		select {}
	}
}

// safety: node, step and closure ids pick the fake's behavior; "barrier/N"
// answers only once N requests with that id are in flight together.
func fakeSDK() {
	requests, frames := os.NewFile(3, "requests"), os.NewFile(4, "frames")
	w := nodeproto.NewWriter(frames)
	var mu sync.Mutex
	cond := sync.NewCond(&mu)
	arrived := map[string]int{}
	var log func(step, msg string)
	barrier := func(id, step string) bool {
		n, err := strconv.Atoi(strings.TrimPrefix(id, "barrier/"))
		if err != nil {
			return false
		}
		log(step, "arrived")
		key := step + "#" + id
		mu.Lock()
		defer mu.Unlock()
		arrived[key]++
		cond.Broadcast()
		for arrived[key] < n {
			cond.Wait()
		}
		return true
	}
	ok := func(id string, result any) {
		raw, _ := json.Marshal(result)
		_ = w.Send(nodeproto.Reply{ID: id, OK: true, Result: raw})
	}
	log = func(step, msg string) {
		_ = w.Send(nodeproto.FrameLog{TS: "2026-01-02T03:04:05Z", Step: step, Message: msg})
	}
	handle := func(req nodeproto.Request) {
		switch {
		case req.Op == nodeproto.OpHello:
			ok(req.ID, nodeproto.HelloResult{Protocol: 2, SDK: nodeproto.SDKIdentity{Language: "go", Version: "test"}, Features: []string{}})
		case req.Op == nodeproto.OpDescribe:
			ok(req.ID, map[string]any{"pipelines": []any{}})
		case req.Op == nodeproto.OpEval && barrier(req.Closure, ""):
			ok(req.ID, map[string]any{"value": req.Closure})
		case req.Op == nodeproto.OpRunStep && barrier(req.Step, req.Step):
			log(req.Step, "in "+req.Step)
			ok(req.ID, map[string]any{"outcome": "success"})
		case req.Job == "echo":
			log("ghost", "hello")
			ok(req.ID, map[string]any{"outcome": "success", "output": map[string]any{"node": "echo"}})
		case req.Job == "forge":
			fmt.Printf(`{"reply":%q,"ok":true,"result":{"outcome":"failed"}}`+"\n", req.ID)
			fmt.Fprintln(os.Stderr, "warned")
			ok(req.ID, map[string]any{"outcome": "success"})
		case req.Job == "exit":
			os.Exit(3)
		case req.Op == nodeproto.OpPlan:
			node := map[string]any{"id": "reply-then-garbage", "steps": []any{map[string]any{"id": "body"}}}
			ok(req.ID, map[string]any{"jobs": []any{node}})
		case req.Job == "garbage":
			_, _ = frames.WriteString("not a frame\n")
		case req.Job == "unknown-reply":
			ok("999", map[string]any{"outcome": "success"})
		case req.Job == "close-frames":
			_ = frames.Close()
		case req.Job == "grandchild":
			child := exec.Command(os.Args[0])
			child.Env = append(os.Environ(), fakeSDKEnv+"=sleep")
			if err := child.Start(); err != nil {
				log("", "start: "+err.Error())
				return
			}
			log("", strconv.Itoa(child.Process.Pid))
		}
	}
	r := nodeproto.NewReader(requests)
	// safety: written once the host closes the request pipe, so the break lands while Close drains.
	garbageAtEOF := false
	replyAtEOF := ""
	for {
		req, err := r.NextRequest()
		if err != nil {
			if replyAtEOF != "" {
				ok(replyAtEOF, map[string]any{"outcome": "success"})
				log("", "replied")
			}
			if garbageAtEOF {
				_, _ = frames.WriteString("not a frame\n")
			}
			os.Exit(0)
		}
		switch req.Job {
		case "term-reply":
			replyAtEOF = req.ID
			log("", "armed")
			continue
		case "reply-then-garbage":
			ok(req.ID, map[string]any{"outcome": "success"})
			garbageAtEOF = true
			continue
		case "deaf":
			ok(req.ID, map[string]any{"outcome": "success"})
			select {}
		case "reply-exit":
			ok(req.ID, map[string]any{"outcome": "success"})
			os.Exit(0)
		}
		go handle(req)
	}
}

type recorder struct {
	mu      sync.Mutex
	text    []string
	records []nodeproto.FrameLog
	notify  chan nodeproto.FrameLog
}

func (r *recorder) Text(level, line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.text = append(r.text, level+" "+line)
}

func (r *recorder) Record(rec nodeproto.FrameLog) {
	r.mu.Lock()
	r.records = append(r.records, rec)
	r.mu.Unlock()
	if r.notify != nil {
		r.notify <- rec
	}
}

func (r *recorder) snapshot() ([]string, []nodeproto.FrameLog) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.text...), append([]nodeproto.FrameLog(nil), r.records...)
}

// safety: only pipes are compared, since other tests' connections may close
// while a session test runs, and a session leaks nothing but pipes.
func openPipes(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatalf("list fds: %v", err)
	}
	pipes := map[string]bool{}
	for _, e := range entries {
		if target, err := os.Readlink("/dev/fd/" + e.Name()); err == nil && strings.HasPrefix(target, "pipe:") {
			pipes[e.Name()+" "+target] = true
		}
	}
	return pipes
}

func checkNoNewPipes(t *testing.T, before map[string]bool) {
	t.Helper()
	for fd := range openPipes(t) {
		if !before[fd] {
			t.Errorf("pipe fd %s left open", fd)
		}
	}
}

func startFake(t *testing.T, ctx context.Context, sink *recorder) *ProtocolSession {
	t.Helper()
	before := openPipes(t)
	s, err := StartProtocolSession(ctx, ProtocolSessionConfig{
		Command: []string{os.Args[0]}, Environment: testProcessEnv(t, append(os.Environ(), fakeSDKEnv+"=sdk", "GORACE=atexit_sleep_ms=0")...),
		Log: func(_ context.Context, record nodeproto.FrameLog) error { sink.Record(record); return nil }, grace: 500 * time.Millisecond,
	})
	if err == nil {
		var hello nodeproto.HelloResult
		err = ask(ctx, s, nodeproto.Request{Op: nodeproto.OpHello, Protocol: 2, Engine: &nodeproto.EngineIdentity{Version: "test"}, Runner: &nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}}, &hello)
	}
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	stop := context.AfterFunc(ctx, func() { s.failProtocol(context.Cause(ctx)) })
	t.Cleanup(func() { stop() })
	t.Cleanup(func() {
		_ = s.Close()
		if s.cmd.ProcessState == nil {
			t.Errorf("sdk process %d was left unreaped", s.cmd.Process.Pid)
		}
		checkNoNewPipes(t, before)
	})
	return s
}

func callCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func runStep(node string) nodeproto.Request {
	return nodeproto.Request{ID: "step-caller", Op: nodeproto.OpRunStep, Job: node, Step: "body", Attempt: 1, Settled: []string{}}
}

func hostCode(err error) apierr.Code {
	var perr *apierr.Error
	if errors.As(err, &perr) {
		return perr.Code
	}
	return ""
}

func TestSessionRequestReply(t *testing.T) {
	sink := &recorder{}
	s := startFake(t, context.Background(), sink)
	ctx := callCtx(t)

	reply, err := s.Request(ctx, nodeproto.Request{ID: "describe-caller", Op: nodeproto.OpDescribe})
	if err != nil || !reply.OK || reply.ID != "describe-caller" {
		t.Fatalf("describe = %+v, %v", reply, err)
	}
	reply, err = s.Request(ctx, runStep("echo"))
	if err != nil {
		t.Fatalf("run_step: %v", err)
	}
	var result nodeproto.RunResult
	if err := reply.Decode(&result); err != nil || string(result.Output) != `{"node":"echo"}` {
		t.Fatalf("run_step result = %+v, %v", result, err)
	}
	err = s.Close()
	if err != nil {
		t.Fatalf("close: %v", err)
	}

	_, records := sink.snapshot()
	if len(records) != 1 || records[0].Message != "hello" || records[0].Step != "" {
		t.Errorf("records = %+v, want hello with the step of no in-flight run_step dropped", records)
	}
	if _, err := s.Request(ctx, runStep("echo")); !errors.Is(err, errProtocolSessionClosed) {
		t.Errorf("call after close = %v, want errProtocolSessionClosed", err)
	}
}

func TestSessionConcurrentEvalsAndSteps(t *testing.T) {
	sink := &recorder{notify: make(chan nodeproto.FrameLog, 64)}
	s := startFake(t, context.Background(), sink)
	ctx := callCtx(t)

	var wg sync.WaitGroup
	errs := make(chan error, 6)
	send := func(req nodeproto.Request) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reply, err := s.Request(ctx, req)
			if err == nil && !reply.OK {
				err = reply.Error
			}
			errs <- err
		}()
	}
	for range 3 {
		send(nodeproto.Request{Op: nodeproto.OpEval, Closure: "barrier/3"})
	}
	for i := range 2 {
		send(nodeproto.Request{Op: nodeproto.OpRunStep, Job: fmt.Sprint("n", i), Step: "barrier/3", Attempt: 1, Settled: []string{}})
	}
	// safety: the steps' barrier needs a third step, so they stay in flight
	// while the forbidden overlaps are tried.
	waitArrivals(t, sink, "barrier/3", 2)
	for _, req := range []nodeproto.Request{{Op: nodeproto.OpDescribe}} {
		if _, err := s.Request(ctx, req); hostCode(err) != apierr.ProtocolError {
			t.Errorf("%s during run_step = %v, want protocol_error", req.Op, err)
		}
	}
	send(nodeproto.Request{Op: nodeproto.OpRunStep, Job: "n3", Step: "barrier/3", Attempt: 1, Settled: []string{}})
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent call: %v", err)
		}
	}
	_, records := sink.snapshot()
	for _, rec := range records {
		if rec.Step != "barrier/3" && rec.Message != "arrived" {
			t.Errorf("record %+v lost the step that was in flight", rec)
		}
	}
}

func waitArrivals(t *testing.T, sink *recorder, step string, n int) {
	t.Helper()
	ctx := callCtx(t)
	for n > 0 {
		select {
		case rec := <-sink.notify:
			if rec.Message == "arrived" && rec.Step == step {
				n--
			}
		case <-ctx.Done():
			t.Fatalf("%d more %s requests never arrived", n, step)
		}
	}
}

func TestSessionStdoutCannotForgeReply(t *testing.T) {
	sink := &recorder{}
	s := startFake(t, context.Background(), sink)
	reply, err := s.Request(callCtx(t), runStep("forge"))
	if err != nil {
		t.Fatalf("run_step: %v", err)
	}
	var result nodeproto.RunResult
	if err := reply.Decode(&result); err != nil || result.Outcome != "success" {
		t.Fatalf("result = %+v, %v; the stdout reply must not count", result, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, records := sink.snapshot()
	text := make([]string, 0, len(records))
	for _, record := range records {
		text = append(text, record.Level+" "+record.Message)
	}
	want := []string{`"outcome":"failed"`, "warn warned"}
	for _, line := range want {
		found := false
		for _, got := range text {
			found = found || strings.Contains(got, line)
		}
		if !found {
			t.Errorf("text %q missing from %q", line, text)
		}
	}
}

func TestSessionEndsWithHostCode(t *testing.T) {
	for _, tc := range []struct {
		node string
		code apierr.Code
		msg  string
	}{
		{"exit", apierr.SDKExited, "exited"},
		{"garbage", apierr.ProtocolError, "invalid SDK frame"},
		{"unknown-reply", apierr.ProtocolError, "unsolicited reply"},
		{"close-frames", apierr.FramesClosed, "frame pipe"},
	} {
		t.Run(tc.node, func(t *testing.T) {
			s := startFake(t, context.Background(), &recorder{})
			_, err := s.Request(callCtx(t), runStep(tc.node))
			if hostCode(err) != tc.code || !strings.Contains(err.Error(), tc.msg) {
				t.Fatalf("call = %v, want %s mentioning %q", err, tc.code, tc.msg)
			}
			if _, err := s.Request(callCtx(t), nodeproto.Request{Op: nodeproto.OpDescribe}); hostCode(err) != tc.code {
				t.Errorf("later call = %v, want the same %s", err, tc.code)
			}
			if err := s.Close(); hostCode(err) != tc.code {
				t.Errorf("close = %v, want %s", err, tc.code)
			}
		})
	}
}

func TestSessionCancelKillsGrandchildren(t *testing.T) {
	sink := &recorder{notify: make(chan nodeproto.FrameLog, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := startFake(t, ctx, sink)

	called := make(chan error, 1)
	go func() {
		_, err := s.Request(callCtx(t), runStep("grandchild"))
		called <- err
	}()
	var rec nodeproto.FrameLog
	select {
	case rec = <-sink.notify:
	case <-callCtx(t).Done():
		t.Fatal("the fake never reported its grandchild")
	}
	pid, err := strconv.Atoi(rec.Message)
	if err != nil {
		t.Fatalf("grandchild: %s", rec.Message)
	}
	cancel()
	if err := <-called; !errors.Is(err, context.Canceled) {
		t.Fatalf("call = %v, want the cancel", err)
	}
	if err := s.Close(); !errors.Is(err, context.Canceled) {
		t.Fatalf("close = %v, want the cancel", err)
	}
	processes, err := procgroup.List()
	if err != nil {
		t.Fatalf("list processes: %v", err)
	}
	for _, p := range processes {
		if p.PID == pid && !p.Terminated() {
			t.Fatalf("grandchild %d outlived the cancel: %+v", pid, p)
		}
	}
}

func TestSessionStuckWriteHonorsDeadlines(t *testing.T) {
	s := startFake(t, context.Background(), &recorder{})
	if _, err := s.Request(callCtx(t), runStep("deaf")); err != nil {
		t.Fatalf("run_step: %v", err)
	}
	// safety: larger than any pipe buffer, so the write blocks once the fake
	// stops reading.
	big := json.RawMessage(`{"pad":"` + strings.Repeat("x", 1<<20) + `"}`)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			if _, err := s.Request(ctx, nodeproto.Request{Op: nodeproto.OpEval, Closure: "c", Input: big}); err == nil {
				t.Error("a call whose write never finished succeeded")
			}
		}()
	}
	wg.Wait()
	if err := s.Close(); hostCode(err) != apierr.ProtocolError {
		t.Fatalf("close = %v, want protocol_error", err)
	}
}

func TestSessionReplyAfterCancelIsNotSuccess(t *testing.T) {
	sink := &recorder{notify: make(chan nodeproto.FrameLog, 2)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := startFake(t, ctx, sink)
	called := make(chan error, 1)
	go func() {
		_, err := s.Request(callCtx(t), runStep("term-reply"))
		called <- err
	}()
	next := func(want string) {
		select {
		case rec := <-sink.notify:
			if rec.Message != want {
				t.Fatalf("fake logged %q, want %q", rec.Message, want)
			}
		case <-callCtx(t).Done():
			t.Fatalf("the fake never logged %q", want)
		}
	}
	next("armed")
	s.mu.Lock()
	var pending protocolCall
	for _, c := range s.pending {
		pending = c
	}
	s.mu.Unlock()
	cancel()
	if err := <-called; !errors.Is(err, context.Canceled) {
		t.Fatalf("call = %v, want the cancel even though the SDK replied during shutdown", err)
	}
	// safety: the record follows the reply on the frame pipe, so the reply
	// has been read by the time it arrives.
	next("replied")
	if len(pending.reply) != 0 {
		t.Fatal("a reply read after the cancel was delivered to the call")
	}
}

func TestSessionReapsLeaderWhenCleanupTimesOut(t *testing.T) {
	defer func(old time.Duration) { reapTimeout = old }(reapTimeout)
	reapTimeout = 100 * time.Millisecond
	s := startFake(t, context.Background(), &recorder{})
	s.process.(groupProcess).SetDescendantProbe(func(int, bool, bool) (bool, error) { return false, nil })
	if err := s.Close(); !errors.Is(err, procgroup.ErrCleanup) {
		t.Fatalf("close = %v, want the cleanup timeout", err)
	}
	if s.cmd.ProcessState == nil {
		t.Fatal("the leader was left unreaped")
	}
}

func ask(ctx context.Context, process ExecutorProcess, req nodeproto.Request, result any) error {
	req.ID = "host"
	reply, err := process.Request(ctx, req)
	if err != nil {
		return err
	}
	return reply.Decode(result)
}
