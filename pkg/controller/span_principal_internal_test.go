package controller

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestStampPrincipalNamesAccountsWithoutTheirEmail(t *testing.T) {
	for _, tc := range []struct {
		p    Principal
		want string
	}{
		{Principal{Name: "ada@example.com", AccountID: "acct-1"}, "acct-1"},
		{Principal{Name: "ada@example.com", TokenPrefix: "swu_abc"}, "swu_abc"},
		{Principal{Name: "ada@example.com"}, ""},
		{Principal{Name: "runner-7"}, "runner-7"},
	} {
		rec := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
		ctx, span := tp.Tracer("test").Start(context.Background(), "request")
		stampPrincipal(ctx, &tc.p)
		span.End()
		got := ""
		for _, kv := range rec.Ended()[0].Attributes() {
			if kv.Key == "sparkwing.principal" {
				got = kv.Value.AsString()
			}
		}
		if got != tc.want {
			t.Errorf("principal %+v stamped %q, want %q", tc.p, got, tc.want)
		}
	}
}
