package sparkwing

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type stubAwaiter struct {
	lastReq AwaitRequest
	runID   string
	data    []byte
	err     error
}

func (s *stubAwaiter) Await(_ context.Context, req AwaitRequest) (*ResolvedPipelineRef, error) {
	s.lastReq = req
	if s.err != nil {
		return nil, s.err
	}
	return &ResolvedPipelineRef{RunID: s.runID, Data: s.data}, nil
}

type awaitOut struct {
	Image string `json:"image"`
}

func TestRunAndAwait_HappyPath(t *testing.T) {
	payload, _ := json.Marshal(awaitOut{Image: "registry/app:v1"})
	aw := &stubAwaiter{runID: "child", data: payload}
	ctx := context.WithValue(context.Background(), keyPipelineAwaiter, aw)

	got, err := RunAndAwait[awaitOut, NoInputs](
		ctx, "upstream", "build",
		WithFreshTimeout(30*time.Second),
		WithFreshArgs(map[string]string{"env": "prod"}),
	)
	if err != nil {
		t.Fatalf("RunAndAwait: %v", err)
	}
	if got.Image != "registry/app:v1" {
		t.Fatalf("out: %+v", got)
	}
	if aw.lastReq.Pipeline != "upstream" || aw.lastReq.NodeID != "build" {
		t.Fatalf("req: %+v", aw.lastReq)
	}
	if aw.lastReq.Timeout != 30*time.Second {
		t.Fatalf("timeout: %v", aw.lastReq.Timeout)
	}
	if aw.lastReq.Args["env"] != "prod" {
		t.Fatalf("args: %v", aw.lastReq.Args)
	}
}

func TestRunAndAwait_NoAwaiterInstalled(t *testing.T) {
	_, err := RunAndAwait[awaitOut, NoInputs](context.Background(), "x", "y")
	if err == nil || !strings.Contains(err.Error(), "no awaiter installed") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestRunAndAwait_AwaiterError(t *testing.T) {
	aw := &stubAwaiter{err: errors.New("boom")}
	ctx := context.WithValue(context.Background(), keyPipelineAwaiter, aw)

	_, err := RunAndAwait[awaitOut, NoInputs](ctx, "up", "n")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "up/n") {
		t.Fatalf("error missing pipeline/node context: %v", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error lost underlying cause: %v", err)
	}
}

func TestRunAndAwait_EmptyDataZeroValue(t *testing.T) {
	aw := &stubAwaiter{runID: "child", data: nil}
	ctx := context.WithValue(context.Background(), keyPipelineAwaiter, aw)

	got, err := RunAndAwait[awaitOut, NoInputs](ctx, "up", "n")
	if err != nil {
		t.Fatal(err)
	}
	if got.Image != "" {
		t.Fatalf("want zero, got %+v", got)
	}
}

func TestAwaitOption_Defaults(t *testing.T) {
	aw := &stubAwaiter{runID: "x", data: []byte(`{}`)}
	ctx := context.WithValue(context.Background(), keyPipelineAwaiter, aw)
	_, _ = RunAndAwait[awaitOut, NoInputs](ctx, "up", "n")
	if aw.lastReq.Timeout != 0 {
		t.Fatalf("default timeout: %v", aw.lastReq.Timeout)
	}
	if aw.lastReq.Args != nil {
		t.Fatalf("default args: %v", aw.lastReq.Args)
	}
}
