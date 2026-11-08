package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestLandIntakeRecoversAcrossControllerRestart(t *testing.T) {
	for _, outage := range []time.Duration{2 * time.Second, 20 * time.Second, time.Minute} {
		t.Run(outage.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				started := time.Now()
				var bodies []string
				response, err := json.Marshal(controllerwire.CreateRunResponse{RunID: "land-admitted", Number: 42})
				if err != nil {
					t.Fatal(err)
				}
				c := testEngine(t, func(r *http.Request) (*http.Response, error) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Fatal(err)
					}
					bodies = append(bodies, string(body))
					if r.Header.Get("Idempotency-Key") != "land-key" {
						t.Fatal("intake replay lost its key")
					}
					if time.Since(started) < outage {
						return nil, &net.OpError{Op: "dial", Net: "unix", Err: syscall.ECONNREFUSED}
					}
					return engineResponse(http.StatusOK, string(response)), nil
				})
				request := controllerwire.CreateRunRequest{IdempotencyKey: "land-key"}
				request.Request.Pipeline = "land"
				request.Request.Options.Runner.Value.Kind = runspec.RunnerAny
				got, err := c.CreateRun(t.Context(), request)
				elapsed := time.Since(started)
				if outage < 30*time.Second {
					if err != nil || got.RunID != "land-admitted" {
						t.Fatalf("land lost during restart: elapsed=%s error=%v run=%s", elapsed, err, got.RunID)
					}
				} else if !errors.Is(err, syscall.ECONNREFUSED) || got.RunID != "" || elapsed < 15*time.Second || elapsed > 30*time.Second || !strings.Contains(err.Error(), "cannot connect to controller") {
					t.Fatalf("unbounded or silent intake failure: elapsed=%s error=%v run=%s", elapsed, err, got.RunID)
				}
				for _, body := range bodies {
					if body != bodies[0] {
						t.Fatal("intake replay changed its body")
					}
				}
			})
		})
	}
}

func TestLandIntakeRetriesStoppingControllerResponse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := time.Now()
		response, err := json.Marshal(controllerwire.CreateRunResponse{RunID: "land-admitted"})
		if err != nil {
			t.Fatal(err)
		}
		c := testEngine(t, func(*http.Request) (*http.Response, error) {
			if time.Since(started) < 20*time.Second {
				return engineResponse(http.StatusServiceUnavailable, `{"error":{"code":"unavailable","message":"the controller stopped before answering"}}`), nil
			}
			return engineResponse(http.StatusOK, string(response)), nil
		})
		request := controllerwire.CreateRunRequest{IdempotencyKey: "land-key"}
		request.Request.Pipeline = "land"
		got, err := c.CreateRun(t.Context(), request)
		if err != nil || got.RunID != "land-admitted" || time.Since(started) > 30*time.Second {
			t.Fatalf("stopping controller ended land intake: elapsed=%s run=%s error=%v", time.Since(started), got.RunID, err)
		}
	})
}

func TestIntakeRecoveryBoundsSlowRequestsAndHonorsCallerDeadline(t *testing.T) {
	for _, limit := range []time.Duration{time.Second, time.Minute} {
		t.Run(limit.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				started := time.Now()
				ctx, cancel := context.WithTimeout(t.Context(), limit)
				defer cancel()
				c := testEngine(t, func(r *http.Request) (*http.Response, error) {
					<-r.Context().Done()
					return nil, r.Context().Err()
				})
				_, err := c.CreateRun(ctx, controllerwire.CreateRunRequest{IdempotencyKey: "land-key"})
				if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) != min(limit, 30*time.Second) {
					t.Fatalf("recovery exceeded its deadline: elapsed=%s error=%v", time.Since(started), err)
				}
			})
		})
	}
}
