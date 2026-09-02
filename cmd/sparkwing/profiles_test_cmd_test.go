package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/discovery"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
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

func logsHealthProbe(t *testing.T, announce bool, logsHealth http.HandlerFunc) profileProbeResult {
	t.Helper()
	discovery.ResetCache()
	t.Cleanup(discovery.ResetCache)

	logsURL := ""
	if logsHealth != nil {
		logs := httptest.NewServer(logsHealth)
		t.Cleanup(logs.Close)
		logsURL = logs.URL
	}
	ctrl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		announced := ""
		if announce {
			announced = logsURL
		}
		_ = json.NewEncoder(w).Encode(discovery.Services{Logs: announced})
	}))
	t.Cleanup(ctrl.Close)

	prof := &profile.Profile{Controller: &profile.ControllerSpec{URL: ctrl.URL, Token: "swu_test"}}
	r := profileProbeResult{Name: "logs", Status: "ok", Detail: "logs route through the controller"}
	flagUnauthenticatedLogs(context.Background(), prof, &r)
	return r
}

func healthBody(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func TestFlagUnauthenticatedLogsNeverPassesOnAnUnknownAuthState(t *testing.T) {
	cases := []struct {
		name       string
		announce   bool
		health     http.HandlerFunc
		wantStatus string
		wantDetail string
	}{
		{
			name:       "open logs service",
			announce:   true,
			health:     healthBody(http.StatusOK, `{"status":"ok","auth":"disabled"}`),
			wantStatus: "warn",
			wantDetail: "serving unauthenticated",
		},
		{
			name:       "authenticated logs service",
			announce:   true,
			health:     healthBody(http.StatusOK, `{"status":"ok","auth":"enabled"}`),
			wantStatus: "ok",
			wantDetail: "logs route through the controller",
		},
		{
			name:       "image too old to report auth",
			announce:   true,
			health:     healthBody(http.StatusOK, `{"status":"ok"}`),
			wantStatus: "warn",
			wantDetail: "logs service auth unknown",
		},
		{
			name:       "degraded logs service",
			announce:   true,
			health:     healthBody(http.StatusServiceUnavailable, `{"status":"degraded","auth":"enabled","problems":["root: disk full"]}`),
			wantStatus: "warn",
			wantDetail: "logs service degraded",
		},
		{
			name:       "open and degraded reports the open service",
			announce:   true,
			health:     healthBody(http.StatusServiceUnavailable, `{"status":"degraded","auth":"disabled","problems":["root: disk full"]}`),
			wantStatus: "warn",
			wantDetail: "serving unauthenticated",
		},
		{
			name:       "no logs URL announced",
			announce:   false,
			health:     healthBody(http.StatusOK, `{"status":"ok","auth":"disabled"}`),
			wantStatus: "warn",
			wantDetail: "logs service auth unknown",
		},
		{
			name:       "health outside the contract",
			announce:   true,
			health:     healthBody(http.StatusOK, "OK\n"),
			wantStatus: "warn",
			wantDetail: "logs service auth unknown",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := logsHealthProbe(t, tc.announce, tc.health)
			if got.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q (detail %q)", got.Status, tc.wantStatus, got.Detail)
			}
			if !strings.Contains(got.Detail, tc.wantDetail) {
				t.Errorf("detail = %q, want it to mention %q", got.Detail, tc.wantDetail)
			}
		})
	}
}

func TestFlagUnauthenticatedLogsProbesTheProfilesOwnBackend(t *testing.T) {
	discovery.ResetCache()
	t.Cleanup(discovery.ResetCache)

	logs := httptest.NewServer(healthBody(http.StatusOK, `{"status":"ok","auth":"disabled"}`))
	defer logs.Close()
	ctrl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(discovery.Services{})
	}))
	defer ctrl.Close()

	prof := &profile.Profile{
		Controller: &profile.ControllerSpec{URL: ctrl.URL},
		Logs:       &backends.Spec{Type: backends.TypeFilesystem, URL: logs.URL},
	}
	r := profileProbeResult{Name: "logs", Status: "ok"}
	flagUnauthenticatedLogs(context.Background(), prof, &r)
	if r.Status != "warn" || !strings.Contains(r.Detail, "serving unauthenticated") {
		t.Errorf("probe = %+v, want a warning about the profile's own open logs backend", r)
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
			whoami:     `{"principal":"unauthed","kind":"none"}`,
			wantStatus: "warn",
			wantDetail: "serving unauthenticated",
		},
		{
			name:       "real principal",
			whoami:     `{"principal":"runner-1","kind":"runner","scopes":["logs.read"]}`,
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
