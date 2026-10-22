package sdkserve

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

type wireOperations struct {
	plans       int
	stepStarted chan struct{}
	stepStopped chan struct{}
}

func (f *wireOperations) Document(any) nodeproto.PlanDoc {
	return nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{}}
}

func (f *wireOperations) Context(ctx context.Context, _ nodeproto.RunContext, _ nodeproto.RunnerIdentity, _ Services) context.Context {
	return ctx
}

func (f *wireOperations) Describe(context.Context) (nodeproto.DescribeDoc, error) {
	return nodeproto.DescribeDoc{}, nil
}

func (f *wireOperations) Plan(context.Context, string, map[string]string, nodeproto.RunContext) (any, nodeproto.PlanDoc, error) {
	f.plans++
	return struct{}{}, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{}}, nil
}

func (f *wireOperations) RunStep(ctx context.Context, _ any, _ nodeproto.Request) (nodeproto.RunResult, error) {
	close(f.stepStarted)
	<-ctx.Done()
	close(f.stepStopped)
	return nodeproto.RunResult{Outcome: "failed", Error: &apierr.Error{Code: apierr.Cancelled, Message: "step cancelled"}}, nil
}

func (f *wireOperations) Eval(context.Context, any, nodeproto.Request) (nodeproto.EvalResult, error) {
	return nodeproto.EvalResult{Value: json.RawMessage(`true`)}, nil
}

func wireHello(id string) string {
	return `{"id":"` + id + `","op":"hello","protocol":2,"engine":{"version":"v1"},"runner":{"name":"local","type":"local","labels":[]},"binding":{"run_id":"r","job_id":"j","routes":{"url":"http://host","bearer":"secret"}}}`
}

func wirePlan(id string) string {
	return `{"id":"` + id + `","op":"plan","pipeline":"p","args":{},"run":{"run_id":"r","pipeline":"p"}}`
}

type wireReply struct {
	ID    string `json:"reply"`
	OK    bool   `json:"ok"`
	Error *struct {
		Code apierr.Code `json:"code"`
	} `json:"error"`
}

func readWireReplies(t *testing.T, raw []byte) map[string]wireReply {
	t.Helper()
	replies := map[string]wireReply{}
	scan := bufio.NewScanner(bytes.NewReader(raw))
	for scan.Scan() {
		var reply wireReply
		if err := json.Unmarshal(scan.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		if reply.ID == "" {
			t.Fatalf("non-reply frame: %s", scan.Bytes())
		}
		replies[reply.ID] = reply
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	return replies
}

func TestServeNegotiatesOnceAndPlansOnce(t *testing.T) {
	fake := &wireOperations{}
	requests := strings.Join([]string{
		`{"id":"pre","op":"describe"}`,
		wireHello("hello"),
		wireHello("again"),
		wirePlan("plan"),
		wirePlan("repeat"),
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := Serve(context.Background(), strings.NewReader(requests), &output, Config{SDKVersion: "v1", Operations: fake}); err != nil {
		t.Fatal(err)
	}
	replies := readWireReplies(t, output.Bytes())
	if len(replies) != 5 || replies["pre"].Error.Code != apierr.ProtocolError || !replies["hello"].OK ||
		replies["again"].Error.Code != apierr.ProtocolError || !replies["plan"].OK ||
		replies["repeat"].Error.Code != apierr.ProtocolError || fake.plans != 1 {
		t.Fatalf("replies=%+v plans=%d", replies, fake.plans)
	}
}

func TestServeRejectsReusedCorrelationID(t *testing.T) {
	fake := &wireOperations{}
	var output bytes.Buffer
	requests := wireHello("same") + "\n" + wirePlan("same") + "\n"
	if err := Serve(context.Background(), strings.NewReader(requests), &output, Config{SDKVersion: "v1", Operations: fake}); err == nil {
		t.Fatal("reused ID was accepted")
	}
	if fake.plans != 0 {
		t.Fatalf("reused ID executed plan %d times", fake.plans)
	}
}

func TestServeRejectsMismatchedHelloBeforeOperations(t *testing.T) {
	fake := &wireOperations{}
	request := strings.Replace(wireHello("hello"), `"protocol":2`, `"protocol":1`, 1)
	var output bytes.Buffer
	if err := Serve(context.Background(), strings.NewReader(request+"\n"+`{"id":"describe","op":"describe"}`+"\n"), &output, Config{SDKVersion: "v1", Operations: fake}); err != nil {
		t.Fatal(err)
	}
	replies := readWireReplies(t, output.Bytes())
	if replies["hello"].Error == nil || replies["hello"].Error.Code != apierr.ProtocolMismatch ||
		replies["describe"].Error == nil || replies["describe"].Error.Code != apierr.ProtocolError {
		t.Fatalf("mismatch negotiated: %+v", replies)
	}
}

func TestServeCancellationKeepsBothReplies(t *testing.T) {
	fake := &wireOperations{stepStarted: make(chan struct{}), stepStopped: make(chan struct{})}
	in, writer := io.Pipe()
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), in, &output, Config{SDKVersion: "v1", Operations: fake}) }()
	for _, line := range []string{wireHello("hello"), wirePlan("plan"), `{"id":"step","op":"run_step","job":"j","step":"s","attempt":1,"settled":[]}`} {
		if _, err := io.WriteString(writer, line+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	<-fake.stepStarted
	if _, err := io.WriteString(writer, `{"id":"cancel","op":"cancel","job":"j","step":"s"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	<-fake.stepStopped
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	replies := readWireReplies(t, output.Bytes())
	if len(replies) != 4 || !replies["cancel"].OK || !replies["step"].OK {
		t.Fatalf("cancel and step replies: %+v", replies)
	}
}

func TestServeInputCloseJoinsRunningStep(t *testing.T) {
	fake := &wireOperations{stepStarted: make(chan struct{}), stepStopped: make(chan struct{})}
	in, writer := io.Pipe()
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), in, &output, Config{SDKVersion: "v1", Operations: fake}) }()
	for _, line := range []string{wireHello("hello"), wirePlan("plan"), `{"id":"step","op":"run_step","job":"j","step":"s","attempt":1,"settled":[]}`} {
		if _, err := io.WriteString(writer, line+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	<-fake.stepStarted
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-fake.stepStopped:
	default:
		t.Fatal("Serve returned before the running step stopped")
	}
	if replies := readWireReplies(t, output.Bytes()); len(replies) != 3 || !replies["step"].OK {
		t.Fatalf("step reply after input close: %+v", replies)
	}
}

func TestServeCancellationClosesIdleInput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		in, writer := io.Pipe()
		defer writer.Close()
		done := make(chan error, 1)
		go func() { done <- Serve(ctx, in, io.Discard, Config{Operations: &wireOperations{}}) }()
		synctest.Wait()
		cancel()
		synctest.Wait()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled server: %v", err)
			}
		default:
			t.Fatal("server remained blocked on input after cancellation")
		}
	})
}

func TestServeEvalUsesClosureWithoutJob(t *testing.T) {
	fake := &wireOperations{}
	requests := strings.Join([]string{wireHello("hello"), wirePlan("plan"), `{"id":"eval","op":"eval","closure":"c","input":null}`}, "\n") + "\n"
	var output bytes.Buffer
	if err := Serve(context.Background(), strings.NewReader(requests), &output, Config{SDKVersion: "v1", Operations: fake}); err != nil {
		t.Fatal(err)
	}
	if reply := readWireReplies(t, output.Bytes())["eval"]; !reply.OK {
		t.Fatalf("eval rejected: %+v", reply)
	}
}

func (*wireOperations) Arguments(any) []nodeproto.ArgumentDescription { return nil }
