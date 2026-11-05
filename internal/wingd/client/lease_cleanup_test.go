package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestLeaseReleaseWaitsForControllerRecoveryWithoutExpiring(t *testing.T) {
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
	synctest.Test(t, func(t *testing.T) {
		held := fixtureClient(t, home).newLease(t.Context(), wingd.AdmissionHTTPState{ID: "ticket", Grant: &wingwire.Grant{RunID: "job", LeaseToken: "token"}})
		done := make(chan error, 1)
		go func() { done <- held.Release() }()
		<-started
		time.Sleep(2 * RequestTimeout)
		select {
		case err := <-done:
			t.Fatalf("release abandoned durable authority before the controller recovered: %v", err)
		default:
		}
		available.Store(true)
		if err := <-done; err != nil {
			t.Fatalf("release after recovery: %v", err)
		}
		if err := held.Release(); err != nil {
			t.Fatalf("repeated release: %v", err)
		}
	})
}

func TestCancelledAdmissionRetriesLostResponseCleanupUntilControllerRecovery(t *testing.T) {
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
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			_, err := fixtureClient(t, home).Acquire(ctx, wingwire.AdmissionRequest{RunID: "job"}, nil)
			done <- err
		}()
		<-committed
		cancel()
		<-cleanupStarted
		time.Sleep(2 * RequestTimeout)
		select {
		case err := <-done:
			t.Fatalf("lost-response cleanup abandoned its durable request during restart: %v", err)
		default:
		}
		available.Store(true)
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled acquisition after cleanup: %v", err)
		}
	})
}
