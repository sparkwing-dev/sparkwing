package otelutil

import (
	"bytes"
	"context"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
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

var builtinSlogHandler slog.Handler

func TestMain(m *testing.M) {
	os.Unsetenv("OTEL_TRACES_SAMPLER_ARG")
	builtinSlogHandler = slog.Default().Handler()
	testleak.Main(m)
}

func TestInitPreservesConfiguredLogging(t *testing.T) {
	for _, key := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"} {
		t.Setenv(key, "")
	}
	old := slog.Default()
	meter, propagator := otel.GetMeterProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() { slog.SetDefault(old); otel.SetMeterProvider(meter); otel.SetTextMapPropagator(propagator) })
	var output bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
	telemetry := Init(t.Context(), Config{ServiceName: "test"})
	t.Cleanup(func() {
		if err := telemetry.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	output.Reset()
	slog.Debug("after-init")
	if !strings.Contains(output.String(), `"level":"DEBUG","msg":"after-init"`) {
		t.Fatalf("configured JSON/debug handler lost: %q", output.String())
	}
}

type startupLogHandler struct {
	slog.Handler
	ready chan struct{}
	once  sync.Once
}

func (h *startupLogHandler) Handle(ctx context.Context, rec slog.Record) error {
	if rec.Message == "otel: logs enabled (OTLP + slog bridge)" {
		h.once.Do(func() { close(h.ready) })
	}
	return h.Handler.Handle(ctx, rec)
}

func TestInitRegistersOTLPBeforeShutdown(t *testing.T) {
	for _, key := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"} {
		t.Setenv(key, "")
	}
	var mu sync.Mutex
	var exported []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		mu.Lock()
		exported = append(exported, body...)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", server.URL)
	old := slog.Default()
	meter, propagator := otel.GetMeterProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() { slog.SetDefault(old); otel.SetMeterProvider(meter); otel.SetTextMapPropagator(propagator) })
	handler := &startupLogHandler{Handler: slog.NewTextHandler(io.Discard, nil), ready: make(chan struct{})}
	slog.SetDefault(slog.New(handler))
	telemetry := Init(t.Context(), Config{ServiceName: "shutdown-test"})
	t.Cleanup(func() {
		select {
		case <-handler.ready:
		case <-time.After(time.Second):
			t.Error("log initialization did not finish")
		}
		if err := telemetry.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	slog.Info("last-before-shutdown")
	if err := telemetry.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !bytes.Contains(exported, []byte("last-before-shutdown")) {
		t.Error("shutdown lost the final OTLP log")
	}
}

func TestInitReturnsWithBuiltinDefaultLogger(t *testing.T) {
	for _, key := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"} {
		t.Setenv(key, "")
	}
	old := slog.Default()
	meter, propagator := otel.GetMeterProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		slog.SetDefault(old)
		otel.SetMeterProvider(meter)
		otel.SetTextMapPropagator(propagator)
	})
	slog.SetDefault(slog.New(builtinSlogHandler))
	logOut := log.Writer()
	log.SetOutput(io.Discard)

	done := make(chan *Telemetry, 1)
	go func() { done <- Init(t.Context(), Config{ServiceName: "test"}) }()
	var telemetry *Telemetry
	select {
	case telemetry = <-done:
	case <-time.After(5 * time.Second):
		// A wedged Init holds the log mutex, so restoring the writer would hang too.
		t.Fatal("Init did not return with the built-in slog handler installed")
	}
	t.Cleanup(func() {
		if err := telemetry.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
		log.SetOutput(logOut)
	})

	printed := make(chan struct{})
	go func() { log.Printf("after-init"); slog.Info("after-init"); close(printed) }()
	select {
	case <-printed:
	case <-time.After(2 * time.Second):
		t.Fatal("logging after Init did not return")
	}
}
