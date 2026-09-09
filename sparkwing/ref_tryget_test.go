package sparkwing

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRefTryGet_CrossPipelineBootstrapReturnsFalse(t *testing.T) {
	r := &stubResolver{err: errors.New("get node build/artifact output: not found")}
	ctx := context.WithValue(context.Background(), keyPipelineResolver, r)

	got, ok := RefToLastRun[buildOut]("build", "artifact").TryGet(ctx)
	if ok {
		t.Fatal("expected ok=false on the bootstrap run")
	}
	if got.Digest != "" || got.Tag != "" {
		t.Fatalf("expected the zero value, got %+v", got)
	}
}

func TestRefTryGet_CrossPipelineReturnsValue(t *testing.T) {
	payload, err := json.Marshal(buildOut{Digest: "sha256:abc", Tag: "v1.2.3"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	r := &stubResolver{runID: "run-xyz", data: payload}
	ctx := context.WithValue(context.Background(), keyPipelineResolver, r)

	got, ok := RefToLastRun[buildOut]("build", "artifact").TryGet(ctx)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got.Digest != "sha256:abc" || got.Tag != "v1.2.3" {
		t.Fatalf("wrong output: %+v", got)
	}
}

// A run that stored nothing is not a value the caller can compare against,
// so TryGet reports it as absent even though Get renders it as the zero T.
func TestRefTryGet_CrossPipelineEmptyOutputReturnsFalse(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("null")} {
		r := &stubResolver{runID: "run-empty", data: data}
		ctx := context.WithValue(context.Background(), keyPipelineResolver, r)

		if _, ok := RefToLastRun[buildOut]("build", "artifact").TryGet(ctx); ok {
			t.Fatalf("data %q: expected ok=false when the run stored no output", data)
		}
	}
}

func TestRefTryGet_CrossPipelineWithoutResolverPanics(t *testing.T) {
	defer func() {
		msg, _ := recover().(string)
		if !strings.Contains(msg, "pipeline resolver") {
			t.Fatalf("unexpected panic value: %q", msg)
		}
	}()
	RefToLastRun[buildOut]("build", "artifact").TryGet(context.Background())
	t.Fatal("expected a panic: no pipeline author can install a resolver at runtime")
}

func TestRefTryGet_CrossPipelineUnmarshalFailurePanics(t *testing.T) {
	defer func() {
		msg, _ := recover().(string)
		if !strings.Contains(msg, "unmarshal build/artifact output") {
			t.Fatalf("unexpected panic value: %q", msg)
		}
	}()
	r := &stubResolver{runID: "run-bad", data: []byte(`["not","an","object"]`)}
	ctx := context.WithValue(context.Background(), keyPipelineResolver, r)

	RefToLastRun[buildOut]("build", "artifact").TryGet(ctx)
	t.Fatal("expected a panic: stored output that does not fit T is a programmer mistake")
}

func TestRefTryGet_InRunIncompleteNodeReturnsFalse(t *testing.T) {
	resolve := func(string) ([]byte, bool) { return nil, false }
	ctx := context.WithValue(context.Background(), keyJSONRefResolver, resolve)

	if _, ok := (Ref[buildOut]{NodeID: "build"}).TryGet(ctx); ok {
		t.Fatal("expected ok=false for a node that has not completed")
	}
}

func TestRefTryGet_InRunReturnsValue(t *testing.T) {
	resolve := func(string) ([]byte, bool) { return []byte(`{"digest":"d","tag":"t"}`), true }
	ctx := context.WithValue(context.Background(), keyJSONRefResolver, resolve)

	got, ok := Ref[buildOut]{NodeID: "build"}.TryGet(ctx)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got.Digest != "d" || got.Tag != "t" {
		t.Fatalf("wrong output: %+v", got)
	}
}

func TestRefTryGet_InRunWithoutResolverPanics(t *testing.T) {
	defer func() {
		msg, _ := recover().(string)
		if !strings.Contains(msg, "without a resolver in context") {
			t.Fatalf("unexpected panic value: %q", msg)
		}
	}()
	Ref[buildOut]{NodeID: "build"}.TryGet(context.Background())
	t.Fatal("expected a panic")
}

// Get's panic value stays a string: the SDK's own tests type-assert it.
func TestRefGet_InRunIncompleteNodePanicsWithString(t *testing.T) {
	defer func() {
		msg, _ := recover().(string)
		if !strings.Contains(msg, `node "build" has not completed`) {
			t.Fatalf("unexpected panic value: %q", msg)
		}
	}()
	resolve := func(string) ([]byte, bool) { return nil, false }
	ctx := context.WithValue(context.Background(), keyJSONRefResolver, resolve)

	Ref[buildOut]{NodeID: "build"}.Get(ctx)
	t.Fatal("expected a panic")
}
