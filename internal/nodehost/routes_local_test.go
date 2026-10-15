package nodehost_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type localStore struct {
	nodes   map[string]*store.Node
	outputs map[string]string
	latest  *store.Run
}

func (l *localStore) GetNode(_ context.Context, runID, nodeID string) (*store.Node, error) {
	if n, ok := l.nodes[runID+"/"+nodeID]; ok {
		return n, nil
	}
	return nil, store.ErrNotFound
}

func (l *localStore) GetNodeOutput(_ context.Context, runID, nodeID string) ([]byte, error) {
	if _, ok := l.nodes[runID+"/"+nodeID]; !ok {
		return nil, store.ErrNotFound
	}
	return []byte(l.outputs[runID+"/"+nodeID]), nil
}

func (l *localStore) GetLatestRun(_ context.Context, _ string, _ []string, _ time.Duration) (*store.Run, error) {
	if l.latest == nil {
		return nil, store.ErrNotFound
	}
	return l.latest, nil
}

func localRuns() *localStore {
	node := func(id, status, outcome string, deps ...string) *store.Node {
		return &store.Node{NodeID: id, Status: status, Outcome: outcome, Deps: deps}
	}
	return &localStore{
		nodes: map[string]*store.Node{
			"run-1/build":   node("build", "done", "success"),
			"run-1/test":    node("test", "done", "success", "build"),
			"run-1/deploy":  node("deploy", "running", "", "absent", "test", "lint", "flaky", "slow", "memo"),
			"run-1/memo":    node("memo", "done", "memoized"),
			"run-1/lint":    node("lint", "done", "success"),
			"run-1/flaky":   node("flaky", "done", "failed"),
			"run-1/slow":    node("slow", "running", ""),
			"run-1/sibling": node("sibling", "done", "success"),
			"run-0/build":   node("build", "done", "success"),
		},
		outputs: map[string]string{"run-1/build": `{"tag":"v1"}`, "run-1/memo": `"reused"`, "run-0/build": `"old"`},
	}
}

func TestLocalOutputsServeADependencysOutputWithoutAController(t *testing.T) {
	cases := []struct {
		name, path, code string
		status           int
		output           any
	}{
		{name: "transitive dependency", path: outputPath("build"), status: http.StatusOK, output: map[string]any{"tag": "v1"}},
		{name: "memo hit", path: outputPath("memo"), status: http.StatusOK, output: "reused"},
		{name: "no output recorded", path: outputPath("lint"), status: http.StatusOK, output: nil},
		{name: "not a dependency", path: outputPath("sibling"), status: http.StatusForbidden, code: "forbidden"},
		{name: "unknown", path: outputPath("nope"), status: http.StatusNotFound, code: "unknown_job"},
		{name: "running", path: outputPath("slow"), status: http.StatusConflict, code: "conflict"},
		{name: "failed", path: outputPath("flaky"), status: http.StatusNotFound, code: "not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, logs := start(t)
			bearer := admit(t, s, nodehost.Node{
				RunID: "run-1", NodeID: "deploy", Resolver: &fakeResolver{}, Masker: secrets.NewMasker(), Local: localRuns(),
			})
			got := ask(t, s, http.MethodGet, tc.path, bearer, "")
			if got.status != tc.status || (tc.code != "" && failureCode(got) != tc.code) {
				t.Fatalf("status %d body %v", got.status, got.body)
			}
			if tc.code == "" {
				if out, ok := got.body["output"]; !ok || !sameJSON(out, tc.output) {
					t.Fatalf("output %v, want %v", got.body["output"], tc.output)
				}
			}
			noLeak(t, logs)
		})
	}
}

func TestLocalPipelineLatestReadsTheNewestSuccessfulRun(t *testing.T) {
	s, _ := start(t)
	st := localRuns()
	bearer := admit(t, s, nodehost.Node{
		RunID: "run-1", NodeID: "deploy", Resolver: &fakeResolver{}, Masker: secrets.NewMasker(), Local: st,
		Refs: []nodeproto.PipelineReference{{Pipeline: "other", Job: "build"}},
	})
	if got := ask(t, s, http.MethodGet, "/node/v1/pipelines/other/latest?job=build", bearer, ""); got.status != http.StatusNotFound || failureCode(got) != "not_found" {
		t.Fatalf("status %d body %v", got.status, got.body)
	}
	st.latest = &store.Run{ID: "run-0"}
	got := ask(t, s, http.MethodGet, "/node/v1/pipelines/other/latest?job=build", bearer, "")
	if got.status != http.StatusOK || got.body["run_id"] != "run-0" || got.body["output"] != "old" {
		t.Fatalf("status %d body %v", got.status, got.body)
	}
}

func TestLocalPipelineLatestRefusesAnUndeclaredReference(t *testing.T) {
	s, logs := start(t)
	st := localRuns()
	st.latest = &store.Run{ID: "run-0"}
	bearer := admit(t, s, nodehost.Node{
		RunID: "run-1", NodeID: "deploy", Resolver: &fakeResolver{}, Masker: secrets.NewMasker(), Local: st,
		Refs: []nodeproto.PipelineReference{{Pipeline: "other", Job: "test"}, {Pipeline: "another", Job: "build"}},
	})
	for _, path := range []string{"/node/v1/pipelines/other/latest?job=build", "/node/v1/pipelines/third/latest?job=build"} {
		got := ask(t, s, http.MethodGet, path, bearer, "")
		if got.status != http.StatusForbidden || failureCode(got) != "forbidden" {
			t.Fatalf("%s: status %d body %v", path, got.status, got.body)
		}
		if _, ok := got.body["output"]; ok {
			t.Fatalf("%s: refusal carries an output: %v", path, got.body)
		}
	}
	if !strings.Contains(logs.String(), "forbidden") {
		t.Fatalf("no audit record of the refusal:\n%s", logs.String())
	}
}

func sameJSON(a, b any) bool {
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok != bok {
		return false
	}
	if !aok {
		return a == b
	}
	if len(am) != len(bm) {
		return false
	}
	for k, v := range am {
		if bm[k] != v {
			return false
		}
	}
	return true
}
