package orchestrator

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCopySSEStream_PrintsLogLinesWithoutTheFraming(t *testing.T) {
	body := strings.Join([]string{
		": open",
		"",
		"id: 42",
		`data: {"msg":"first"}`,
		"",
		": keepalive",
		"",
		"id: 84",
		`data: {"msg":"second"}`,
		"",
		"event: stream_end",
		"data: {}",
		"",
	}, "\n")

	var out bytes.Buffer
	var multi atomic.Bool
	copySSEStream(context.Background(), strings.NewReader(body), "build", &multi, &sync.Mutex{}, &out)

	got := out.String()
	want := "{\"msg\":\"first\"}\n{\"msg\":\"second\"}\n{}\n"
	if got != want {
		t.Fatalf("follow output =\n%q\nwant\n%q", got, want)
	}
	for _, framing := range []string{"id:", "data:", ": open", ": keepalive", "event:"} {
		if strings.Contains(got, framing) {
			t.Errorf("follow output leaked %q framing:\n%s", framing, got)
		}
	}
}

func TestCopySSEStream_PrefixesEveryLineWhenFollowingSeveralNodes(t *testing.T) {
	var out bytes.Buffer
	var multi atomic.Bool
	multi.Store(true)
	copySSEStream(context.Background(), strings.NewReader("data: hello\n\n"), "build", &multi, &sync.Mutex{}, &out)
	if got := out.String(); got != "[build] hello\n" {
		t.Fatalf("multi-node output = %q", got)
	}
}

func TestCopySSEStream_StopsOnACancelledFollow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	var multi atomic.Bool
	copySSEStream(ctx, io.LimitReader(strings.NewReader("data: hello\n\n"), 64), "build", &multi, &sync.Mutex{}, &out)
	if out.Len() != 0 {
		t.Fatalf("cancelled follow still wrote %q", out.String())
	}
}
