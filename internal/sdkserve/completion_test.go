package sdkserve

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

type completedOperations struct {
	wireOperations
	second chan struct{}
}

func (f *completedOperations) RunStep(_ context.Context, _ any, req nodeproto.Request) (nodeproto.RunResult, error) {
	if req.ID == "second" {
		close(f.second)
	}
	return nodeproto.RunResult{Outcome: "success"}, nil
}

type heldReplyWriter struct {
	bytes.Buffer
	written chan struct{}
	release chan struct{}
}

func (w *heldReplyWriter) Write(raw []byte) (int, error) {
	n, err := w.Buffer.Write(raw)
	if bytes.Contains(raw, []byte(`"reply":"first"`)) {
		close(w.written)
		<-w.release
	}
	return n, err
}

func TestServeCompletionReleasesStepBeforeReply(t *testing.T) {
	fake := &completedOperations{second: make(chan struct{})}
	out := &heldReplyWriter{written: make(chan struct{}), release: make(chan struct{})}
	in, requests := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- Serve(t.Context(), in, out, Config{Operations: fake}) }()
	defer func() { close(out.release); _ = requests.Close(); <-done }()
	for _, line := range []string{wireHello("hello"), wirePlan("plan"), `{"id":"first","op":"run_step","job":"j","step":"s","attempt":1,"settled":[]}`} {
		if _, err := io.WriteString(requests, line+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	<-out.written
	if _, err := io.WriteString(requests, `{"id":"second","op":"run_step","job":"j","step":"s","attempt":2,"settled":[]}`+"\n"); err != nil {
		t.Fatal(err)
	}
	<-fake.second
}
