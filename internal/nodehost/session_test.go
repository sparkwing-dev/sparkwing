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

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
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
		_ = w.Send(nodeproto.Reply{ID: id, OK: true, Result: raw})
	}
	log = func(step, msg string) {
		_ = w.Send(nodeproto.LogRecord{TS: "2026-01-02T03:04:05Z", Node: "forged", Step: step, Msg: msg})
	}
	handle := func(req nodeproto.Request) {
		switch {
		case req.Op == nodeproto.OpDescribe:
			ok(req.ID, map[string]any{"protocol": "1", "pipelines": []any{}})
		case req.Op == nodeproto.OpEval && barrier(req.Closure, ""):
			ok(req.ID, map[string]any{"value": req.Closure})
		case req.Op == nodeproto.OpRunStep && barrier(req.Step, req.Step):
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
		case req.Node == "garbage":
			_, _ = frames.WriteString("not a frame\n")
		case req.Node == "unknown-reply":
			ok("999", map[string]any{"outcome": "success"})
		case req.Node == "close-frames":
			_ = frames.Close()
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
	for {
		req, err := r.NextRequest()
		if err != nil {
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

func openFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatalf("list fds: %v", err)
	}
	return len(entries)
}

func startFake(t *testing.T, ctx context.Context, sink *recorder) *Session {
	t.Helper()
	before := openFDs(t)
	s, err := StartSession(ctx, SessionConfig{
		Entry: os.Args[0],
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
		if after := openFDs(t); after != before {
			t.Errorf("open fds went from %d to %d", before, after)
		}
	})
	return s
}

func callCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func runNode(node string) nodeproto.Request {
	return nodeproto.Request{Op: nodeproto.OpRunNode, Node: node, Attempt: 1}
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

	reply, err := s.Call(ctx, nodeproto.Request{Op: nodeproto.OpDescribe})
	if err != nil || !reply.OK || reply.ID != "1" {
		t.Fatalf("describe = %+v, %v", reply, err)
	}
	reply, err = s.Call(ctx, runNode("echo"))
	if err != nil {
		t.Fatalf("run_node: %v", err)
	}
	var result nodeproto.RunResult
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
	send := func(req nodeproto.Request) {
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
		send(nodeproto.Request{Op: nodeproto.OpEval, Closure: "barrier/3"})
	}
	for range 2 {
		send(nodeproto.Request{Op: nodeproto.OpRunStep, Node: "n", Step: "barrier/3", Attempt: 1})
	}
	// safety: the steps' barrier needs a third step, so they stay in flight
	// while the forbidden overlaps are tried.
	waitArrivals(t, sink, "barrier/3", 2)
	for _, req := range []nodeproto.Request{runNode("echo"), {Op: nodeproto.OpDescribe}} {
		if _, err := s.Call(ctx, req); !errors.Is(err, ErrBusy) {
			t.Errorf("%s during run_step = %v, want ErrBusy", req.Op, err)
		}
	}
	send(nodeproto.Request{Op: nodeproto.OpRunStep, Node: "n", Step: "barrier/3", Attempt: 1})
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
	var result nodeproto.RunResult
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
			if _, err := s.Call(callCtx(t), nodeproto.Request{Op: nodeproto.OpDescribe}); hostCode(err) != tc.code {
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
