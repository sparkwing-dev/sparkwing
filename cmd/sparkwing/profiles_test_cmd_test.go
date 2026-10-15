package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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

func healthBody(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func TestProbeLogsReportsWhetherTheControllerKeepsNodeLogs(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       string
	}{
		{"keeps segments", `{"logs":{"segments":true}}`, http.StatusOK, "ok"},
		{"keeps none", `{"logs":{"segments":false}}`, http.StatusOK, "warn"},
		{"cannot say", `{}`, http.StatusInternalServerError, "warn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/capabilities" {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(ctrl.Close)
			prof := &profile.Profile{Controller: &profile.ControllerSpec{URL: ctrl.URL, Token: "swu_test"}}
			if got := probeLogs(context.Background(), prof); got.Status != tc.want {
				t.Fatalf("probeLogs = %+v, want status %s", got, tc.want)
			}
		})
	}
}

func TestProbeAuthWarnsOnTheControllersAnonymousPrincipal(t *testing.T) {
	cases := []struct {
		name       string
		whoami     string
		wantStatus string
		wantDetail string
	}{
		{
			name:       "anonymous principal",
			whoami:     `{"principal":"","kind":"anonymous","scopes":[]}`,
			wantStatus: "warn",
			wantDetail: "controller identifies this request as anonymous",
		},
		{
			name:       "real principal",
			whoami:     `{"principal":"runner-1","kind":"machine","scopes":["logs.read"]}`,
			wantStatus: "ok",
			wantDetail: "principal=runner-1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/auth/whoami") {
					_, _ = w.Write([]byte(tc.whoami))
					return
				}
				_, _ = w.Write([]byte(`{"runs":[]}`))
			}))
			defer ctrl.Close()

			prof := &profile.Profile{Controller: &profile.ControllerSpec{URL: ctrl.URL, Token: "swu_test"}}
			got := probeAuth(context.Background(), prof)
			if got.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q (detail %q)", got.Status, tc.wantStatus, got.Detail)
			}
			if !strings.Contains(got.Detail, tc.wantDetail) {
				t.Errorf("detail = %q, want it to mention %q", got.Detail, tc.wantDetail)
			}
		})
	}
}
