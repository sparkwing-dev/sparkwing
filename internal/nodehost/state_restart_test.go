package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

type stateRestartTransport func(*http.Request) (*http.Response, error)

func (f stateRestartTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHeldOwnerStateFollowerReconnectsAfterControllerOutage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		before := followerSnapshot()
		after := runstate.RunSnapshot{Run: runstate.RunRow{Execution: before.Run.Execution}, Nodes: before.Nodes, Expansions: before.Expansions}
		after.Run.StateSeq++
		body, err := json.Marshal(after)
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		requests := 0
		transport := stateRestartTransport(func(*http.Request) (*http.Response, error) {
			requests++
			if time.Since(started) < 6*time.Second {
				return nil, &net.OpError{Op: "dial", Net: "unix", Err: syscall.ECONNREFUSED}
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
		})
		private, err := client.NewLocalHostClient(client.EngineOptions{
			URL: "http://controller", HTTP: &http.Client{Transport: transport},
			Bearer: func(context.Context) (string, error) { return "held-owner", nil },
		}, before.Run.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := private.Close(); err != nil {
				t.Error(err)
			}
		}()
		state := &RemoteHostStore{PrivateHostState: private}
		follower := stateFollower{Store: state, Subscribe: localStateSubscription}
		if err := follower.wait(t.Context(), before); err != nil {
			t.Fatalf("held owner stopped during controller restart after %s: %v", time.Since(started), err)
		}
		if time.Since(started) < 6*time.Second || requests < 2 || context.Cause(t.Context()) != nil {
			t.Fatalf("follower did not await durable recovery: elapsed=%s requests=%d cause=%v", time.Since(started), requests, context.Cause(t.Context()))
		}
	})
}
