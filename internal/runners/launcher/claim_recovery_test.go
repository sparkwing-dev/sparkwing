package launcher

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
)

func claimPollResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestLauncherRestartUsesRealClientRateLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, _, _, _ := fixture()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		gate := ratelimit.New(1, 5*time.Second)
		gate.Allow("launcher", time.Now())
		time.Sleep(time.Second)
		var admissions []time.Time
		wire, err := client.NewEngineClient(client.EngineOptions{URL: "http://controller.invalid", Launcher: true, HTTP: &http.Client{Transport: cloudResourceTransport(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/api/v1/launcher/claim" {
				return claimPollResponse(200, `{"awards":[],"retired":[]}`), nil
			}
			admissions = append(admissions, time.Now())
			if len(admissions) == 2 {
				cancel()
			}
			if !gate.Allow("launcher", time.Now()) {
				response := claimPollResponse(429, `{"error":{"code":"rate_limited","message":"poll later","details":{"retry_after_ms":5000}}}`)
				response.Header.Set("Retry-After", "5")
				return response, nil
			}
			return claimPollResponse(200, `{}`), nil
		})}})
		if err != nil {
			t.Fatal(err)
		}
		l.Control, err = NewControllerControl(wire, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitNode}})
		if err != nil {
			t.Fatal(err)
		}
		var logs strings.Builder
		l.Poll, l.Logger = 2*time.Second, slog.New(slog.NewTextHandler(&logs, nil))
		if err := l.Run(ctx); err != nil {
			t.Fatal(err)
		}
		if len(admissions) != 2 {
			t.Fatalf("admissions=%v logs=%s", admissions, logs.String())
		}
		if strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "initial claim deferred") || admissions[1].Sub(admissions[0]) < 5*time.Second {
			t.Fatalf("startup spacing=%s, want initial INFO and >=5s retry; logs=%s", admissions[1].Sub(admissions[0]), logs.String())
		}
	})
}

func TestLauncherClaimErrorRetainsLastAdvice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, _, _, _ := fixture()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		gate := ratelimit.New(1, 5*time.Second)
		var admissions []time.Time
		limited := 0
		wire, err := client.NewEngineClient(client.EngineOptions{URL: "http://controller.invalid", Launcher: true, HTTP: &http.Client{Transport: cloudResourceTransport(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/api/v1/launcher/claim" {
				return claimPollResponse(200, `{"awards":[],"retired":[]}`), nil
			}
			admissions = append(admissions, time.Now())
			if len(admissions) == 5 {
				cancel()
			}
			if !gate.Allow("launcher", time.Now()) {
				limited++
				return claimPollResponse(429, `{"error":{"code":"rate_limited","message":"poll later","details":{"retry_after_ms":5000}}}`), nil
			}
			if len(admissions) == 2 {
				return claimPollResponse(502, `bad gateway`), nil
			}
			response := claimPollResponse(200, `{}`)
			switch len(admissions) {
			case 3:
				response.Header.Set(controllerwire.ClaimPollAfterHeader, "3")
				gate = ratelimit.New(1, 3*time.Second)
				gate.Allow("launcher", time.Now())
			case 4:
				gate = ratelimit.New(1, 2*time.Second)
				gate.Allow("launcher", time.Now())
			default:
				response.Header.Set(controllerwire.ClaimPollAfterHeader, "5")
			}
			return response, nil
		})}})
		if err != nil {
			t.Fatal(err)
		}
		l.Control, err = NewControllerControl(wire, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitNode}})
		if err != nil {
			t.Fatal(err)
		}
		var logs strings.Builder
		l.Poll, l.Logger = 2*time.Second, slog.New(slog.NewTextHandler(&logs, nil))
		if err := l.Run(ctx); err != nil {
			t.Fatal(err)
		}
		if len(admissions) != 5 {
			t.Fatalf("admissions=%v logs=%s", admissions, logs.String())
		}
		for i, want := range []time.Duration{5 * time.Second, 5 * time.Second, 3 * time.Second, 2 * time.Second} {
			if spacing := admissions[i+1].Sub(admissions[i]); spacing != want || limited != 0 {
				t.Fatalf("claim %d spacing=%s want=%s limited=%d logs=%s", i+2, spacing, want, limited, logs.String())
			}
		}
	})
}
