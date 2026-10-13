package nodehost_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestAnAwaitHoldsTheNoProgressTimeout(t *testing.T) {
	s, _ := start(t)
	c := newChildController(t, started("child-1"))
	polling, finish := make(chan struct{}, 1), make(chan struct{})
	release := sync.OnceFunc(func() { close(finish) })
	t.Cleanup(release)
	c.handle("GET /api/v1/runs/run-1/children/child-1", func(w http.ResponseWriter, r *http.Request) {
		select {
		case polling <- struct{}{}:
		default:
		}
		<-finish
		answer(http.StatusOK, map[string]string{"status": "success"})(w, r)
	})
	claim := claimFor(t, c.fakeController)
	plan := nodeproto.PlanNode{ID: "deploy", Modifiers: &nodeproto.PlanModifiers{NoProgressTimeoutMS: 1000}}

	awaiting := func(ctx context.Context, n *nodehost.NodeRun) nodehost.Result {
		node := nodeWithClaim("deploy", claim)
		node.Pause = n.Pause
		bearer, revoke, err := s.Admit(node)
		if err != nil {
			return nodehost.Result{Outcome: sparkwing.Failed, Err: err}
		}
		defer revoke()
		codes := make(chan int, 1)
		openRequest(ctx, s, http.MethodPost, "/node/v1/children", bearer, `{"pipeline":"build"}`, codes)
		if code := <-codes; code >= 300 {
			return nodehost.Result{Outcome: sparkwing.Failed, Err: errors.New(http.StatusText(code))}
		}
		openRequest(ctx, s, http.MethodGet, "/node/v1/children/child-1/await?wait_ms=60000", bearer, "", codes)
		if code := <-codes; code != http.StatusOK {
			return nodehost.Result{Outcome: sparkwing.Failed, Err: errors.New(http.StatusText(code))}
		}
		return nodehost.Result{Outcome: sparkwing.Success}
	}
	done := make(chan nodehost.Result, 1)
	go func() { done <- nodehost.TimeoutLayer(awaiting)(t.Context(), &nodehost.NodeRun{Plan: plan}) }()
	<-polling

	// safety: the silent node starts after the await opened and has the same budget, so its
	// timeout proves the awaiting node sat silent past its own.
	silent := nodehost.TimeoutLayer(func(ctx context.Context, _ *nodehost.NodeRun) nodehost.Result {
		<-ctx.Done()
		return nodehost.Result{Outcome: sparkwing.Failed, Err: ctx.Err()}
	})(t.Context(), &nodehost.NodeRun{Plan: plan})
	if silent.Reason != store.FailureNoProgressTimeout {
		t.Fatalf("a silent node ended %s %q, want no_progress_timeout", silent.Outcome, silent.Reason)
	}
	release()
	if res := <-done; res.Outcome != sparkwing.Success {
		t.Errorf("the awaiting node ended %s %q: %v, want success", res.Outcome, res.Reason, res.Err)
	}
}
