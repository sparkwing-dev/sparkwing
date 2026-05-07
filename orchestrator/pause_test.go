package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// pauseTestHarness opens one store + one Backends bundle shared by
// both the orchestrator goroutine and the test's polling/releasing
// goroutine, avoiding a second store.Open racing with the first.
type pauseTestHarness struct {
	t        *testing.T
	paths    orchestrator.Paths
	st       *store.Store
	backends orchestrator.Backends
}

func newPauseHarness(t *testing.T) *pauseTestHarness {
	t.Helper()
	p := newPaths(t)
	if err := p.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return &pauseTestHarness{
		t:        t,
		paths:    p,
		st:       st,
		backends: orchestrator.LocalBackends(p, st),
	}
}

func (h *pauseTestHarness) waitForPause(nodeID string) *store.DebugPause {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		runs, _ := h.st.ListRuns(context.Background(), store.RunFilter{Limit: 10})
		for _, r := range runs {
			ps, _ := h.st.ListDebugPauses(context.Background(), r.ID)
			for _, pp := range ps {
				if pp.NodeID == nodeID && pp.ReleasedAt == nil {
					return pp
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	h.t.Fatalf("no open pause for node %q within deadline", nodeID)
	return nil
}

func (h *pauseTestHarness) release(runID, nodeID string) {
	h.t.Helper()
	if err := h.st.ReleaseDebugPause(context.Background(), runID, nodeID,
		"test", store.PauseReleaseManual); err != nil {
		h.t.Fatalf("release: %v", err)
	}
}

func init() {
	register("orch-pause-ok", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &okPipe{} })
	register("orch-pause-fanout", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &fanOutOK{} })
	register("orch-pause-fail", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &failPipe{} })
}

// TestPause_BeforeRun_HoldsUntilReleased pauses a single-node pipeline
// and then releases it from a side goroutine. The run must finish
// success and the store must carry a released pause row.
func TestPause_BeforeRun_HoldsUntilReleased(t *testing.T) {
	h := newPauseHarness(t)
	opts := orchestrator.Options{
		Pipeline: "orch-pause-ok",
		Debug: orchestrator.DebugDirectives{
			PauseBefore: []string{"orch-pause-ok"},
		},
	}
	type result struct {
		res *orchestrator.Result
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, err := orchestrator.Run(context.Background(), h.backends, opts)
		done <- result{r, err}
	}()

	st := h.waitForPause("orch-pause-ok")
	if st.Reason != store.PauseReasonBefore {
		t.Fatalf("pause reason = %q, want %q", st.Reason, store.PauseReasonBefore)
	}
	h.release(st.RunID, "orch-pause-ok")

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("run err: %v", r.err)
		}
		if r.res.Status != "success" {
			t.Fatalf("status = %q, want success", r.res.Status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not complete within 10s after release")
	}
}

// TestPause_BeforeRun_Timeout verifies SPARKWING_PAUSE_TIMEOUT fires.
func TestPause_BeforeRun_Timeout(t *testing.T) {
	t.Setenv("SPARKWING_PAUSE_TIMEOUT", "500ms")
	h := newPauseHarness(t)
	opts := orchestrator.Options{
		Pipeline: "orch-pause-ok",
		Debug: orchestrator.DebugDirectives{
			PauseBefore: []string{"orch-pause-ok"},
		},
	}
	res, err := orchestrator.Run(context.Background(), h.backends, opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q, want success (timeout releases and run continues)", res.Status)
	}
	ps, err := h.st.ListDebugPauses(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("list pauses: %v", err)
	}
	if len(ps) != 1 {
		t.Fatalf("got %d pauses, want 1", len(ps))
	}
	if ps[0].ReleaseKind != store.PauseReleaseTimeout {
		t.Fatalf("release_kind = %q, want %q", ps[0].ReleaseKind, store.PauseReleaseTimeout)
	}
}

// TestPause_After_HoldsAfterSuccess ensures pause-after fires when
// the node completes successfully.
func TestPause_After_HoldsAfterSuccess(t *testing.T) {
	h := newPauseHarness(t)
	opts := orchestrator.Options{
		Pipeline: "orch-pause-ok",
		Debug: orchestrator.DebugDirectives{
			PauseAfter: []string{"orch-pause-ok"},
		},
	}
	type result struct {
		res *orchestrator.Result
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, err := orchestrator.Run(context.Background(), h.backends, opts)
		done <- result{r, err}
	}()
	st := h.waitForPause("orch-pause-ok")
	if st.Reason != store.PauseReasonAfter {
		t.Fatalf("pause reason = %q, want %q", st.Reason, store.PauseReasonAfter)
	}
	h.release(st.RunID, "orch-pause-ok")
	select {
	case r := <-done:
		if r.res.Status != "success" {
			t.Fatalf("status = %q, want success", r.res.Status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run hung")
	}
}

// TestPause_OnFailure_PausesOnError verifies that a Run-errored node
// triggers --pause-on-failure. (REG-013b cross-check; the wire-up
// landed together with 013a so the test lives here.)
func TestPause_OnFailure_PausesOnError(t *testing.T) {
	h := newPauseHarness(t)
	opts := orchestrator.Options{
		Pipeline: "orch-pause-fail",
		Debug: orchestrator.DebugDirectives{
			PauseOnFailure: true,
		},
	}
	type result struct {
		res *orchestrator.Result
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, err := orchestrator.Run(context.Background(), h.backends, opts)
		done <- result{r, err}
	}()
	st := h.waitForPause("orch-pause-fail")
	if st.Reason != store.PauseReasonOnFailure {
		t.Fatalf("pause reason = %q, want %q", st.Reason, store.PauseReasonOnFailure)
	}
	h.release(st.RunID, "orch-pause-fail")
	select {
	case r := <-done:
		if r.res.Status != "failed" {
			t.Fatalf("status = %q, want failed", r.res.Status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run hung after release")
	}
}

// Compile-time: DebugDirectives is empty by default so production
// Options don't accidentally pick up pause flags.
var _ = sparkwing.Paused

// TestPause_OnFailure_SkipsOnSuccess ensures --pause-on-failure does
// NOT pause a node that returned without error. Guards against
// accidentally coupling pause-after semantics into pause-on-failure.
func TestPause_OnFailure_SkipsOnSuccess(t *testing.T) {
	h := newPauseHarness(t)
	opts := orchestrator.Options{
		Pipeline: "orch-pause-ok",
		Debug: orchestrator.DebugDirectives{
			PauseOnFailure: true,
		},
	}
	res, err := orchestrator.Run(context.Background(), h.backends, opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q, want success (pause-on-failure must not hold a successful node)",
			res.Status)
	}
	ps, err := h.st.ListDebugPauses(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("list pauses: %v", err)
	}
	if len(ps) != 0 {
		t.Fatalf("got %d pauses, want 0 on successful run", len(ps))
	}
}

// TestPause_OnFailure_SkipsOnCancelled ensures a cancelled node (its
// upstream failed) does NOT get held by --pause-on-failure. The
// dispatcher's markCancelled path is distinct from a Run-errored
// Failed outcome.
func TestPause_OnFailure_SkipsOnCancelled(t *testing.T) {
	h := newPauseHarness(t)
	// orch-middle-fails: a -> b(fail) -> c. c should be cancelled, not
	// paused. b's Run errors, so b *should* pause; release it so the
	// run completes and we can assert c has no pause row.
	opts := orchestrator.Options{
		Pipeline: "orch-middle-fails",
		Debug: orchestrator.DebugDirectives{
			PauseOnFailure: true,
		},
	}
	type result struct {
		res *orchestrator.Result
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, err := orchestrator.Run(context.Background(), h.backends, opts)
		done <- result{r, err}
	}()
	st := h.waitForPause("b")
	if st.Reason != store.PauseReasonOnFailure {
		t.Fatalf("pause reason = %q, want %q", st.Reason, store.PauseReasonOnFailure)
	}
	h.release(st.RunID, "b")
	select {
	case r := <-done:
		// c must be cancelled, never paused.
		ps, _ := h.st.ListDebugPauses(context.Background(), r.res.RunID)
		for _, p := range ps {
			if p.NodeID == "c" {
				t.Fatalf("c got a pause row (%+v); cancelled nodes must not pause", p)
			}
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run hung")
	}
}
