//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestClosures(t *testing.T) {
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
	marker := t.TempDir()
	args := map[string]string{"marker": marker, "hooks": "true"}
	logs := &memLogs{records: map[string][]sparkwing.LogRecord{}}
	attempts := &Attempts{
		Routes: srv, Resolver: fixtureResolver{}, Masker: secrets.NewMasker(),
		OpenLog: func(_ context.Context, _, node string) (NodeLog, error) { return memLog{logs: logs, node: node}, nil },
	}
	cfg := Config{
		Session:  SessionConfig{Entry: bin, Dir: t.TempDir(), Grace: 2 * time.Second},
		Pipeline: "attempts", Args: args,
		Run:   nodeproto.RunContext{RunID: "run-hooks"},
		State: openStore(t), Logger: slog.New(slog.DiscardHandler), Heartbeat: time.Hour,
		Attempts: attempts,
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	res, err := Execute(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	trace := func(node string) string {
		data, _ := os.ReadFile(filepath.Join(marker, node))
		return string(data)
	}
	row := func(node string) *store.Node {
		r, err := cfg.State.GetNode(t.Context(), "run-hooks", node)
		if err != nil {
			t.Fatalf("node %s: %v", node, err)
		}
		return r
	}

	t.Run("ASkipIfThatHoldsSkipsTheNodeBeforeItStarts", func(t *testing.T) {
		r := row("skipped")
		if r.Outcome != string(sparkwing.Skipped) || r.Error != "SkipIf predicate 1 returned true" || r.StartedAt != nil {
			t.Errorf("skipped row outcome=%q error=%q started=%v, want skipped by predicate 1 and never started", r.Outcome, r.Error, r.StartedAt)
		}
		if got := trace("skipped"); got != "" {
			t.Errorf("the skipped node's body ran: %q", got)
		}
	})

	t.Run("ASkippedApprovalGateNeverAsks", func(t *testing.T) {
		if r := row("skipped-gate"); r.Outcome != string(sparkwing.Skipped) || r.StartedAt != nil {
			t.Errorf("gate row outcome=%q started=%v, want skipped and never started", r.Outcome, r.StartedAt)
		}
		if appr, err := cfg.State.GetApproval(t.Context(), "run-hooks", "skipped-gate"); err == nil {
			t.Errorf("the skipped gate created an approval: %+v", appr)
		}
	})

	t.Run("ASkipIfThatFailsOrOverrunsDoesNotSkip", func(t *testing.T) {
		if res.Nodes["kept"].Outcome != sparkwing.Success || trace("kept") != "body\n" {
			t.Errorf("kept = %+v, trace %q, want its body run once", res.Nodes["kept"], trace("kept"))
		}
		log := logs.text("kept")
		for _, want := range []string{"SkipIf predicate 0 failed", "SkipIf predicate 1 exceeded 300ms budget"} {
			if !strings.Contains(log, want) {
				t.Errorf("kept log lacks %q:\n%s", want, log)
			}
		}
	})

	t.Run("AFailingVerifyFailsTheAttemptAndAfterRunSeesIt", func(t *testing.T) {
		r := row("unverified")
		if r.Outcome != string(sparkwing.Failed) || r.FailureReason != store.FailureVerify || r.Error != "verify: health check: 503" {
			t.Errorf("unverified row outcome=%q reason=%q error=%q, want failed with verify", r.Outcome, r.FailureReason, r.Error)
		}
		if got := trace("unverified"); got != "body\nafter:verify: health check: 503\n" {
			t.Errorf("unverified trace = %q, want the body, then after_run with the verify error", got)
		}
	})

	t.Run("AFailingVerifyNeverFansOut", func(t *testing.T) {
		r := row("unverified-source")
		if r.Outcome != string(sparkwing.Failed) || r.FailureReason != store.FailureVerify {
			t.Errorf("source row outcome=%q reason=%q error=%q, want failed with verify", r.Outcome, r.FailureReason, r.Error)
		}
		if got := trace("unverified-source"); got != "" {
			t.Errorf("source trace = %q, want the generator never run", got)
		}
		if _, err := cfg.State.GetNode(t.Context(), "run-hooks", "unverified-a"); err == nil {
			t.Error("a node was generated from a source whose verify failed")
		}
	})

	t.Run("AFailingBeforeRunEndsTheNodeWithoutItsBodyOrARetry", func(t *testing.T) {
		r := res.Nodes["unready"]
		if r.Outcome != sparkwing.Failed || !r.Final || r.Err == nil || r.Err.Error() != "BeforeRun hook 0: not ready" {
			t.Errorf("unready = %+v, want a final failure from before_run", r)
		}
		if got := trace("unready"); got != "before\n" {
			t.Errorf("unready trace = %q, want one before_run and neither body, retry nor after_run", got)
		}
	})

	t.Run("AfterRunRunsOnlyInTheLastOfThreeAttempts", func(t *testing.T) {
		if got := trace("flaky"); got != "before\nbody\nbody\nbody\nafter:step \"run\": still broken\n" {
			t.Errorf("flaky trace = %q, want before_run once, three bodies, then after_run once with the error", got)
		}
	})

	t.Run("ATimedOutAttemptStillRunsAfterRun", func(t *testing.T) {
		r := row("slow")
		if r.Outcome != string(sparkwing.Failed) || r.FailureReason != store.FailureTimeout {
			t.Errorf("slow row outcome=%q reason=%q error=%q, want failed with timeout", r.Outcome, r.FailureReason, r.Error)
		}
		if got := trace("slow"); got != "body\nafter:timeout exceeded (500ms): context deadline exceeded\n" {
			t.Errorf("slow trace = %q, want the body, then after_run with the timeout error", got)
		}
	})

	t.Run("BeforeRunRunsOnceAcrossRetriesAndAfterRunInTheLast", func(t *testing.T) {
		run, err := cfg.State.GetRun(t.Context(), "run-hooks")
		if err != nil {
			t.Fatal(err)
		}
		var doc nodeproto.PlanDoc
		if err := json.Unmarshal(run.PlanSnapshot, &doc); err != nil {
			t.Fatal(err)
		}
		var hooked nodeproto.PlanNode
		for _, n := range doc.Nodes {
			if n.ID == "hooked" {
				hooked = n
			}
		}
		if err := os.Remove(filepath.Join(marker, "hooked")); err != nil {
			t.Fatal(err)
		}
		r := &Run{ID: "run-hooks", Doc: doc, Pipeline: "attempts", Args: args, Context: cfg.Run, State: cfg.State, Logger: cfg.Logger}
		exec := attempts.executor(&cfg)
		var retried []Result
		first := exec(t.Context(), &NodeRun{Run: r, Plan: hooked, Attempt: 1, Retried: func(res Result) bool {
			retried = append(retried, res)
			return true
		}})
		second := exec(t.Context(), &NodeRun{Run: r, Plan: hooked, Attempt: 2})
		if first.Outcome != sparkwing.Failed || second.Outcome != sparkwing.Success || len(retried) != 1 {
			t.Errorf("attempts = %+v then %+v (retry asked %d times), want a failure then a success", first, second, len(retried))
		}
		if got := trace("hooked"); got != "before\nbody\nbody\nverify\nafter:<nil>\n" {
			t.Errorf("hooked trace = %q, want before_run once, verify after the passing body and after_run in attempt 2 only", got)
		}
	})
}
