package nodehost_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type localChildren struct {
	mu      sync.Mutex
	parents []nodehost.ChildParent
	runs    map[string]*store.Run
	queued  bool
}

func (l *localChildren) Queued(_ context.Context, _ string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.queued, nil
}

func (l *localChildren) StartChild(_ context.Context, parent nodehost.ChildParent, req nodehost.ChildRequest) (string, error) {
	if req.Pipeline == "self" {
		return "", errors.Join(nodehost.ErrChildCycle, errors.New("cycle: self would re-enter itself"))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.parents = append(l.parents, parent)
	return "child-1", nil
}

func (l *localChildren) GetRun(_ context.Context, runID string) (*store.Run, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r, ok := l.runs[runID]; ok {
		return r, nil
	}
	return nil, store.ErrNotFound
}

func TestLocalChildrenStartAndAwaitWithoutAController(t *testing.T) {
	s, logs := start(t)
	st := localRuns()
	st.nodes["child-1/build"] = &store.Node{NodeID: "build", Status: "done", Outcome: "success"}
	st.outputs["child-1/build"] = `{"tag":"v2"}`
	kids := &localChildren{runs: map[string]*store.Run{}, queued: true}
	var holds, resumes int
	bearer := admit(t, s, nodehost.Node{
		RunID: "run-1", NodeID: "deploy", Resolver: &fakeResolver{}, Masker: secrets.NewMasker(),
		Local: st, Children: kids,
		Hold: func() func() { holds++; return func() { resumes++ } },
	})

	if got := ask(t, s, http.MethodGet, "/node/v1/children/child-1/await?wait_ms=0", bearer, ""); got.status != http.StatusNotFound || got.body["code"] != "unknown_child" {
		t.Fatalf("await before start: status %d body %v", got.status, got.body)
	}
	if got := ask(t, s, http.MethodPost, "/node/v1/children", bearer, `{"pipeline":"self"}`); got.status != http.StatusConflict || got.body["code"] != "cycle" {
		t.Fatalf("cycle: status %d body %v", got.status, got.body)
	}
	got := ask(t, s, http.MethodPost, "/node/v1/children", bearer, `{"pipeline":"build"}`)
	if got.status != http.StatusCreated || got.body["run_id"] != "child-1" {
		t.Fatalf("start: status %d body %v", got.status, got.body)
	}
	if want := (nodehost.ChildParent{RunID: "run-1", NodeID: "deploy"}); len(kids.parents) != 1 || kids.parents[0] != want {
		t.Fatalf("parents = %v, want %v: the child attaches to what that node holds", kids.parents, want)
	}

	if got := ask(t, s, http.MethodGet, "/node/v1/children/child-1/await?wait_ms=300&node=build", bearer, ""); got.status != http.StatusOK || got.body["status"] != "pending" {
		t.Fatalf("queued child: status %d body %v", got.status, got.body)
	}
	if holds != 1 || resumes != 1 {
		t.Fatalf("holds=%d resumes=%d: a child waiting in plan admission must hold the parent's wall timeout, then let go", holds, resumes)
	}
	kids.mu.Lock()
	kids.runs["child-1"] = &store.Run{ID: "child-1", Status: "success"}
	kids.mu.Unlock()
	got = ask(t, s, http.MethodGet, "/node/v1/children/child-1/await?wait_ms=1000&node=build", bearer, "")
	if out, _ := got.body["output"].(map[string]any); got.status != http.StatusOK || got.body["status"] != "success" || out["tag"] != "v2" {
		t.Fatalf("finished child: status %d body %v", got.status, got.body)
	}
	if got := ask(t, s, http.MethodGet, "/node/v1/children/child-1/await?wait_ms=0&node=nope", bearer, ""); got.status != http.StatusNotFound || got.body["code"] != "unknown_node" {
		t.Fatalf("unknown node: status %d body %v", got.status, got.body)
	}
	noLeak(t, logs)
}
