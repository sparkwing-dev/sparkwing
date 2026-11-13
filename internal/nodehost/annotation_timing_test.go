package nodehost

import (
	"context"
	"encoding/json"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestAnnotationTimingIncludesFreshAuthorizationAndAcknowledgement(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	old := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(old); _ = provider.Shutdown(context.Background()) })
	fixture := start(t)
	service := newService(t)
	entered, release := make(chan struct{}), make(chan struct{})
	service.Annotation = func(_ context.Context, _ capability.Authorized, input nodewire.AnnotationRequest) error {
		if input.Message != "private-annotation-token" {
			t.Error("annotation changed")
		}
		close(entered)
		<-release
		return nil
	}
	bearer := admitService(t, fixture, service)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	begin, closeServer := localProcessHTTP(t.Context(), listener, fixture.handler, func() error { return nil })
	begin()
	t.Cleanup(func() {
		if err := closeServer(); err != nil {
			t.Error(err)
		}
	})
	result := make(chan error, 1)
	go func() {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+listener.Addr().String()+"/node/v1/annotations", strings.NewReader(`{"level":"info","message":"private-annotation-token","step":"body"}`))
		if err != nil {
			result <- err
			return
		}
		request.Header.Set("Authorization", "Bearer "+bearer)
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			err = response.Body.Close()
			if response.StatusCode != 200 {
				t.Errorf("annotation status %d", response.StatusCode)
			}
		}
		result <- err
	}()
	select {
	case <-entered:
	case err := <-result:
		t.Fatalf("annotation did not enter: %v", err)
	}
	premature := false
	for _, span := range recorder.Ended() {
		if span.Name() == "nodehost.annotation" {
			premature = true
		}
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if premature {
		t.Error("annotation span ended before persistence acknowledgement")
	}
	spans := recorder.Ended()
	counts := map[string]int{}
	var outer sdktrace.ReadOnlySpan
	for _, span := range spans {
		if span.Name() == "nodehost.annotation" {
			outer = span
		}
	}
	if outer == nil {
		t.Fatal("annotation timing span missing")
	}
	for _, span := range spans {
		if span.SpanContext().TraceID() == outer.SpanContext().TraceID() {
			counts[span.Name()]++
		}
	}
	for name, want := range map[string]int{"nodehost.annotation": 1, "nodehost.parent_auth": 2, "nodehost.binding": 1, "nodehost.annotation_write": 1} {
		if counts[name] != want {
			t.Errorf("%s spans=%d, want %d", name, counts[name], want)
		}
	}
	raw, _ := json.Marshal(tracetest.SpanStubsFromReadOnlySpans(spans))
	for _, secret := range []string{bearer, "private-annotation-token", listener.Addr().String()} {
		if strings.Contains(string(raw), secret) {
			t.Error("trace leaked private request data")
		}
	}
}

func TestSlowAnnotationRecordsWithoutCollector(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fixture := start(t)
		service := newService(t)
		var records []otelutil.OperationTiming
		service.observeTiming = func(timing otelutil.OperationTiming) { records = append(records, timing) }
		service.Annotation = func(context.Context, capability.Authorized, nodewire.AnnotationRequest) error {
			time.Sleep(2 * time.Second)
			return nil
		}
		bearer := admitService(t, fixture, service)
		req := httptest.NewRequest(http.MethodPost, "/node/v1/annotations", strings.NewReader(`{"level":"info","message":"private-token","step":"body"}`))
		req.Header.Set("Authorization", "Bearer "+bearer)
		response := httptest.NewRecorder()
		traceAnnotation(fixture.handler).ServeHTTP(response, req)
		if response.Code != 200 {
			t.Fatal(response.Body.String())
		}
		if len(records) != 2 {
			t.Fatalf("slow write/root records=%d, want 2: %+v", len(records), records)
		}
		for _, record := range records {
			if record.DurationMS != 2000 || record.RequestID == "" || record.RequestID != records[0].RequestID || !record.Success {
				t.Fatalf("slow timing lost: %+v", record)
			}
		}
		raw, _ := json.Marshal(records)
		if strings.Contains(string(raw), bearer) || strings.Contains(string(raw), "private-token") {
			t.Fatal("diagnostic exposed request payload")
		}
	})
}

type timedParentAuthentication struct {
	capability.AuthenticationReader
}

func (p timedParentAuthentication) ReadAuthentication(ctx context.Context, lookup capability.CredentialLookup, evidence capability.RequestEvidence) (capability.AuthenticationSnapshot, error) {
	time.Sleep(2 * time.Second)
	return p.AuthenticationReader.ReadAuthentication(ctx, lookup, evidence)
}

type timedParentBinding struct{ capability.BindingChecker }

func (p timedParentBinding) CheckBinding(ctx context.Context, principal capability.Principal, rule capability.BindingRule, target capability.Target) error {
	time.Sleep(2 * time.Second)
	return p.BindingChecker.CheckBinding(ctx, principal, rule, target)
}

func TestSlowAnnotationAuthorizationAttribution(t *testing.T) {
	for _, phase := range []string{"parent_auth", "binding"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				fixture := start(t, func(deps *ProcessAuthorityDependencies) {
					if phase == "parent_auth" {
						deps.Parent = timedParentAuthentication{deps.Parent}
					} else {
						deps.Bindings = timedParentBinding{deps.Bindings}
					}
				})
				service := newService(t)
				var records []otelutil.OperationTiming
				service.observeTiming = func(timing otelutil.OperationTiming) { records = append(records, timing) }
				service.Annotation = func(context.Context, capability.Authorized, nodewire.AnnotationRequest) error { return nil }
				bearer := admitService(t, fixture, service)
				req := httptest.NewRequest(http.MethodPost, "/node/v1/annotations", strings.NewReader(`{"level":"info","message":"note","step":"body"}`))
				req.Header.Set("Authorization", "Bearer "+bearer)
				response := httptest.NewRecorder()
				traceAnnotation(fixture.handler).ServeHTTP(response, req)
				if response.Code != 200 {
					t.Fatal(response.Body.String())
				}
				found := 0
				for _, record := range records {
					if record.Operation == "nodehost."+phase && record.DurationMS == 2000 {
						found++
					}
				}
				want := 1
				if phase == "parent_auth" {
					want = 2
				}
				if found != want {
					t.Fatalf("slow %s attribution=%d, want %d: %+v", phase, found, want, records)
				}
			})
		})
	}
}

func TestCancelledAnnotationRetainsSlowTiming(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fixture := start(t)
		service := newService(t)
		var records []otelutil.OperationTiming
		service.observeTiming = func(timing otelutil.OperationTiming) { records = append(records, timing) }
		service.Annotation = func(ctx context.Context, _ capability.Authorized, _ nodewire.AnnotationRequest) error {
			<-ctx.Done()
			return ctx.Err()
		}
		bearer := admitService(t, fixture, service)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		time.AfterFunc(time.Second, cancel)
		req := httptest.NewRequest(http.MethodPost, "/node/v1/annotations", strings.NewReader(`{"level":"info","message":"note","step":"body"}`)).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+bearer)
		response := httptest.NewRecorder()
		traceAnnotation(fixture.handler).ServeHTTP(response, req)
		if response.Code < 400 {
			t.Fatal("canceled persistence was acknowledged as success")
		}
		if len(records) != 2 {
			t.Fatalf("canceled write/root records missing: %+v", records)
		}
		for _, record := range records {
			if record.Success || record.DurationMS != 1000 {
				t.Fatalf("cancellation timing changed: %+v", record)
			}
		}
	})
}
