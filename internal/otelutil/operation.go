package otelutil

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// OperationTiming contains only host-selected operation names and phase offsets.
// Phases are milliseconds since operation start, not additive durations.
type OperationTiming struct {
	Operation     string           `json:"operation"`
	DurationMS    int64            `json:"duration_ms"`
	Success       bool             `json:"success"`
	Phases        []OperationPhase `json:"phases,omitempty"`
	StartedAt     time.Time        `json:"started_at"`
	RequestID     string           `json:"request_id,omitempty"`
	Route         string           `json:"route,omitempty"`
	TraceID       string           `json:"trace_id,omitempty"`
	SpanID        string           `json:"span_id,omitempty"`
	DroppedPhases int              `json:"dropped_phases,omitempty"`
}

// OperationPhase is a host-selected phase and its offset from operation start.
type OperationPhase struct {
	Name     string `json:"name"`
	OffsetMS int64  `json:"offset_ms"`
}

type timingObserverKey struct{}

// WithTimingObserver attaches a best-effort slow-operation sink. The sink must
// not perform network I/O or wait for delivery; errors cannot affect the operation.
func WithTimingObserver(ctx context.Context, observe func(OperationTiming)) context.Context {
	return context.WithValue(ctx, timingObserverKey{}, observe)
}

// Operation owns a span and a bounded set of host-selected phase offsets.
type Operation struct {
	span          trace.Span
	started       time.Time
	name          string
	observe       func(OperationTiming)
	mu            sync.Mutex
	phases        []OperationPhase
	droppedPhases int
}

// StartOperation accepts constant operation names or controller route templates,
// never URLs, credentials, user text or error messages.
func StartOperation(ctx context.Context, name string) (context.Context, *Operation) {
	ctx, span := otel.Tracer("sparkwing.requests").Start(ctx, name)
	observe, _ := ctx.Value(timingObserverKey{}).(func(OperationTiming))
	return ctx, &Operation{span: span, started: time.Now(), name: name, observe: observe}
}

// Phase records a fixed phase name without transport details or error payloads.
func (o *Operation) Phase(name string) {
	if !o.span.IsRecording() && o.observe == nil {
		return
	}
	o.mu.Lock()
	if len(o.phases) < 128 {
		o.phases = append(o.phases, OperationPhase{Name: name, OffsetMS: time.Since(o.started).Milliseconds()})
	} else {
		o.droppedPhases++
	}
	o.mu.Unlock()
	o.span.AddEvent(name)
}

// Finish records only success, so errors containing secrets never enter telemetry.
func (o *Operation) Finish(err error) {
	duration := time.Since(o.started)
	o.span.SetAttributes(attribute.Bool("sparkwing.success", err == nil))
	o.span.End()
	if o.observe != nil && duration >= time.Second {
		o.mu.Lock()
		phases := append([]OperationPhase(nil), o.phases...)
		dropped := o.droppedPhases
		o.mu.Unlock()
		timing := OperationTiming{Operation: o.name, DurationMS: duration.Milliseconds(), Success: err == nil, Phases: phases, StartedAt: o.started.UTC(), DroppedPhases: dropped}
		if sc := o.span.SpanContext(); sc.IsValid() {
			timing.TraceID = sc.TraceID().String()
			timing.SpanID = sc.SpanID().String()
		}
		o.observe(timing)
	}
}
