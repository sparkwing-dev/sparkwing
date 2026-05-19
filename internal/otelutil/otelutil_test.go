package otelutil

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// TestResolveSampler_Default returns parent-based always-sample when
// OTEL_TRACES_SAMPLER_ARG is unset.
func TestResolveSampler_Default(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "")
	s := resolveSampler()
	if s == nil {
		t.Fatalf("resolveSampler returned nil")
	}
	// ParentBased + always-sample stringifies like
	// "ParentBased{root:TraceIDRatioBased{1}, ...}".
	if got := s.Description(); !containsAll(got, "ParentBased", "TraceIDRatioBased") {
		t.Errorf("sampler description missing expected tokens: %s", got)
	}
}

// TestResolveSampler_HonorsEnv reads a valid ratio out of the env
// var and builds a sampler with that ratio.
func TestResolveSampler_HonorsEnv(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0.1")
	s := resolveSampler()
	if s == nil {
		t.Fatalf("nil sampler")
	}
	if got := s.Description(); !containsAll(got, "0.1") {
		t.Errorf("sampler description should mention 0.1 ratio: %s", got)
	}
}

// TestResolveSampler_InvalidEnvFallsBackToOne asserts we don't crash
// on a garbage env value and fall back to the default ratio.
func TestResolveSampler_InvalidEnvFallsBackToOne(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "not-a-number")
	s := resolveSampler()
	if s == nil {
		t.Fatalf("nil sampler")
	}
	if got := s.Description(); !containsAll(got, "TraceIDRatioBased") {
		t.Errorf("sampler description unexpected: %s", got)
	}
}

// TestWrapTransport_Roundtrips verifies the wrapped transport still
// delivers requests to the target. otelhttp's span creation requires
// an active TracerProvider to do meaningful work, but the transport
// itself must always pass the request through.
func TestWrapTransport_Roundtrips(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := &http.Client{Transport: WrapTransport(nil)}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("build req: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status=%d want 204", resp.StatusCode)
	}
}

// TestStampSpan_NoopWithoutTracer exercises the handler path when no
// TracerProvider has been installed (global provider returns a noop
// tracer). StampSpan must not panic.
func TestStampSpan_NoopWithoutTracer(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("StampSpan panicked with no tracer: %v", r)
		}
	}()
	StampSpan(context.Background(), SpanAttrs{
		RunID: "r", NodeID: "n", Pipeline: "p", Outcome: "success", Principal: "admin",
	})
}

// TestStampSpan_SkipsEmptyAttrs guards against cardinality leaks via
// accidental empty attribute values. When only a subset of fields is
// set, span.SetAttributes is called per non-empty field.
func TestStampSpan_SkipsEmptyAttrs(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	defer tp.Shutdown(context.Background())
	ctx, span := tp.Tracer("test").Start(context.Background(), "unit")
	defer span.End()
	StampSpan(ctx, SpanAttrs{RunID: "only-this"})
	// Not asserting span contents (sdk-internal) -- this test is a
	// regression guard against a panic path when the only set field
	// is one of several.
}

// TestTraceParentEnv_EmptyWithoutSpan: no active span -> empty string.
// Caller can unconditionally append; empty strings are skipped on the
// child-process env append path.
func TestTraceParentEnv_EmptyWithoutSpan(t *testing.T) {
	if got := TraceParentEnv(context.Background()); got != "" {
		t.Errorf("expected empty with no span, got %q", got)
	}
}

// TestTraceParentEnv_WithSpan produces a W3C traceparent env value
// that a child can feed back through ContextFromEnv to continue the
// trace. Asserts round-trip: the extracted SpanContext matches the
// original trace id.
func TestTraceParentEnv_WithSpan(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	defer tp.Shutdown(context.Background())
	ctx, span := tp.Tracer("test").Start(context.Background(), "unit")
	defer span.End()

	env := TraceParentEnv(ctx)
	const prefix = "TRACEPARENT="
	if env == "" || len(env) <= len(prefix) || env[:len(prefix)] != prefix {
		t.Fatalf("unexpected env var: %q", env)
	}

	// Feed it back through the reader. Use os.Setenv via t.Setenv so
	// the test stays hermetic.
	t.Setenv("TRACEPARENT", env[len(prefix):])
	extracted := ContextFromEnv(context.Background())
	// The extracted context should carry the same TraceID as the
	// original span.
	want := span.SpanContext().TraceID().String()
	got := spanTraceIDString(extracted)
	if got != want {
		t.Errorf("round-trip trace id mismatch: got %q want %q", got, want)
	}
}

func spanTraceIDString(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}

func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		if !contains(haystack, n) {
			return false
		}
	}
	return true
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// Ensure OTEL_TRACES_SAMPLER_ARG doesn't leak into sibling tests.
func TestMain(m *testing.M) {
	os.Unsetenv("OTEL_TRACES_SAMPLER_ARG")
	os.Exit(m.Run())
}
