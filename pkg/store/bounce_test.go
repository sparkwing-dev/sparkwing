package store_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func bounceFixture(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "later", Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := st.StartNode(ctx, "run-1", "build"); err != nil {
		t.Fatalf("start node: %v", err)
	}
	return st, ctx
}

func TestNodeBounce_RequestBecomesPendingThenConsumed(t *testing.T) {
	st, ctx := bounceFixture(t)

	b, err := st.RequestNodeBounce(ctx, "run-1", "build", "korey")
	if err != nil {
		t.Fatalf("RequestNodeBounce: %v", err)
	}
	if b.Seq != 1 || b.RequestedBy != "korey" || b.ConsumedAt != nil {
		t.Fatalf("requested row = %+v, want seq 1, requested_by korey, unconsumed", b)
	}

	pending, err := st.PendingNodeBounce(ctx, "run-1", "build")
	if err != nil || pending == nil {
		t.Fatalf("PendingNodeBounce = %v, %v; want the request", pending, err)
	}
	if pending.Seq != b.Seq {
		t.Errorf("pending seq = %d, want %d", pending.Seq, b.Seq)
	}
	other, err := st.PendingNodeBounce(ctx, "run-1", "later")
	if err != nil || other != nil {
		t.Errorf("PendingNodeBounce(later) = %v, %v; a bounce reaches one node only", other, err)
	}

	if err := st.ConsumeNodeBounce(ctx, "run-1", "build", b.Seq, store.BounceBounced); err != nil {
		t.Fatalf("ConsumeNodeBounce: %v", err)
	}
	pending, err = st.PendingNodeBounce(ctx, "run-1", "build")
	if err != nil || pending != nil {
		t.Errorf("PendingNodeBounce after consume = %v, %v; want none", pending, err)
	}

	rows, err := st.ListNodeBounces(ctx, "run-1")
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListNodeBounces = %d rows, %v", len(rows), err)
	}
	if rows[0].Outcome != store.BounceBounced || rows[0].ConsumedAt == nil {
		t.Errorf("consumed row = %+v, want outcome %q and a consumed_at", rows[0], store.BounceBounced)
	}
}

func TestNodeBounce_ConsumeIsIdempotent(t *testing.T) {
	st, ctx := bounceFixture(t)
	b, err := st.RequestNodeBounce(ctx, "run-1", "build", "korey")
	if err != nil {
		t.Fatalf("RequestNodeBounce: %v", err)
	}
	if err := st.ConsumeNodeBounce(ctx, "run-1", "build", b.Seq, store.BounceBounced); err != nil {
		t.Fatalf("consume#1: %v", err)
	}
	if err := st.ConsumeNodeBounce(ctx, "run-1", "build", b.Seq, store.BounceMissed); err != nil {
		t.Fatalf("consume#2: %v", err)
	}
	rows, err := st.ListNodeBounces(ctx, "run-1")
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListNodeBounces = %d rows, %v", len(rows), err)
	}
	if rows[0].Outcome != store.BounceBounced {
		t.Errorf("outcome after re-consume = %q, want the first verdict %q kept",
			rows[0].Outcome, store.BounceBounced)
	}

	err = st.ConsumeNodeBounce(ctx, "run-1", "build", 99, store.BounceBounced)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("consume of an unknown seq = %v, want ErrNotFound", err)
	}
}

func TestNodeBounce_RepeatedRequestsQueueInOrder(t *testing.T) {
	st, ctx := bounceFixture(t)
	first, err := st.RequestNodeBounce(ctx, "run-1", "build", "korey")
	if err != nil {
		t.Fatalf("request#1: %v", err)
	}
	second, err := st.RequestNodeBounce(ctx, "run-1", "build", "korey")
	if err != nil {
		t.Fatalf("request#2: %v", err)
	}
	if second.Seq != first.Seq+1 {
		t.Fatalf("second seq = %d, want %d", second.Seq, first.Seq+1)
	}
	pending, err := st.PendingNodeBounce(ctx, "run-1", "build")
	if err != nil || pending == nil || pending.Seq != first.Seq {
		t.Fatalf("pending = %v (%v), want the older request first", pending, err)
	}
	if err := st.ConsumeNodeBounce(ctx, "run-1", "build", first.Seq, store.BounceBounced); err != nil {
		t.Fatalf("consume#1: %v", err)
	}
	pending, err = st.PendingNodeBounce(ctx, "run-1", "build")
	if err != nil || pending == nil || pending.Seq != second.Seq {
		t.Fatalf("pending after consume = %v (%v), want the second request", pending, err)
	}
}

func TestNodeBounce_AcceptedWhileTheRowSaysRunningWithNoProcess(t *testing.T) {
	st, ctx := bounceFixture(t)
	b, err := st.RequestNodeBounce(ctx, "run-1", "build", "korey")
	if err != nil {
		t.Fatalf("RequestNodeBounce on a running row: %v", err)
	}
	if b.Seq != 1 {
		t.Errorf("seq = %d, want 1", b.Seq)
	}
}

func TestNodeBounce_ConcurrentRequestsDoNotCollide(t *testing.T) {
	st, ctx := bounceFixture(t)

	const requests = 24
	errs := make(chan error, requests)
	seqs := make(chan int64, requests)
	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	for i := range requests {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			b, err := st.RequestNodeBounce(ctx, "run-1", "build", fmt.Sprintf("operator-%d", i))
			if err != nil {
				errs <- err
				return
			}
			seqs <- b.Seq
		}()
	}
	start.Done()
	done.Wait()
	close(errs)
	close(seqs)

	var failures []string
	for err := range errs {
		failures = append(failures, err.Error())
	}
	if len(failures) > 0 {
		t.Fatalf("%d of %d concurrent requests failed; first: %s", len(failures), requests, failures[0])
	}
	seen := map[int64]bool{}
	for seq := range seqs {
		if seen[seq] {
			t.Errorf("sequence %d was handed out twice", seq)
		}
		seen[seq] = true
	}
	rows, err := st.ListNodeBounces(ctx, "run-1")
	if err != nil || len(rows) != requests {
		t.Fatalf("ListNodeBounces = %d rows, %v; want %d", len(rows), err, requests)
	}
}

func TestStartNode_CannotReopenANodeThatAlreadyFinished(t *testing.T) {
	st, ctx := bounceFixture(t)
	if err := st.FinishNode(ctx, "run-1", "build", "success", "", []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("finish node: %v", err)
	}

	if err := st.StartNode(ctx, "run-1", "build"); err != nil {
		t.Fatalf("StartNode on a finished node returned an error, want a silent no-op: %v", err)
	}
	n, err := st.GetNode(ctx, "run-1", "build")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.Status != "done" || n.Outcome != "success" || string(n.Output) != `{"ok":true}` {
		t.Fatalf("node = %+v, want its terminal row untouched", n)
	}

	if err := st.FinishNode(ctx, "run-1", "build", "failed", "second execution", nil); err != nil {
		t.Fatalf("FinishNode: %v", err)
	}
	n, err = st.GetNode(ctx, "run-1", "build")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.Outcome != "success" || n.Error != "" {
		t.Errorf("node = %+v, want the first verdict kept", n)
	}

	before, err := st.GetNode(ctx, "run-1", "later")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.StartNode(ctx, "run-1", "later"); err != nil {
		t.Fatal(err)
	}
	after, err := st.GetNode(ctx, "run-1", "later")
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "running" || after.StartedAt == nil {
		t.Errorf("node = %+v, want it started", after)
	}
	if before.StartedAt != nil && after.StartedAt.Before(*before.StartedAt) {
		t.Errorf("started_at went backwards: %v -> %v", before.StartedAt, after.StartedAt)
	}
}

func TestNodeBounce_RefusesWhatHasNoProcessToKill(t *testing.T) {
	st, ctx := bounceFixture(t)

	if _, err := st.RequestNodeBounce(ctx, "nope", "build", "korey"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown run = %v, want ErrNotFound", err)
	}
	if _, err := st.RequestNodeBounce(ctx, "run-1", "ghost", "korey"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown node = %v, want ErrNotFound", err)
	}
	_, err := st.RequestNodeBounce(ctx, "run-1", "later", "korey")
	if !errors.Is(err, store.ErrNodeNotRunning) {
		t.Errorf("pending node = %v, want ErrNodeNotRunning", err)
	}
	if err != nil && !strings.Contains(err.Error(), "pending") {
		t.Errorf("error %q does not name the status the node is actually in", err)
	}

	if err := st.FinishNode(ctx, "run-1", "build", "success", "", nil); err != nil {
		t.Fatalf("finish node: %v", err)
	}
	if _, err := st.RequestNodeBounce(ctx, "run-1", "build", "korey"); !errors.Is(err, store.ErrNodeNotRunning) {
		t.Errorf("finished node = %v, want ErrNodeNotRunning", err)
	}
	if err := st.FinishRun(ctx, "run-1", "success", ""); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	_, err = st.RequestNodeBounce(ctx, "run-1", "build", "korey")
	if !errors.Is(err, store.ErrRunNotLive) {
		t.Errorf("finished run = %v, want ErrRunNotLive", err)
	}
}
