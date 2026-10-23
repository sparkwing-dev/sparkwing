package sdkserve

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

type joinedOperations struct {
	wireOperations
	started chan string
	stopped chan string
	release chan struct{}
}

func (f *joinedOperations) wait(ctx context.Context, req nodeproto.Request) {
	f.started <- req.ID
	<-ctx.Done()
	f.stopped <- req.ID
	<-f.release
}

func (f *joinedOperations) RunStep(ctx context.Context, _ any, req nodeproto.Request) (nodeproto.RunResult, error) {
	f.wait(ctx, req)
	return nodeproto.RunResult{Outcome: "success"}, nil
}

func (f *joinedOperations) Eval(ctx context.Context, _ any, req nodeproto.Request) (nodeproto.EvalResult, error) {
	f.wait(ctx, req)
	return nodeproto.EvalResult{Value: []byte("null")}, nil
}

func TestCancelJoinsSelectedStepsAndEvals(t *testing.T) {
	for _, test := range []struct {
		name, selector string
		step           bool
	}{
		{"job", `"job":"j"`, true},
		{"target-eval", `"target":"eval"`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				fake := &joinedOperations{started: make(chan string, 2), stopped: make(chan string, 2), release: make(chan struct{})}
				in, writer := io.Pipe()
				var output bytes.Buffer
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan error, 1)
				go func() { done <- Serve(ctx, in, &output, Config{Operations: fake}) }()
				defer func() {
					select {
					case <-fake.release:
					default:
						close(fake.release)
					}
					cancel()
					_ = writer.Close()
					<-done
				}()
				send := func(line string) {
					t.Helper()
					if _, err := io.WriteString(writer, line+"\n"); err != nil {
						t.Fatal(err)
					}
				}
				send(wireHello("hello"))
				send(wirePlan("plan"))
				send(`{"id":"eval","op":"eval","closure":"condition"}`)
				<-fake.started
				if test.step {
					send(`{"id":"step","op":"run_step","job":"j","step":"s","attempt":1,"settled":[]}`)
					<-fake.started
				}
				send(`{"id":"cancel","op":"cancel",` + test.selector + `}`)
				synctest.Wait()
				expected := 1
				if test.step {
					expected = 2
				}
				if len(fake.stopped) != expected {
					t.Fatalf("cancel selected %d operations, want %d", len(fake.stopped), expected)
				}
				if strings.Contains(output.String(), `"reply":"cancel"`) {
					t.Fatal("cancel acknowledged before work joined")
				}
				close(fake.release)
				synctest.Wait()
				replies := readWireReplies(t, output.Bytes())
				if !replies["cancel"].OK || !replies["eval"].OK || test.step && !replies["step"].OK {
					t.Fatalf("replies = %+v", replies)
				}
			})
		})
	}
}

func TestCancelRejectsForeignBinding(t *testing.T) {
	var output bytes.Buffer
	requests := wireHello("hello") + "\n" + `{"id":"cancel","op":"cancel","job":"foreign"}` + "\n"
	if err := Serve(t.Context(), strings.NewReader(requests), &output, Config{Operations: &wireOperations{}}); err != nil {
		t.Fatal(err)
	}
	if reply := readWireReplies(t, output.Bytes())["cancel"]; reply.Error == nil {
		t.Fatal("foreign cancellation accepted")
	}
}
