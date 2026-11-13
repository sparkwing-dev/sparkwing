package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestControllerRequestTimingPropagatesWithoutSecrets(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	old := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(old); _ = provider.Shutdown(context.Background()) })
	var receivedTrace string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedTrace = r.Header.Get("traceparent")
		if r.Header.Get("Authorization") != "Bearer private-bearer" {
			t.Error("credential changed")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	review, err := NewRunReview(EngineOptions{HTTP: server.Client(), URL: server.URL + "/private-url-token", Bearer: func(context.Context) (string, error) { return "private-bearer", nil }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, parent := provider.Tracer("fixture").Start(t.Context(), "fixture")
	err = review.AppendAnnotation(ctx, "private-run-token", "job", controllerwire.AnnotationWrite{Annotation: nodewire.Annotation{Level: "info", Message: "private-message-token"}})
	parent.End()
	if err != nil {
		t.Fatal(err)
	}
	incoming := (propagation.TraceContext{}).Extract(context.Background(), propagation.MapCarrier{"traceparent": receivedTrace})
	if trace.SpanContextFromContext(incoming).TraceID() != parent.SpanContext().TraceID() {
		t.Error("trace identity lost")
	}
	spans := recorder.Ended()
	var request sdktrace.ReadOnlySpan
	for _, span := range spans {
		if span.Name() == "controller POST /api/v1/runs/{run}/jobs/{job}/annotations" {
			request = span
		}
	}
	if request == nil {
		t.Fatalf("controller timing span missing: %v", spans)
	}
	if receivedTrace == "" {
		t.Fatal("controller received no trace context")
	}
	names := map[string]bool{}
	for _, e := range request.Events() {
		names[e.Name] = true
	}
	for _, name := range []string{"http.tls_start", "http.tls_done", "http.get_connection", "http.got_connection", "http.wrote_request", "http.first_response_byte", "rpc.complete"} {
		if !names[name] {
			t.Errorf("missing timing phase %s", name)
		}
	}
	raw, _ := json.Marshal(tracetest.SpanStubsFromReadOnlySpans(spans))
	for _, secret := range []string{"private-bearer", "private-url-token", "private-run-token", "private-message-token", server.URL} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("span disclosed %s", secret)
		}
	}
}

func TestControllerTimingNeverRecordsCredentialOrTransportErrors(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	old := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(old); _ = provider.Shutdown(context.Background()) })
	sentinel := errors.New("https://user:private-password@controller.example/path?token=private-query-token")
	for _, phase := range []string{"bearer", "transport"} {
		t.Run(phase, func(t *testing.T) {
			options := EngineOptions{URL: "https://controller.example/private-path-token", Bearer: func(context.Context) (string, error) {
				if phase == "bearer" {
					return "", sentinel
				}
				return "private-bearer-token", nil
			}, HTTP: &http.Client{Transport: engineTransport(func(*http.Request) (*http.Response, error) { return nil, sentinel })}}
			review, err := NewRunReview(options)
			if err != nil {
				t.Fatal(err)
			}
			err = review.AppendAnnotation(t.Context(), "run", "job", controllerwire.AnnotationWrite{Annotation: nodewire.Annotation{Level: "info", Message: "private-body-token"}})
			if !errors.Is(err, sentinel) {
				t.Fatalf("transport error changed: %v", err)
			}
		})
	}
	raw, _ := json.Marshal(tracetest.SpanStubsFromReadOnlySpans(recorder.Ended()))
	for _, secret := range []string{"private-password", "private-query-token", "private-path-token", "private-bearer-token", "private-body-token", "controller.example"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("span leaked %s", secret)
		}
	}
}

type slowControllerBody struct{}

func (slowControllerBody) Read([]byte) (int, error) { time.Sleep(2 * time.Second); return 0, io.EOF }

func TestSlowControllerResponsePhasesWithoutCollector(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var records []otelutil.OperationTiming
		ctx := otelutil.WithTimingObserver(t.Context(), func(timing otelutil.OperationTiming) { records = append(records, timing) })
		review, err := NewRunReview(EngineOptions{URL: "https://controller.example/private-token", Bearer: func(context.Context) (string, error) { time.Sleep(time.Second); return "private-bearer", nil }, HTTP: &http.Client{Transport: engineTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: io.NopCloser(slowControllerBody{})}, nil
		})}})
		if err != nil {
			t.Fatal(err)
		}
		if err := review.AppendAnnotation(ctx, "run", "job", controllerwire.AnnotationWrite{Annotation: nodewire.Annotation{Level: "info", Message: "private-body"}}); err != nil {
			t.Fatal(err)
		}
		if len(records) != 2 {
			t.Fatalf("slow bearer/RPC receipts=%d, want 2: %+v", len(records), records)
		}
		var request *otelutil.OperationTiming
		for i := range records {
			if records[i].Operation == "controller.bearer" {
				if records[i].DurationMS != 1000 {
					t.Error("bearer duration lost")
				}
			} else {
				request = &records[i]
			}
		}
		if request == nil || request.DurationMS != 3000 {
			t.Fatalf("RPC duration lost: %+v", records)
		}
		offsets := map[string]int64{}
		for _, phase := range request.Phases {
			offsets[phase.Name] = phase.OffsetMS
		}
		if offsets["http.attempt"] != 1000 || offsets["http.response_body_complete"] != 3000 || offsets["rpc.complete"] != 3000 {
			t.Fatalf("phase attribution lost: %+v", offsets)
		}
		raw, _ := json.Marshal(records)
		for _, secret := range []string{"private-token", "private-bearer", "private-body", "controller.example"} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("diagnostic exposed %s", secret)
			}
		}
	})
}
