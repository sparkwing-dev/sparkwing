package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestTerminalReportSurvivesDeployOutage(t *testing.T) {
	for _, private := range []bool{false, true} {
		for _, status := range []int{502, 503, 504, 0} {
			for _, advice := range []string{"", "10"} {
				if status == 0 && advice != "" {
					continue
				}
				t.Run(fmt.Sprintf("private=%v/status=%d/advice=%s", private, status, advice), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						start := time.Now()
						calls := 0
						var payload []byte
						var previous time.Time
						options := EngineOptions{URL: "http://controller", Bearer: func(context.Context) (string, error) { return "held-award", nil }, HTTP: &http.Client{Transport: engineTransport(func(r *http.Request) (*http.Response, error) {
							calls++
							if calls > 400 {
								t.Fatal("terminal retry is spinning")
							}
							body, err := io.ReadAll(r.Body)
							if err != nil {
								t.Fatal(err)
							}
							if calls == 1 {
								payload = body
							}
							path := "/api/v1/runs/run/finish"
							if private {
								path = "/api/v1/runs/run/local-host/finish"
							}
							if !bytes.Equal(body, payload) || r.Header.Get("Authorization") != "Bearer held-award" || r.URL.Path != path {
								t.Fatal("completion changed payload, award or run")
							}
							if advice == "10" && !previous.IsZero() && time.Since(previous) < 10*time.Second {
								t.Fatal("terminal retry ignored Retry-After")
							}
							previous = time.Now()
							if time.Since(start) < 90*time.Second {
								if status == 0 {
									return nil, syscall.ECONNRESET
								}
								response := engineResponse(status, "no backend")
								response.Header = http.Header{"Retry-After": []string{advice}}
								return response, nil
							}
							var result any = controllerwire.Applied{}
							if private {
								result = runstate.Applied{}
							}
							data, err := json.Marshal(result)
							if err != nil {
								t.Fatal(err)
							}
							return engineResponse(200, string(data)), nil
						})}}
						ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
						defer cancel()
						if err := testTerminalReport(t, ctx, options, private); err != nil || ctx.Err() != nil || time.Since(start) < 90*time.Second || calls < 2 || calls > 400 {
							t.Fatalf("live_award=%v calls=%d elapsed=%s error=%v", ctx.Err() == nil, calls, time.Since(start), err)
						}
					})
				})
			}
		}
	}
}

func TestTerminalReportHonorsAuthorityContextAndPermanentRefusals(t *testing.T) {
	for _, private := range []bool{false, true} {
		for _, status := range []int{401, 403, 409, 200, 503} {
			t.Run(fmt.Sprintf("private=%v/status=%d", private, status), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					start, calls := time.Now(), 0
					options := EngineOptions{URL: "http://controller", Bearer: func(context.Context) (string, error) { return "held-award", nil }, HTTP: &http.Client{Transport: engineTransport(func(*http.Request) (*http.Response, error) {
						calls++
						response := engineResponse(status, "refused")
						response.Header = http.Header{"Retry-After": []string{"60"}}
						return response, nil
					})}}
					ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
					defer cancel()
					err := testTerminalReport(t, ctx, options, private)
					if status == 503 {
						if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != 3*time.Second || calls != 1 {
							t.Fatalf("calls=%d elapsed=%s error=%v", calls, time.Since(start), err)
						}
					} else if err == nil || calls != 1 || time.Since(start) != 0 {
						t.Fatalf("permanent refusal retried: calls=%d elapsed=%s error=%v", calls, time.Since(start), err)
					}
				})
			})
		}
	}
}

func testTerminalReport(t *testing.T, ctx context.Context, options EngineOptions, private bool) error {
	t.Helper()
	if private {
		c, err := NewLocalHostClient(options, "run")
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_, err = c.FinishRunAndUnit(ctx, runstate.FinishRunRequest{Transition: runstate.RunCompletion{RunID: runspec.RunID("run"), Cause: runstate.RunFinish, Reason: "finished"}})
		return err
	}
	c, err := NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Finish(ctx, "run", controllerwire.FinishRequest{Reason: "finished"})
	return err
}

func TestTerminalReportKeepsPermanentRefusalDuringCancellation(t *testing.T) {
	for _, private := range []bool{false, true} {
		t.Run(fmt.Sprint(private), func(t *testing.T) {
			stopped := errors.New("award stopped")
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			options := EngineOptions{URL: "http://controller", Bearer: func(context.Context) (string, error) { return "held-award", nil }, HTTP: &http.Client{Transport: engineTransport(func(*http.Request) (*http.Response, error) {
				cancel(stopped)
				return engineResponse(403, `{"error":{"code":"forbidden","message":"authority ended"}}`), nil
			})}}
			err := testTerminalReport(t, ctx, options, private)
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != hosterr.Forbidden || errors.Is(err, stopped) {
				t.Fatalf("permanent refusal replaced by cancellation: %v", err)
			}
		})
	}
}
