package sparkwing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRefTryGet_CrossPipelineBootstrapReturnsFalse(t *testing.T) {
	r := &stubResolver{err: fmt.Errorf("get node build/artifact output: not found: %w", ErrRefAbsent)}
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

type refWarnRecorder struct{ lines []string }

func (r *refWarnRecorder) Log(level, msg string) { r.lines = append(r.lines, level+": "+msg) }
func (r *refWarnRecorder) Emit(rec LogRecord)    { r.lines = append(r.lines, rec.Level+": "+rec.Msg) }

func (r *refWarnRecorder) warned(t *testing.T, want ...string) {
	t.Helper()
	var warns []string
	for _, line := range r.lines {
		if strings.HasPrefix(line, "warn: ") {
			warns = append(warns, line)
		}
	}
	if len(warns) != 1 {
		t.Fatalf("want exactly one warn, got %v", r.lines)
	}
	for _, w := range want {
		if !strings.Contains(warns[0], w) {
			t.Errorf("warn %q is missing %q", warns[0], w)
		}
	}
}

func TestRefTryGet_LogsEveryMissNamingThePipelineAndNode(t *testing.T) {
	rec := &refWarnRecorder{}
	res := &stubResolver{err: fmt.Errorf("no matching run for pipeline %q (maxAge=24h0m0s): %w", "build", ErrRefAbsent)}
	ctx := context.WithValue(context.WithValue(context.Background(), keyPipelineResolver, res), keyLogger, Logger(rec))

	if _, ok := RefToLastRun[buildOut]("build", "artifact").TryGet(ctx); ok {
		t.Fatal("expected a miss")
	}
	rec.warned(t, "build/artifact", "no matching run")
}

func TestRefTryGet_LogsAStoredEmptyOutputAsAMiss(t *testing.T) {
	rec := &refWarnRecorder{}
	res := &stubResolver{runID: "run-xyz", data: []byte("null")}
	ctx := context.WithValue(context.WithValue(context.Background(), keyPipelineResolver, res), keyLogger, Logger(rec))

	if _, ok := RefToLastRun[buildOut]("build", "artifact").TryGet(ctx); ok {
		t.Fatal("expected a miss")
	}
	rec.warned(t, "build/artifact", "stored no output")
}

func TestRefTryGet_PanicsWhenTheStepsContextEnded(t *testing.T) {
	res := &stubResolver{err: fmt.Errorf("no matching run for pipeline %q: %w: %w", "build", context.Canceled, ErrRefAbsent)}
	ctx := context.WithValue(context.Background(), keyPipelineResolver, res)

	defer func() {
		msg, _ := recover().(string)
		if !strings.Contains(msg, "build") {
			t.Fatalf("a cancelled step should panic naming the ref, got %q", msg)
		}
	}()
	_, _ = RefToLastRun[buildOut]("build", "artifact").TryGet(ctx)
	t.Fatal("a cancelled context was reported as an absent output")
}

// A resolver that could not reach its store returns an unmarked error, and a
// step that treated it as its own first run would rebuild everything on every
// run for as long as the outage lasted.
func TestRefTryGet_CrossPipelineUnreachableStorePanics(t *testing.T) {
	defer func() {
		msg, _ := recover().(string)
		if !strings.Contains(msg, "connection refused") {
			t.Fatalf("an unreachable store should panic naming the failure, got %q", msg)
		}
	}()
	r := &stubResolver{err: errors.New("dial tcp 127.0.0.1:4343: connect: connection refused")}
	ctx := context.WithValue(context.Background(), keyPipelineResolver, r)

	RefToLastRun[buildOut]("build", "artifact").TryGet(ctx)
	t.Fatal("a store the resolver could not reach was reported as an absent prior run")
}

func TestRefTryGet_CrossPipelineMarkedAbsenceReturnsFalse(t *testing.T) {
	r := &stubResolver{err: fmt.Errorf("no matching run for pipeline %q (maxAge=0s): %w", "build", ErrRefAbsent)}
	ctx := context.WithValue(context.Background(), keyPipelineResolver, r)

	got, ok := RefToLastRun[buildOut]("build", "artifact").TryGet(ctx)
	if ok {
		t.Fatal("expected ok=false for a marked absence")
	}
	if got.Digest != "" || got.Tag != "" {
		t.Fatalf("expected the zero value, got %+v", got)
	}
}

// Get panics either way, so the sentinel does not change which failures a
// pipeline author can handle through it.
func TestRefGet_CrossPipelineMarkedAbsenceStillPanics(t *testing.T) {
	defer func() {
		msg, _ := recover().(string)
		if !strings.Contains(msg, "no matching run") {
			t.Fatalf("unexpected panic value: %q", msg)
		}
	}()
	r := &stubResolver{err: fmt.Errorf("no matching run for pipeline %q (maxAge=0s): %w", "build", ErrRefAbsent)}
	ctx := context.WithValue(context.Background(), keyPipelineResolver, r)

	RefToLastRun[buildOut]("build", "artifact").Get(ctx)
	t.Fatal("expected a panic")
}
