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

type Config struct {
	ServiceName string

	Version string

	RegisterMetrics func(metric.Meter)
}

type Telemetry struct {
	PromHandler http.Handler

	shutdowns []func(context.Context) error
}

func (t *Telemetry) Shutdown(ctx context.Context) error {
	for _, fn := range t.shutdowns {
		if err := fn(ctx); err != nil {
			log.Printf("warning: otel shutdown error: %v", err)
		}
	}
	return nil
}

func ContextFromEnv(ctx context.Context) context.Context {
	tp := os.Getenv("TRACEPARENT")
	if tp == "" {
		return ctx
	}
	carrier := propagation.MapCarrier{"traceparent": tp}
	return propagation.TraceContext{}.Extract(ctx, carrier)
}

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

func Tracer(name string) trace.Tracer {
	return otel.Tracer(name)
}

func Meter(name string) metric.Meter {
	return otel.Meter(name)
}

func Init(ctx context.Context, cfg Config) *Telemetry {
	t := &Telemetry{}

	serviceName := cfg.ServiceName
	if env := os.Getenv("OTEL_SERVICE_NAME"); env != "" {
		serviceName = env
	}

	res, err := resource.New(
		ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(cfg.Version),
		),
		resource.WithHost(),
	)
	if err != nil {
		res = resource.Default()
	}

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

	if cfg.RegisterMetrics != nil {
		cfg.RegisterMetrics(otel.Meter(cfg.ServiceName))
	}

	log.Printf("otel: metrics enabled (prometheus /metrics)")

	return t
}

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

func WrapHandler(serviceName string, h http.Handler) http.Handler {
	return otelhttp.NewHandler(h, serviceName)
}

func WrapTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return otelhttp.NewTransport(base)
}

type SpanAttrs struct {
	RunID     string
	NodeID    string
	Pipeline  string
	Outcome   string
	Principal string
}

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
			_ = handler.Handle(ctx, r)
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
