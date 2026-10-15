package main

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
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

func TestProbeAuthChecksFinalRunsRoute(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{{http.StatusOK, "ok"}, {http.StatusUnauthorized, "fail"}, {http.StatusForbidden, "fail"}} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			calls := 0
			profileHTTPFixture(t, func(req *http.Request) (int, string) {
				calls++
				if req.Method != "GET" || req.URL.Path != "/api/v1/runs" || req.URL.Query().Get("limit") != "1" || req.Header.Get("Authorization") != "Bearer private-credential" {
					t.Fatalf("auth probe: %s %s", req.Method, req.URL)
				}
				return tc.status, `{"runs":[],"next_cursor":"","truncated":false}`
			})
			result := probeAuth(t.Context(), &profile.Profile{Controller: &profile.ControllerSpec{URL: "https://controller.example", Token: "private-credential"}})
			if result.Status != tc.want || calls != 1 {
				t.Fatalf("probe: %+v calls=%d", result, calls)
			}
		})
	}
}
