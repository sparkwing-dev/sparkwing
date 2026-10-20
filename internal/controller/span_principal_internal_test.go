package controller

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestStampPrincipalNamesAccountsWithoutTheirEmail(t *testing.T) {
	for _, subject := range []capability.Subject{{Kind: capability.SubjectAccount, ID: "acct-1"}, {Kind: capability.SubjectMachine, ID: "runner-7"}} {
		rec := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
		ctx, span := tp.Tracer("test").Start(context.Background(), "request")
		notePolicyPrincipal(ctx, capability.Principal{Capability: capability.Capability{Subject: subject}})
		span.End()
		got := ""
		for _, kv := range rec.Ended()[0].Attributes() {
			if kv.Key == "sparkwing.principal" {
				got = kv.Value.AsString()
			}
		}
		if got != subject.ID {
			t.Errorf("principal %+v stamped %q, want %q", subject, got, subject.ID)
		}
	}
}
