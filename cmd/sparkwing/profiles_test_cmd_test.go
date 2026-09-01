package main

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type errReader struct{ prefix string }

func (r *errReader) Read(p []byte) (int, error) {
	if r.prefix != "" {
		n := copy(p, r.prefix)
		r.prefix = r.prefix[n:]
		return n, nil
	}
	return 0, errors.New("unexpected EOF")
}

func healthResponse(status int, body io.Reader) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(body),
	}
}

func TestInterpretHealthBody(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       io.Reader
		wantStatus string
		wantDetail string
		wantStop   bool
	}{
		{
			name:     "healthy",
			status:   http.StatusOK,
			body:     strings.NewReader(`{"status":"ok","auth":"enabled"}`),
			wantStop: false,
		},
		{
			name:       "degraded names the problems",
			status:     http.StatusOK,
			body:       strings.NewReader(`{"status":"degraded","problems":["triggers: 3 claimed >30m without /done"]}`),
			wantStatus: "warn",
			wantDetail: "triggers: 3 claimed >30m without /done",
			wantStop:   true,
		},
		{
			name:     "a service outside the contract is not a fault",
			status:   http.StatusOK,
			body:     strings.NewReader("OK\n"),
			wantStop: false,
		},
		{
			name:       "a body that could not be read fails",
			status:     http.StatusOK,
			body:       &errReader{prefix: `{"status":"deg`},
			wantStatus: "fail",
			wantDetail: "read health body",
			wantStop:   true,
		},
		{
			name:       "non-200 fails",
			status:     http.StatusServiceUnavailable,
			body:       strings.NewReader("database unreachable\n"),
			wantStatus: "fail",
			wantDetail: "database unreachable",
			wantStop:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := profileProbeResult{Name: "controller"}
			_, stop := interpretHealthBody(&r, healthResponse(tc.status, tc.body))
			if stop != tc.wantStop {
				t.Fatalf("stop = %v, want %v (status %q, detail %q)", stop, tc.wantStop, r.Status, r.Detail)
			}
			if r.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", r.Status, tc.wantStatus)
			}
			if !strings.Contains(r.Detail, tc.wantDetail) {
				t.Errorf("detail = %q, want it to mention %q", r.Detail, tc.wantDetail)
			}
		})
	}
}
