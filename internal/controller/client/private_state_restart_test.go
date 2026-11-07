package client

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func restartStateClient(t *testing.T, transport engineTransport) *LocalHostClient {
	t.Helper()
	c, err := NewLocalHostClient(EngineOptions{URL: "http://controller", HTTP: &http.Client{Transport: transport}, Bearer: func(context.Context) (string, error) { return "owner-award", nil }}, "run")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	return c
}

func TestPrivateStateReadsSurviveSixSecondControllerOutage(t *testing.T) {
	for _, kind := range []string{"snapshot", "node"} {
		for _, failure := range []string{"transport", "unavailable"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					started := time.Now()
					calls := 0
					ref := runspec.NodeRef{RunID: "run", NodeID: "job"}
					var result any = runstate.RunSnapshot{Run: runstate.RunRow{Execution: runstate.Execution{ID: "run", StateSeq: 2}}}
					if kind == "node" {
						result = runstate.NodeRow{Ref: ref, StateSeq: 2}
					}
					body, err := json.Marshal(result)
					if err != nil {
						t.Fatal(err)
					}
					c := restartStateClient(t, func(r *http.Request) (*http.Response, error) {
						calls++
						if r.Method != http.MethodGet {
							t.Fatalf("state read used %s", r.Method)
						}
						if time.Since(started) < 6*time.Second {
							if failure == "unavailable" {
								return engineResponse(http.StatusServiceUnavailable, `{"error":{"code":"unavailable","message":"controller restarting"}}`), nil
							}
							return nil, &net.OpError{Op: "dial", Net: "unix", Err: syscall.ECONNREFUSED}
						}
						return engineResponse(http.StatusOK, string(body)), nil
					})
					if kind == "snapshot" {
						_, err = c.ReadSnapshot(t.Context(), "run")
					} else {
						_, err = c.ReadNode(t.Context(), ref)
					}
					if err != nil || time.Since(started) < 6*time.Second || calls < 2 {
						t.Fatalf("state read abandoned held work after %s (%d requests): %v", time.Since(started), calls, err)
					}
					if err := context.Cause(t.Context()); err != nil {
						t.Fatalf("host context ended during reconnect: %v", err)
					}
				})
			})
		}
	}
}

func TestPrivateStateReadKeepsPermanentRefusalsFatal(t *testing.T) {
	for _, code := range []apierr.Code{apierr.Unauthenticated, apierr.Forbidden, apierr.ProtocolError, apierr.IntegrityFailure} {
		t.Run(string(code), func(t *testing.T) {
			calls := 0
			body, err := json.Marshal(map[string]any{"error": &apierr.Error{Code: code, Message: "refused"}})
			if err != nil {
				t.Fatal(err)
			}
			c := restartStateClient(t, func(*http.Request) (*http.Response, error) {
				calls++
				return engineResponse(http.StatusForbidden, string(body)), nil
			})
			_, err = c.ReadSnapshot(t.Context(), "run")
			var refused *apierr.Error
			if !errors.As(err, &refused) || refused.Code != code || calls != 1 {
				t.Fatalf("permanent refusal retried: calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestPrivateStateReadReconnectStopsWithHostContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(nil)
		cause := errors.New("host lease ended")
		timer := time.AfterFunc(time.Second, func() { cancel(cause) })
		defer timer.Stop()
		c := restartStateClient(t, func(*http.Request) (*http.Response, error) {
			return nil, &net.OpError{Op: "dial", Net: "unix", Err: syscall.ECONNREFUSED}
		})
		_, err := c.ReadSnapshot(ctx, "run")
		if !errors.Is(err, cause) {
			t.Fatalf("reconnect did not retain host cancellation: %v", err)
		}
	})
}

func TestPrivateStateReadRejectsMalformedAndForeignSnapshots(t *testing.T) {
	foreign, err := json.Marshal(runstate.RunSnapshot{Run: runstate.RunRow{Execution: runstate.Execution{ID: "foreign"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"{", string(foreign)} {
		t.Run(body, func(t *testing.T) {
			calls := 0
			c := restartStateClient(t, func(*http.Request) (*http.Response, error) {
				calls++
				return engineResponse(http.StatusOK, body), nil
			})
			if _, err := c.ReadSnapshot(t.Context(), "run"); err == nil || calls != 1 {
				t.Fatalf("invalid snapshot retried: calls=%d error=%v", calls, err)
			}
		})
	}
}
