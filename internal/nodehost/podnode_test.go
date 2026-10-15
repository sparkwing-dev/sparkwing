//go:build unix

package nodehost

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// A session that gives up on a sink stuck past the grace counts what the sink
// never got, so the node's log cannot seal as complete.
func TestSessionCountsLinesItsStuckSinkNeverGot(t *testing.T) {
	sink := &blockingSink{release: make(chan struct{})}
	t.Cleanup(func() { close(sink.release) })
	s, err := StartSession(context.Background(), SessionConfig{
		Entry: pipelineentry.Binary(os.Args[0]),
		Env:   append(os.Environ(), fakeSDKEnv+"=sdk", "GORACE=atexit_sleep_ms=0"),
		Sink:  sink,
		Grace: 100 * time.Millisecond, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}, Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := s.Call(callCtx(t), runStep("echo")); err != nil {
		t.Fatalf("run_step: %v", err)
	}
	if _, err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if s.Lost() == 0 {
		t.Fatal("a session that abandoned its sink lost nothing")
	}
}

// A session whose sink kept up loses nothing.
func TestSessionLosesNothingWhenItsSinkKeepsUp(t *testing.T) {
	s := startFake(t, context.Background(), &recorder{})
	if _, err := s.Call(callCtx(t), runStep("echo")); err != nil {
		t.Fatalf("run_step: %v", err)
	}
	if _, err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if n := s.Lost(); n != 0 {
		t.Fatalf("lost %d lines with a sink that kept up", n)
	}
}

// A flag describe marks secret is masked in every node's log of a hosted run.
func TestExecuteMasksSecretInputs(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a pipeline binary; run without -short")
	}
	bin := filepath.Join(t.TempDir(), "attemptfixture")
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/attemptfixture").CombinedOutput(); err != nil {
		t.Fatalf("build the fixture: %v\n%s", err, out)
	}
	srv, err := Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	logs := &memLogs{records: map[string][]sparkwing.LogRecord{}}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	res, err := Execute(ctx, Config{
		Session:  SessionConfig{Entry: pipelineentry.Binary(bin), Dir: t.TempDir(), Grace: 2 * time.Second, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}, Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}},
		Pipeline: "attempts", Args: map[string]string{"marker": t.TempDir(), "pass": "input-secret-4242"},
		Run:   nodeproto.RunContext{RunID: "run-secret-input"},
		State: openStore(t), Logger: slog.New(slog.DiscardHandler),
		Attempts: &Attempts{
			Routes: srv, Resolver: fixtureResolver{}, Masker: secrets.NewMasker(),
			OpenLog: func(_ context.Context, _, node string) (NodeLog, error) {
				logs.mu.Lock()
				defer logs.mu.Unlock()
				return memLog{logs: logs, node: node}, nil
			},
		},
	})
	if err != nil || res.Status != "success" {
		t.Fatalf("run = %+v, %v", res, err)
	}
	if text := logs.text("build"); strings.Contains(text, "input-secret-4242") || !strings.Contains(text, "pass=***") {
		t.Fatalf("build's log = %s, want its secret input masked", text)
	}
}

type memoMissSlots struct {
	ConcurrencySlots
	acquired store.AcquireSlotRequest
	released bool
}

func (s *memoMissSlots) AcquireSlot(_ context.Context, in store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	s.acquired = in
	return store.AcquireSlotResponse{Kind: store.AcquireGranted}, nil
}

func (s *memoMissSlots) ReleaseSlot(context.Context, string, string, string, string, string, time.Duration) error {
	s.released = true
	return nil
}

// safety: a follower must not turn a missing output into successful empty output.
func TestMemoFollowerWithNoOutputRunsTheNode(t *testing.T) {
	slots := &memoMissSlots{}
	m := &Memo{
		Slots: slots,
		Source: func(context.Context, store.ClaimInputRequest, string, string) (MemoSource, error) {
			return MemoSource{}, store.ErrNotFound
		},
	}
	run := &Run{ID: "run-follower", Logger: slog.New(slog.DiscardHandler), validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}
	ran := false
	r := &memoRun{
		cfg: &Config{}, m: m, adm: &Admission{Slots: m.Slots}, n: &NodeRun{Run: run, Plan: nodeproto.JobSpec{ID: "n", Steps: []nodeproto.StepSpec{{ID: "body"}}}},
		next: func(context.Context, *NodeRun) Result {
			ran = true
			return Result{Outcome: sparkwing.Success, Output: []byte(`"own"`), recorded: true}
		},
		g: group{key: memoKeyPrefix + "h", policy: store.OnLimitCoalesce, capacity: 1, cost: 1, memoHash: "h"},
	}
	res, copied := r.follow(t.Context(), "run-leader", "n")
	if !ran || copied || !slots.acquired.BypassRead || !slots.released || res.Outcome != sparkwing.Success || string(res.Output) != `"own"` {
		t.Fatalf("follower = %+v (ran %v, copied %v), want the node run as a miss", res, ran, copied)
	}
}
