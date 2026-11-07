package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestLeaseReleaseRetriesDuringControllerRecovery(t *testing.T) {
	home := shortHome(t)
	var available atomic.Bool
	started := make(chan struct{})
	var once sync.Once
	serveHTTPFixture(t, home, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != admissionPath+"/requests/ticket" {
			t.Errorf("unexpected cleanup: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		once.Do(func() { close(started) })
		if !available.Load() {
			http.Error(w, "controller restarting", http.StatusServiceUnavailable)
			return
		}
		writeHTTPFixture(w, wingd.AdmissionHTTPState{ID: "ticket", State: "released"})
	}))
	held := fixtureClient(t, home).newLease(t.Context(), wingd.AdmissionHTTPState{ID: "ticket", Grant: &wingwire.Grant{RunID: "job", LeaseToken: "token"}})
	done := make(chan error, 1)
	go func() { done <- held.Release() }()
	<-started
	available.Store(true)
	if err := <-done; err != nil {
		t.Fatalf("release after recovery: %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatalf("repeated release: %v", err)
	}
}

func TestCancelledAdmissionRetriesLostResponseCleanupDuringControllerRecovery(t *testing.T) {
	home := shortHome(t)
	var available atomic.Bool
	committed := make(chan string, 1)
	cleanupStarted := make(chan struct{})
	var once sync.Once
	var key atomic.Value
	serveHTTPFixture(t, home, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var request wingd.AdmissionHTTPRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			key.Store(request.RequestKey)
			committed <- request.RequestKey
			<-r.Context().Done()
			return
		}
		if r.Method != http.MethodDelete || r.URL.Path != admissionPath+"/requests/by-key/"+key.Load().(string) {
			t.Errorf("cleanup lost the request identity: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		once.Do(func() { close(cleanupStarted) })
		if !available.Load() {
			http.Error(w, "controller restarting", http.StatusServiceUnavailable)
			return
		}
		writeHTTPFixture(w, wingd.AdmissionHTTPState{State: "released"})
	}))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := fixtureClient(t, home).Acquire(ctx, wingwire.AdmissionRequest{RunID: "job"}, nil)
		done <- err
	}()
	<-committed
	cancel()
	<-cleanupStarted
	available.Store(true)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquisition after cleanup: %v", err)
	}
}

func TestAdmissionCleanupGivesUpWhenControllerCannotAnswer(t *testing.T) {
	previous := leaseReleaseTimeout
	leaseReleaseTimeout = admissionCleanupTimeout
	t.Cleanup(func() { leaseReleaseTimeout = previous })
	for _, operation := range []string{"release", "cancelled acquisition"} {
		for _, silent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/silent=%t", operation, silent), func(t *testing.T) {
				home := shortHome(t)
				rescue := make(chan struct{})
				committed := make(chan struct{})
				serveHTTPFixture(t, home, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost {
						var request wingd.AdmissionHTTPRequest
						if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
							t.Error(err)
							return
						}
						close(committed)
						select {
						case <-r.Context().Done():
						case <-rescue:
						}
						return
					}
					if silent {
						select {
						case <-r.Context().Done():
							return
						case <-rescue:
						}
					}
					select {
					case <-rescue:
						writeHTTPFixture(w, wingd.AdmissionHTTPState{State: "released"})
					default:
						http.Error(w, "controller shutting down", http.StatusServiceUnavailable)
					}
				}))
				cl := fixtureClient(t, home)
				done := make(chan error, 1)
				if operation == "release" {
					held := cl.newLease(t.Context(), wingd.AdmissionHTTPState{ID: "ticket", Grant: &wingwire.Grant{RunID: "job", LeaseToken: "token"}})
					go func() { done <- held.Release() }()
				} else {
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					go func() { _, err := cl.Acquire(ctx, wingwire.AdmissionRequest{RunID: "job"}, nil); done <- err }()
					<-committed
					cancel()
				}
				err := <-done
				close(rescue)
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("cleanup error = %v, want deadline exceeded", err)
				}
				if operation != "release" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cleanup lost acquisition cancellation: %v", err)
				}
			})
		}
	}
}
