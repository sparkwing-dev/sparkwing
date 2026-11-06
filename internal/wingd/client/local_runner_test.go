package client

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
)

func runnerClientInstance() localhostwire.LocalRunnerInstance {
	return localhostwire.LocalRunnerInstance{ID: "instance", CapabilityPrefix: "claimant", PID: 10, ProcessBirth: "birth", Version: "v1", BuildIdentity: "build", Machine: "machine", State: "draining"}
}

func TestLocalRunnerWatchReconnectsAndRetainsInstance(t *testing.T) {
	home := shortHome(t)
	var polls atomic.Int32
	serveHTTPFixture(t, home, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != localRunnerPath+"/instance" || r.URL.Query().Get("wait_state") != "active" || r.Header.Get("Authorization") != "" {
			t.Errorf("changed runner watch: %s %s", r.Method, r.URL)
		}
		if polls.Add(1) == 1 {
			http.Error(w, "controller unavailable", http.StatusServiceUnavailable)
			return
		}
		writeHTTPFixture(w, runnerClientInstance())
	}))
	client, err := NewLocalRunnerClient(home, "v1")
	if err != nil {
		t.Fatal(err)
	}
	instance, err := client.Watch(t.Context(), "instance", "active")
	if err != nil || instance != runnerClientInstance() || polls.Load() != 2 {
		t.Fatalf("runner watch: %+v %v polls=%d", instance, err, polls.Load())
	}
}

func TestLocalRunnerWatchRefusesChangedIdentityAndStopsOnCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "changed identity", true: "cancelled"}[cancelled], func(t *testing.T) {
			home := shortHome(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			serveHTTPFixture(t, home, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if cancelled {
					cancel()
					<-r.Context().Done()
					return
				}
				instance := runnerClientInstance()
				instance.ID = "foreign"
				writeHTTPFixture(w, instance)
			}))
			client, err := NewLocalRunnerClient(home, "v1")
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Watch(ctx, "instance", "active")
			if cancelled {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled watch: %v", err)
				}
			} else {
				var refusal *apierr.Error
				if !errors.As(err, &refusal) || refusal.Code != apierr.ProtocolError {
					t.Fatalf("foreign identity accepted: %v", err)
				}
			}
		})
	}
}

func TestActiveLocalRunnerDiscoveryChecksLifecycleAndIdentity(t *testing.T) {
	for _, state := range []string{"active", "draining", "incomplete"} {
		t.Run(state, func(t *testing.T) {
			home := shortHome(t)
			serveHTTPFixture(t, home, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != localRunnerPath || r.URL.Query().Get("state") != "active" || r.Header.Get("Authorization") != "" {
					t.Errorf("changed runner discovery: %s %s", r.Method, r.URL)
				}
				instance := runnerClientInstance()
				instance.State = state
				if state == "incomplete" {
					instance.ID = ""
				}
				writeHTTPFixture(w, instance)
			}))
			client, err := NewLocalRunnerClient(home, "v1")
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Active(t.Context())
			if (err == nil) != (state == "active") {
				t.Fatalf("discovery state %s: %v", state, err)
			}
		})
	}
}
