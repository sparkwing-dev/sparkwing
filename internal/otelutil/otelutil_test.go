package otelutil

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/sparkwing-dev/sparkwing/internal/testleak"
)

func TestResolveSampler_Default(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "")
	s := resolveSampler()
	if s == nil {
		t.Fatalf("resolveSampler returned nil")
	}
	if got := s.Description(); !containsAll(got, "ParentBased", "TraceIDRatioBased") {
		t.Errorf("sampler description missing expected tokens: %s", got)
	}
}

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

func TestStampSpan_SkipsEmptyAttrs(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	defer tp.Shutdown(context.Background())
	ctx, span := tp.Tracer("test").Start(context.Background(), "unit")
	defer span.End()
	StampSpan(ctx, SpanAttrs{RunID: "only-this"})
}

func TestTraceParentEnv_EmptyWithoutSpan(t *testing.T) {
	if got := TraceParentEnv(context.Background()); got != "" {
		t.Errorf("expected empty with no span, got %q", got)
	}
}

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

	t.Setenv("TRACEPARENT", env[len(prefix):])
	extracted := ContextFromEnv(context.Background())
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

func TestMain(m *testing.M) {
	os.Unsetenv("OTEL_TRACES_SAMPLER_ARG")
	testleak.Main(m)
}
