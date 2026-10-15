//go:build unix

package nodehost

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
)

// A session that gives up on a sink stuck past the grace counts what the sink
// never got, so the node's log cannot seal as complete.
func TestSessionCountsLinesItsStuckSinkNeverGot(t *testing.T) {
	sink := &blockingSink{release: make(chan struct{})}
	t.Cleanup(func() { close(sink.release) })
	s, err := startConfiguredProcess(context.Background(), SessionConfig{
		Entry: pipelineentry.Binary(os.Args[0]),
		Env:   append(os.Environ(), fakeSDKEnv+"=sdk", "GORACE=atexit_sleep_ms=0"),
		Sink:  sink,
		Grace: 100 * time.Millisecond, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}, Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := s.Request(callCtx(t), runStep("echo")); err != nil {
		t.Fatalf("run_step: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if s.cfg.output.lost == 0 {
		t.Fatal("a session that abandoned its sink lost nothing")
	}
}

// A session whose sink kept up loses nothing.
func TestSessionLosesNothingWhenItsSinkKeepsUp(t *testing.T) {
	s := startFake(t, context.Background(), &recorder{})
	if _, err := s.Request(callCtx(t), runStep("echo")); err != nil {
		t.Fatalf("run_step: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if n := s.cfg.output.lost; n != 0 {
		t.Fatalf("lost %d lines with a sink that kept up", n)
	}
}
