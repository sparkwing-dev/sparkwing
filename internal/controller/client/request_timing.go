package client

import (
	"context"
	"crypto/tls"
	"net/http/httptrace"

	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
)

func traceControllerRequest(ctx context.Context, method, pattern string) (context.Context, *otelutil.Operation) {
	ctx, operation := otelutil.StartOperation(ctx, "controller "+method+" "+pattern)
	phase := operation.Phase
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GetConn:              func(string) { phase("http.get_connection") },
		GotConn:              func(httptrace.GotConnInfo) { phase("http.got_connection") },
		DNSStart:             func(httptrace.DNSStartInfo) { phase("http.dns_start") },
		DNSDone:              func(httptrace.DNSDoneInfo) { phase("http.dns_done") },
		ConnectStart:         func(string, string) { phase("http.connect_start") },
		ConnectDone:          func(string, string, error) { phase("http.connect_done") },
		TLSHandshakeStart:    func() { phase("http.tls_start") },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { phase("http.tls_done") },
		WroteRequest:         func(httptrace.WroteRequestInfo) { phase("http.wrote_request") },
		GotFirstResponseByte: func() { phase("http.first_response_byte") },
	})
	return ctx, operation
}
