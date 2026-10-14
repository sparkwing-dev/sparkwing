//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/testleak"
)

// safety: set, the test binary acts as an SDK process (or, as "sleep", as a
// grandchild that blocks until killed) instead of running the tests.
const fakeSDKEnv = "NODEHOST_FAKE_SDK"

func TestMain(m *testing.M) {
	switch os.Getenv(fakeSDKEnv) {
	case "sdk":
		fakeSDK()
	case "sleep":
		select {}
	}
	testleak.Main(m)
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
		_ = w.Send(nodeproto.ReplyOld{ID: id, OK: true, Result: raw})
	}
	log = func(step, msg string) {
		_ = w.Send(nodeproto.LogRecord{TS: "2026-01-02T03:04:05Z", Node: "forged", Step: step, Msg: msg})
	}
	handle := func(req nodeproto.RequestOld) {
		switch {
		case req.Op == nodeproto.OpDescribeOld:
			ok(req.ID, map[string]any{"protocol": "1", "pipelines": []any{}})
		case req.Op == nodeproto.OpEvalOld && barrier(req.Closure, ""):
			ok(req.ID, map[string]any{"value": req.Closure})
		case req.Op == nodeproto.OpRunStepOld && barrier(req.Step, req.Step):
			log(req.Step, "in "+req.Step)
			ok(req.ID, map[string]any{"outcome": "success"})
		case req.Node == "echo":
			log("ghost", "hello")
			ok(req.ID, map[string]any{"outcome": "success", "output": map[string]any{"node": "echo"}})
		case req.Node == "forge":
			fmt.Printf(`{"reply":%q,"ok":true,"result":{"outcome":"failed"}}`+"\n", req.ID)
			fmt.Fprintln(os.Stderr, "warned")
			ok(req.ID, map[string]any{"outcome": "success"})
		case req.Node == "exit":
			os.Exit(3)
		case req.Op == nodeproto.OpPlanOld:
			node := map[string]any{"id": "reply-then-garbage", "deps": []any{}}
			ok(req.ID, map[string]any{"protocol": "1", "pipeline": req.Pipeline, "run_id": req.Run.RunID, "nodes": []any{node}})
		case req.Node == "garbage":
			_, _ = frames.WriteString("not a frame\n")
		case req.Node == "unknown-reply":
			ok("999", map[string]any{"outcome": "success"})
		case req.Node == "close-frames":
			_ = frames.Close()
		case req.Node == "term-reply":
			term := make(chan os.Signal, 1)
			signal.Notify(term, syscall.SIGTERM)
			log("", "armed")
			<-term
			ok(req.ID, map[string]any{"outcome": "success"})
			log("", "replied")
			os.Exit(0)
		case req.Node == "grandchild":
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
	for {
		req, err := r.NextRequest()
		if err != nil {
			if garbageAtEOF {
				_, _ = frames.WriteString("not a frame\n")
			}
			os.Exit(0)
		}
		switch req.Node {
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
	records []nodeproto.LogRecord
	notify  chan nodeproto.LogRecord
}

func (r *recorder) Text(level, line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.text = append(r.text, level+" "+line)
}

func (r *recorder) Record(rec nodeproto.LogRecord) {
	r.mu.Lock()
	r.records = append(r.records, rec)
	r.mu.Unlock()
	if r.notify != nil {
		r.notify <- rec
	}
}

func (r *recorder) snapshot() ([]string, []nodeproto.LogRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.text...), append([]nodeproto.LogRecord(nil), r.records...)
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

func startFake(t *testing.T, ctx context.Context, sink *recorder) *Session {
	t.Helper()
	before := openPipes(t)
	s, err := StartSession(ctx, SessionConfig{
		Entry: pipelineentry.Binary(os.Args[0]),
		// bug: the race runtime otherwise sleeps a second at exit, past the grace.
		Env:   append(os.Environ(), fakeSDKEnv+"=sdk", "GORACE=atexit_sleep_ms=0"),
		Sink:  sink,
		Grace: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.Close()
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

func runNode(node string) nodeproto.RequestOld {
	return nodeproto.RequestOld{Op: nodeproto.OpRunNodeOld, Node: node, Attempt: 1}
}

func hostCode(err error) nodeproto.ErrorCode {
	var perr *nodeproto.Error
	if errors.As(err, &perr) {
		return perr.Code
	}
	return ""
}

func TestSessionRequestReply(t *testing.T) {
	sink := &recorder{}
	s := startFake(t, context.Background(), sink)
	ctx := callCtx(t)

	reply, err := s.Call(ctx, nodeproto.RequestOld{Op: nodeproto.OpDescribeOld})
	if err != nil || !reply.OK || reply.ID != "1" {
		t.Fatalf("describe = %+v, %v", reply, err)
	}
	reply, err = s.Call(ctx, runNode("echo"))
	if err != nil {
		t.Fatalf("run_node: %v", err)
	}
	var result nodeproto.RunResultOld
	if err := reply.Decode(&result); err != nil || string(result.Output) != `{"node":"echo"}` {
		t.Fatalf("run_node result = %+v, %v", result, err)
	}
	usage, err := s.Close()
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if usage.Wall <= 0 {
		t.Errorf("usage = %+v, want a wall time", usage)
	}
	_, records := sink.snapshot()
	if len(records) != 1 || records[0].Msg != "hello" || records[0].Step != "" {
		t.Errorf("records = %+v, want hello with the step of no in-flight run_step dropped", records)
	}
	if _, err := s.Call(ctx, runNode("echo")); !errors.Is(err, ErrClosed) {
		t.Errorf("call after close = %v, want ErrClosed", err)
	}
}

func TestSessionConcurrentEvalsAndSteps(t *testing.T) {
	sink := &recorder{notify: make(chan nodeproto.LogRecord, 64)}
	s := startFake(t, context.Background(), sink)
	ctx := callCtx(t)

	var wg sync.WaitGroup
	errs := make(chan error, 6)
	send := func(req nodeproto.RequestOld) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reply, err := s.Call(ctx, req)
			if err == nil && !reply.OK {
				err = reply.Error
			}
			errs <- err
		}()
	}
	for range 3 {
		send(nodeproto.RequestOld{Op: nodeproto.OpEvalOld, Closure: "barrier/3"})
	}
	for range 2 {
		send(nodeproto.RequestOld{Op: nodeproto.OpRunStepOld, Node: "n", Step: "barrier/3", Attempt: 1})
	}
	// safety: the steps' barrier needs a third step, so they stay in flight
	// while the forbidden overlaps are tried.
	waitArrivals(t, sink, "barrier/3", 2)
	for _, req := range []nodeproto.RequestOld{runNode("echo"), {Op: nodeproto.OpDescribeOld}} {
		if _, err := s.Call(ctx, req); !errors.Is(err, ErrBusy) {
			t.Errorf("%s during run_step = %v, want ErrBusy", req.Op, err)
		}
	}
	send(nodeproto.RequestOld{Op: nodeproto.OpRunStepOld, Node: "n", Step: "barrier/3", Attempt: 1})
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent call: %v", err)
		}
	}
	_, records := sink.snapshot()
	for _, rec := range records {
		if rec.Step != "barrier/3" && rec.Msg != "arrived" {
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
			if rec.Msg == "arrived" && rec.Step == step {
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
	reply, err := s.Call(callCtx(t), runNode("forge"))
	if err != nil {
		t.Fatalf("run_node: %v", err)
	}
	var result nodeproto.RunResultOld
	if err := reply.Decode(&result); err != nil || result.Outcome != nodeproto.OutcomeSuccess {
		t.Fatalf("result = %+v, %v; the stdout reply must not count", result, err)
	}
	if _, err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	text, _ := sink.snapshot()
	want := []string{`info {"reply":"1","ok":true,"result":{"outcome":"failed"}}`, "warn warned"}
	for _, line := range want {
		found := false
		for _, got := range text {
			found = found || got == line
		}
		if !found {
			t.Errorf("text %q missing from %q", line, text)
		}
	}
}

func TestSessionEndsWithHostCode(t *testing.T) {
	for _, tc := range []struct {
		node string
		code nodeproto.ErrorCode
		msg  string
	}{
		{"exit", nodeproto.CodeSDKExited, "exit status 3"},
		{"garbage", nodeproto.CodeProtocolError, "not a JSON object"},
		{"unknown-reply", nodeproto.CodeProtocolError, "unknown request"},
		{"close-frames", nodeproto.CodeFramesClosed, "frame pipe"},
	} {
		t.Run(tc.node, func(t *testing.T) {
			s := startFake(t, context.Background(), &recorder{})
			_, err := s.Call(callCtx(t), runNode(tc.node))
			if hostCode(err) != tc.code || !strings.Contains(err.Error(), tc.msg) {
				t.Fatalf("call = %v, want %s mentioning %q", err, tc.code, tc.msg)
			}
			if _, err := s.Call(callCtx(t), nodeproto.RequestOld{Op: nodeproto.OpDescribeOld}); hostCode(err) != tc.code {
				t.Errorf("later call = %v, want the same %s", err, tc.code)
			}
			if _, err := s.Close(); hostCode(err) != tc.code {
				t.Errorf("close = %v, want %s", err, tc.code)
			}
		})
	}
}

func TestSessionCancelKillsGrandchildren(t *testing.T) {
	sink := &recorder{notify: make(chan nodeproto.LogRecord, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := startFake(t, ctx, sink)

	called := make(chan error, 1)
	go func() {
		_, err := s.Call(callCtx(t), runNode("grandchild"))
		called <- err
	}()
	var rec nodeproto.LogRecord
	select {
	case rec = <-sink.notify:
	case <-callCtx(t).Done():
		t.Fatal("the fake never reported its grandchild")
	}
	pid, err := strconv.Atoi(rec.Msg)
	if err != nil {
		t.Fatalf("grandchild: %s", rec.Msg)
	}
	cancel()
	if err := <-called; !errors.Is(err, context.Canceled) {
		t.Fatalf("call = %v, want the cancel", err)
	}
	if _, err := s.Close(); !errors.Is(err, context.Canceled) {
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

type blockingSink struct {
	release chan struct{}
	mu      sync.Mutex
	text    []string
}

func (b *blockingSink) Text(level, line string) {
	<-b.release
	b.mu.Lock()
	defer b.mu.Unlock()
	b.text = append(b.text, level+" "+line)
}

func (b *blockingSink) Record(rec nodeproto.LogRecord) {
	<-b.release
	b.mu.Lock()
	defer b.mu.Unlock()
	b.text = append(b.text, rec.Level+" "+rec.Msg)
}

func TestSessionBlockedSinkNeitherStallsRepliesNorClose(t *testing.T) {
	sink := &blockingSink{release: make(chan struct{})}
	t.Cleanup(func() { close(sink.release) })
	before := openPipes(t)
	s, err := StartSession(context.Background(), SessionConfig{
		Entry: pipelineentry.Binary(os.Args[0]),
		Env:   append(os.Environ(), fakeSDKEnv+"=sdk", "GORACE=atexit_sleep_ms=0"),
		Sink:  sink,
		Grace: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	for range 3 {
		if _, err := s.Call(callCtx(t), runNode("echo")); err != nil {
			t.Fatalf("run_node behind a blocked sink: %v", err)
		}
	}
	if _, err := s.Close(); err != nil {
		t.Fatalf("close behind a blocked sink: %v", err)
	}
	checkNoNewPipes(t, before)
}

func TestSinkQueueCountsDrops(t *testing.T) {
	defer func(old int) { sinkQueueBytes = old }(sinkQueueBytes)
	sinkQueueBytes = 1000
	sink := &blockingSink{release: make(chan struct{})}
	q := newSinkQueue()
	go q.run(sink)
	for i := range 50 {
		q.push(sinkItem{level: LevelInfo, text: strings.Repeat("x", 90) + strconv.Itoa(i), size: 92})
	}
	close(sink.release)
	q.close()
	<-q.done
	kept := len(sink.text) - 1
	want := fmt.Sprintf("warn sparkwing: %d log lines dropped while the log sink was behind", 50-kept)
	if kept >= 50 || sink.text[len(sink.text)-1] != want {
		t.Fatalf("delivered %d lines ending %q, want drops counted as %q", kept, sink.text[len(sink.text)-1], want)
	}
}

func TestSessionStuckWriteHonorsDeadlines(t *testing.T) {
	s := startFake(t, context.Background(), &recorder{})
	if _, err := s.Call(callCtx(t), runNode("deaf")); err != nil {
		t.Fatalf("run_node: %v", err)
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
			if _, err := s.Call(ctx, nodeproto.RequestOld{Op: nodeproto.OpEvalOld, Closure: "c", Input: big}); err == nil {
				t.Error("a call whose write never finished succeeded")
			}
		}()
	}
	wg.Wait()
	if _, err := s.Close(); hostCode(err) != nodeproto.CodeProtocolError {
		t.Fatalf("close = %v, want protocol_error", err)
	}
}

func TestSessionReplyAfterCancelIsNotSuccess(t *testing.T) {
	sink := &recorder{notify: make(chan nodeproto.LogRecord, 2)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := startFake(t, ctx, sink)
	called := make(chan error, 1)
	go func() {
		_, err := s.Call(callCtx(t), runNode("term-reply"))
		called <- err
	}()
	next := func(want string) {
		select {
		case rec := <-sink.notify:
			if rec.Msg != want {
				t.Fatalf("fake logged %q, want %q", rec.Msg, want)
			}
		case <-callCtx(t).Done():
			t.Fatalf("the fake never logged %q", want)
		}
	}
	next("armed")
	s.mu.Lock()
	var pending call
	for _, c := range s.inflight {
		pending = c
	}
	s.mu.Unlock()
	cancel()
	if err := <-called; !errors.Is(err, context.Canceled) {
		t.Fatalf("call = %v, want the cancel even though the SDK replied on SIGTERM", err)
	}
	// safety: the record follows the reply on the frame pipe, so the reply
	// has been read by the time it arrives.
	next("replied")
	if len(pending.reply) != 0 {
		t.Fatal("a reply read after the cancel was delivered to the call")
	}
}

func TestSessionWriteFailureDuringExitIsSDKExited(t *testing.T) {
	s := startFake(t, context.Background(), &recorder{})
	failed := make(chan struct{})
	go func() {
		s.writeFailed(errors.New("broken pipe"))
		close(failed)
	}()
	if err := s.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	<-failed
	if err := s.failure(); hostCode(err) != nodeproto.CodeSDKExited {
		t.Fatalf("failure = %v, want sdk_exited for a write that broke as the SDK exited", err)
	}
}

func TestSessionReapsLeaderWhenCleanupTimesOut(t *testing.T) {
	defer func(old time.Duration) { reapTimeout = old }(reapTimeout)
	reapTimeout = 100 * time.Millisecond
	s := startFake(t, context.Background(), &recorder{})
	s.proc.(groupProcess).SetDescendantProbe(func(int, bool, bool) (bool, error) { return false, nil })
	if _, err := s.Close(); !errors.Is(err, procgroup.ErrCleanup) {
		t.Fatalf("close = %v, want the cleanup timeout", err)
	}
	if s.cmd.ProcessState == nil {
		t.Fatal("the leader was left unreaped")
	}
}
