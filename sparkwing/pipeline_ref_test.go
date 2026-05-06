package sparkwing

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// stubResolver is a test double for PipelineResolver. Lets the test
// control the (runID, data, err) triple the Get path observes.
type stubResolver struct {
	lastPipeline string
	lastNodeID   string
	lastMaxAge   time.Duration

	runID string
	data  []byte
	err   error
}

func (s *stubResolver) resolve(_ context.Context, pipeline, nodeID string, maxAge time.Duration) (*ResolvedPipelineRef, error) {
	s.lastPipeline = pipeline
	s.lastNodeID = nodeID
	s.lastMaxAge = maxAge
	if s.err != nil {
		return nil, s.err
	}
	return &ResolvedPipelineRef{RunID: s.runID, Data: s.data}, nil
}

type buildOut struct {
	Digest string `json:"digest"`
	Tag    string `json:"tag"`
}

// TestRefToLastRun_Get_UnmarshalsData exercises the happy path: the
// resolver returns JSON, Get unmarshals into the typed parameter.
func TestRefToLastRun_Get_UnmarshalsData(t *testing.T) {
	payload, _ := json.Marshal(buildOut{Digest: "sha256:abc", Tag: "v1.2.3"})
	r := &stubResolver{runID: "run-xyz", data: payload}
	ctx := WithPipelineResolver(context.Background(), r)

	ref := RefToLastRun[buildOut]("build", "artifact", MaxAge(1*time.Hour))
	got := ref.Get(ctx)

	if got.Digest != "sha256:abc" || got.Tag != "v1.2.3" {
		t.Fatalf("wrong output: %+v", got)
	}
	if r.lastPipeline != "build" || r.lastNodeID != "artifact" || r.lastMaxAge != time.Hour {
		t.Fatalf("resolver called with wrong args: %+v", r)
	}
}

// TestRefToLastRun_Get_PanicsWithoutResolver makes the "called outside
// the orchestrator" footgun loud.
func TestRefToLastRun_Get_PanicsWithoutResolver(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "pipeline resolver") {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	ref := RefToLastRun[buildOut]("build", "artifact")
	ref.Get(context.Background())
}

// TestRefToLastRun_Get_PanicsOnResolverError surfaces the resolver's
// error via a helpful panic. Authors see "no run within X" rather
// than a vague nil deref.
func TestRefToLastRun_Get_PanicsOnResolverError(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "no matching run") {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	r := &stubResolver{err: errors.New("no matching run")}
	ctx := WithPipelineResolver(context.Background(), r)
	ref := RefToLastRun[buildOut]("build", "artifact")
	ref.Get(ctx)
}

// TestRefToLastRun_Get_EmptyDataProducesZeroValue: a resolver that
// returns nil/empty bytes gives the caller the zero value rather
// than panicking in json.Unmarshal. Matches how the in-run path
// treats "no upstream output yet" elsewhere.
func TestRefToLastRun_Get_EmptyDataProducesZeroValue(t *testing.T) {
	r := &stubResolver{runID: "run-empty", data: nil}
	ctx := WithPipelineResolver(context.Background(), r)
	ref := RefToLastRun[buildOut]("build", "artifact")
	got := ref.Get(ctx)
	if got.Digest != "" || got.Tag != "" {
		t.Fatalf("expected zero value, got %+v", got)
	}
}

// TestRefToLastRun_NoOptions: MaxAge defaults to 0 when the caller
// skips the option, and Pipeline / NodeID round-trip onto the Ref.
func TestRefToLastRun_NoOptions(t *testing.T) {
	ref := RefToLastRun[buildOut]("build", "artifact")
	if ref.MaxAge != 0 {
		t.Fatalf("MaxAge: %v", ref.MaxAge)
	}
	if ref.Pipeline != "build" || ref.NodeID != "artifact" {
		t.Fatalf("fields: %+v", ref)
	}
}

// TestCollectCrossPipelineRefs_DiscoversFieldByShape: the audit
// helper should find Ref[T] fields whose Pipeline is non-empty
// (cross-pipeline routing) and skip in-run refs (Pipeline empty).
func TestCollectCrossPipelineRefs_DiscoversFieldByShape(t *testing.T) {
	type Job struct {
		Build    Ref[buildOut]
		NotARef  string
		InRun    Ref[buildOut] // Pipeline=="" -> in-run, excluded
		Unfilled Ref[buildOut] // empty everywhere, excluded
	}
	job := &Job{
		Build: RefToLastRun[buildOut]("build", "artifact", MaxAge(1*time.Hour)),
		InRun: Ref[buildOut]{NodeID: "sibling"},
	}
	pairs := collectCrossPipelineRefs(job)
	if len(pairs) != 1 {
		t.Fatalf("got %d pairs: %+v", len(pairs), pairs)
	}
	if pairs[0].Pipeline != "build" || pairs[0].NodeID != "artifact" {
		t.Fatalf("pair: %+v", pairs[0])
	}
}

// TestPipelineResolverFunc_AdaptsPlainFunction gives orchestrator
// wiring a one-liner way to pass a closure as a PipelineResolver.
func TestPipelineResolverFunc_AdaptsPlainFunction(t *testing.T) {
	called := false
	fn := PipelineResolverFunc(func(_ context.Context, pipeline, nodeID string, maxAge time.Duration) (*ResolvedPipelineRef, error) {
		called = true
		return &ResolvedPipelineRef{RunID: "fn-run", Data: []byte(`{"digest":"z","tag":"z"}`)}, nil
	})
	ctx := WithPipelineResolver(context.Background(), fn)
	got := RefToLastRun[buildOut]("x", "y").Get(ctx)
	if !called || got.Digest != "z" {
		t.Fatalf("func resolver not invoked properly: called=%v got=%+v", called, got)
	}
}
