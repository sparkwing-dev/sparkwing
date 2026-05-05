// Package otelutil provides shared OpenTelemetry initialization for all sparkwing services.
package otelutil

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// Config configures a service's telemetry setup.
type Config struct {
	// ServiceName is the OTEL service.name attribute (e.g. "sparkwing-controller").
	ServiceName string

	// Version is the service version, used in resource attributes.
	Version string

	// RegisterMetrics is an optional callback invoked after the MeterProvider
	// is set up, allowing the caller to register service-specific instruments.
	RegisterMetrics func(metric.Meter)
}

// Telemetry holds the initialized telemetry state.
type Telemetry struct {
	// PromHandler serves Prometheus metrics on /metrics.
	PromHandler http.Handler

	shutdowns []func(context.Context) error
}

// Shutdown flushes and shuts down all telemetry providers.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	for _, fn := range t.shutdowns {
		if err := fn(ctx); err != nil {
			log.Printf("warning: otel shutdown error: %v", err)
		}
	}
	return nil
}

// ContextFromEnv extracts a trace context from the TRACEPARENT environment
// variable. If TRACEPARENT is not set, returns the input context unchanged.
// This is used by runners to join the controller's trace.
func ContextFromEnv(ctx context.Context) context.Context {
	tp := os.Getenv("TRACEPARENT")
	if tp == "" {
		return ctx
	}
	carrier := propagation.MapCarrier{"traceparent": tp}
	return propagation.TraceContext{}.Extract(ctx, carrier)
}

// TraceParentEnv returns a "TRACEPARENT=<w3c>" env-var string derived
// from the active span in ctx, or "" when no span is active. Callers
// append the result to a child process's env so the child's
// ContextFromEnv can rejoin the parent trace. Keeps the propagation
// detail inside otelutil so callers don't reach into go.opentelemetry.io.
func TraceParentEnv(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	tp := carrier["traceparent"]
	if tp == "" {
		return ""
	}
	return "TRACEPARENT=" + tp
}

// Tracer returns a named tracer for the given service.
func Tracer(name string) trace.Tracer {
	return otel.Tracer(name)
}

// Meter returns a named meter for the given service.
func Meter(name string) metric.Meter {
	return otel.Meter(name)
}

// Init sets up OpenTelemetry for a sparkwing service:
//   - TracerProvider with OTLP export (if OTEL_EXPORTER_OTLP_ENDPOINT is set)
//   - MeterProvider with Prometheus exporter (always) + OTLP (if endpoint set)
//   - LoggerProvider with slog bridge for trace/span ID correlation (if endpoint set)
//   - W3C TraceContext + Baggage propagators
func Init(ctx context.Context, cfg Config) *Telemetry {
	t := &Telemetry{}

	// OTEL_SERVICE_NAME env wins over the compiled default so one
	// image with multiple subcommands (controller / worker / pool /
	// web) gets the right service.name per-deployment by setting the
	// env in its manifest.
	serviceName := cfg.ServiceName
	if env := os.Getenv("OTEL_SERVICE_NAME"); env != "" {
		serviceName = env
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(cfg.Version),
		),
		resource.WithHost(),
	)
	if err != nil {
		res = resource.Default()
	}

	// ── Metrics: Prometheus exporter (always active) ──
	registry := promclient.NewRegistry()
	promExporter, err := prometheus.New(prometheus.WithRegisterer(registry))
	if err != nil {
		log.Printf("warning: otel prometheus exporter failed: %v", err)
		t.PromHandler = http.NotFoundHandler()
	}
	t.PromHandler = promhttp.HandlerFor(registry, promhttp.HandlerOpts{})

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(promExporter),
	)
	otel.SetMeterProvider(mp)
	t.shutdowns = append(t.shutdowns, mp.Shutdown)

	// ── Traces: OTLP exporter (optional) ──
	// The Go SDK's otlptracehttp respects OTEL_EXPORTER_OTLP_TRACES_ENDPOINT
	// natively for the actual endpoint URL. We just need to know whether to
	// initialize the exporter at all.
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != "" {
		traceCtx, traceCancel := context.WithTimeout(ctx, 5*time.Second)
		traceExporter, err := otlptracehttp.New(traceCtx)
		traceCancel()
		if err != nil {
			log.Printf("warning: otel OTLP trace exporter failed: %v", err)
		} else {
			tp := sdktrace.NewTracerProvider(
				sdktrace.WithResource(res),
				sdktrace.WithBatcher(traceExporter),
				sdktrace.WithSampler(resolveSampler()),
			)
			otel.SetTracerProvider(tp)
			t.shutdowns = append(t.shutdowns, tp.Shutdown)
			log.Printf("otel: traces enabled (OTLP)")
		}
	}

	// ── Logs: OTLP exporter + slog bridge (optional) ──
	//
	// The log exporter is created in a goroutine with a timeout because
	// otlploghttp.New() can block for 30+ seconds when the collector is
	// unreachable, ignoring context cancellation. We start the server
	// first and wire in the log bridge later if it succeeds.
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT") != "" {
		go func() {
			logCtx, logCancel := context.WithTimeout(ctx, 5*time.Second)
			defer logCancel()
			logExporter, err := otlploghttp.New(logCtx)
			if err != nil {
				log.Printf("warning: otel OTLP log exporter failed: %v", err)
				return
			}
			lp := sdklog.NewLoggerProvider(
				sdklog.WithResource(res),
				sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
			)
			t.shutdowns = append(t.shutdowns, lp.Shutdown)

			otelHandler := otelslog.NewHandler(cfg.ServiceName, otelslog.WithLoggerProvider(lp))
			existing := slog.Default().Handler()
			combined := &multiSlogHandler{handlers: []slog.Handler{
				&traceContextHandler{inner: existing},
				otelHandler,
			}}
			slog.SetDefault(slog.New(combined))
			log.Printf("otel: logs enabled (OTLP + slog bridge)")
		}()
	} else {
		slog.SetDefault(slog.New(&traceContextHandler{
			inner: slog.NewTextHandler(os.Stderr, nil),
		}))
	}

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// ── Service-specific metric instruments ──
	if cfg.RegisterMetrics != nil {
		cfg.RegisterMetrics(otel.Meter(cfg.ServiceName))
	}

	log.Printf("otel: metrics enabled (prometheus /metrics)")

	return t
}

// resolveSampler returns the parent-based sampler for this process.
// OTEL_TRACES_SAMPLER_ARG is read as a float in [0,1]; the default is
// 1.0 (sample everything), which matches laptop-dev expectations.
// Prod manifests set it to 0.1 for 10% head sampling.
func resolveSampler() sdktrace.Sampler {
	ratio := 1.0
	if raw := os.Getenv("OTEL_TRACES_SAMPLER_ARG"); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 0 && v <= 1 {
			ratio = v
		} else {
			log.Printf("warning: OTEL_TRACES_SAMPLER_ARG=%q is not a float in [0,1]; defaulting to 1.0", raw)
		}
	}
	return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
}

// WrapHandler wraps h with otelhttp middleware so every HTTP request
// becomes a span rooted at the given service name. Pass the result as
// the process's HTTP handler.
func WrapHandler(serviceName string, h http.Handler) http.Handler {
	return otelhttp.NewHandler(h, serviceName)
}

// WrapTransport returns an http.RoundTripper that stamps outgoing
// requests with the W3C trace-context header derived from the
// request's context. Accepts nil to mean http.DefaultTransport.
func WrapTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return otelhttp.NewTransport(base)
}

// SpanAttrs is the bundle of sparkwing-scoped attributes handlers
// stamp on the active span. All fields are optional; empty values are
// skipped so the scrape never carries an empty-string attribute.
type SpanAttrs struct {
	RunID     string
	NodeID    string
	Pipeline  string
	Outcome   string
	Principal string
}

// StampSpan writes the non-empty fields on a to the active span
// carried in ctx. No-op when the context carries no span (e.g. a code
// path that's not yet wrapped by otelhttp).
func StampSpan(ctx context.Context, a SpanAttrs) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	if a.RunID != "" {
		span.SetAttributes(attribute.String("sparkwing.run_id", a.RunID))
	}
	if a.NodeID != "" {
		span.SetAttributes(attribute.String("sparkwing.node_id", a.NodeID))
	}
	if a.Pipeline != "" {
		span.SetAttributes(attribute.String("sparkwing.pipeline", a.Pipeline))
	}
	if a.Outcome != "" {
		span.SetAttributes(attribute.String("sparkwing.outcome", a.Outcome))
	}
	if a.Principal != "" {
		span.SetAttributes(attribute.String("sparkwing.principal", a.Principal))
	}
}

// traceContextHandler wraps an slog.Handler to inject trace_id and span_id
// from the context into every log record. This enables log-to-trace correlation.
type traceContextHandler struct {
	inner slog.Handler
}

func (h *traceContextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *traceContextHandler) Handle(ctx context.Context, r slog.Record) error {
	sc := trace.SpanContextFromContext(ctx)
	if sc.HasTraceID() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()))
	}
	if sc.HasSpanID() {
		r.AddAttrs(slog.String("span_id", sc.SpanID().String()))
	}
	return h.inner.Handle(ctx, r)
}

func (h *traceContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceContextHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *traceContextHandler) WithGroup(name string) slog.Handler {
	return &traceContextHandler{inner: h.inner.WithGroup(name)}
}

// multiSlogHandler fans out log records to multiple slog handlers.
type multiSlogHandler struct {
	handlers []slog.Handler
}

func (h *multiSlogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *multiSlogHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, r.Level) {
			handler.Handle(ctx, r)
		}
	}
	return nil
}

func (h *multiSlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithAttrs(attrs)
	}
	return &multiSlogHandler{handlers: handlers}
}

func (h *multiSlogHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithGroup(name)
	}
	return &multiSlogHandler{handlers: handlers}
}
