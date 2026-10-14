//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestExecutePlansWithTheSDK(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a pipeline binary; run without -short")
	}
	bin := filepath.Join(t.TempDir(), "gofixture")
	build := exec.Command("go", "build", "-o", bin, "../nodeproto/conformance/gofixture")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the Go fixture: %v\n%s", err, out)
	}
	config := func(st *store.Store, runID string, args map[string]string, exec Executor) Config {
		return Config{
			Session:   SessionConfig{Entry: pipelineentry.Binary(bin), Dir: t.TempDir(), Grace: 2 * time.Second},
			Pipeline:  "conformance",
			Args:      args,
			Run:       nodeproto.RunContextOld{RunID: runID, Git: &nodeproto.Git{SHA: "4f2c1e0", Branch: "main", Repo: "acme/app"}},
			State:     st,
			Execute:   exec,
			Logger:    slog.New(slog.DiscardHandler),
			Heartbeat: time.Hour,
		}
	}
	t.Run("RunsThePlannedNodes", func(t *testing.T) { runsThePlannedNodes(t, config) })
	t.Run("APlanFailureFinishesTheRunRow", func(t *testing.T) { aPlanFailureFinishesTheRunRow(t, config) })
	t.Run("AnUnknownPipelineWritesNoRun", func(t *testing.T) { anUnknownPipelineWritesNoRun(t, config) })
	t.Run("AnUnrecordedFinishFailsTheRun", func(t *testing.T) { anUnrecordedFinishFailsTheRun(t, config) })
	t.Run("TheRunLeaseOutlivesTheRunRow", func(t *testing.T) { theRunLeaseOutlivesTheRunRow(t, config) })
}

type leaseAtFinish struct {
	*store.Store
	d    *fakeDaemon
	live chan int
}

func (s leaseAtFinish) FinishRun(ctx context.Context, runID, status, errMsg string) error {
	live := 0
	if l := s.d.lease(runID); l != nil {
		select {
		case <-l.done:
		default:
			live = 1
		}
	}
	s.live <- live
	return s.Store.FinishRun(ctx, runID, status, errMsg)
}

func theRunLeaseOutlivesTheRunRow(t *testing.T, config configFn) {
	st, d := openStore(t), newFakeDaemon()
	cfg := config(st, "run-leased", map[string]string{"env": "prod"}, (&fakeExec{}).run)
	at := leaseAtFinish{Store: st, d: d, live: make(chan int, 1)}
	cfg.State, cfg.Admission = at, &Admission{Daemon: d}
	if _, err := Execute(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if live := <-at.live; live != 1 {
		t.Error("the run lease ended before the run row went terminal")
	}
	if l := d.lease("run-leased"); l == nil {
		t.Fatal("Execute took no run lease")
	}
	if live, _, _ := d.counts(); live != 0 {
		t.Errorf("%d leases live after Execute, want the run lease released", live)
	}
}

type refusingFinish struct{ *store.Store }

func (refusingFinish) FinishRun(context.Context, string, string, string) error {
	return errors.New("disk full")
}

func anUnrecordedFinishFailsTheRun(t *testing.T, config configFn) {
	st := openStore(t)
	cfg := config(st, "run-unrecorded", map[string]string{"env": "prod"}, (&fakeExec{}).run)
	cfg.State = refusingFinish{st}
	res, err := Execute(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "failed" || res.Err == nil || !strings.Contains(res.Err.Error(), "disk full") {
		t.Errorf("status=%s err=%v, want failed naming the lost write", res.Status, res.Err)
	}
}

type configFn func(st *store.Store, runID string, args map[string]string, exec Executor) Config

func runsThePlannedNodes(t *testing.T, config configFn) {
	st := openStore(t)
	fx := &fakeExec{bodies: map[string]Executor{"fail": func(context.Context, *NodeRun) Result {
		return Result{Outcome: sparkwing.Failed, Err: errors.New("boom")}
	}}}
	res, err := Execute(t.Context(), config(st, "run-e2e", map[string]string{"env": "prod"}, fx.run))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "failed" || res.Err == nil || res.Err.Error() != "nodes failed: [fail]" {
		t.Errorf("status=%s err=%v, want failed naming only fail", res.Status, res.Err)
	}
	run, err := st.GetRun(t.Context(), "run-e2e")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" || run.FinishedAt == nil || run.LastHeartbeatAt == nil || run.GitSHA != "4f2c1e0" || run.TriggerSource != "manual" {
		t.Errorf("run row status=%s finished=%v heartbeat=%v sha=%s trigger=%s", run.Status, run.FinishedAt, run.LastHeartbeatAt, run.GitSHA, run.TriggerSource)
	}
	if run.Args["env"] != "prod" || run.Args["count"] != "3" {
		t.Errorf("run args = %v, want env from the invocation and count from its default", run.Args)
	}
	var snap nodeproto.PlanDocOld
	if err := json.Unmarshal(run.PlanSnapshot, &snap); err != nil || snap.RunID != "run-e2e" || len(snap.Nodes) == 0 {
		t.Fatalf("plan snapshot = %s (err %v)", run.PlanSnapshot, err)
	}
	for _, n := range snap.Nodes {
		row, err := st.GetNode(t.Context(), "run-e2e", n.ID)
		if err != nil || row.Status != "done" {
			t.Errorf("node %s row = %+v (err %v), want done", n.ID, row, err)
		}
	}
	if !fx.ran("fail-recover") {
		t.Error("fail's recovery node did not run")
	}
}

func aPlanFailureFinishesTheRunRow(t *testing.T, config configFn) {
	st := openStore(t)
	res, err := Execute(t.Context(), config(st, "run-plan-fail", map[string]string{"env": "prod", "fail-plan": "true"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	var perr *nodeproto.Error
	if res.Status != "failed" || !errors.As(res.Err, &perr) || perr.Code != nodeproto.CodePlanFailed {
		t.Errorf("status=%s err=%v, want failed with plan_failed", res.Status, res.Err)
	}
	run, err := st.GetRun(t.Context(), "run-plan-fail")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" || run.FinishedAt == nil || !strings.Contains(run.Error, "plan refused") {
		t.Errorf("run row status=%q finished=%v error=%q, want failed with the plan's error", run.Status, run.FinishedAt, run.Error)
	}
}

func anUnknownPipelineWritesNoRun(t *testing.T, config configFn) {
	st := openStore(t)
	cfg := config(st, "run-unknown", nil, nil)
	cfg.Pipeline = "ghost"
	_, err := Execute(t.Context(), cfg)
	var perr *nodeproto.Error
	if !errors.As(err, &perr) || perr.Code != nodeproto.CodeUnknownPipeline {
		t.Fatalf("err = %v, want unknown_pipeline", err)
	}
	if _, err := st.GetRun(t.Context(), "run-unknown"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("run row lookup err = %v, want not found", err)
	}
}
