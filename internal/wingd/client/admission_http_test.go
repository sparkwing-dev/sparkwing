package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func serveHTTPFixture(t *testing.T, home string, handler http.Handler) {
	t.Helper()
	sock, err := wingd.EnsureSocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := wingd.ListenSocket(wingd.APISocketBeside(sock))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); <-done })
}

func writeHTTPFixture(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func fixtureClient(t *testing.T, home string) *Client {
	t.Helper()
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	return &Client{sock: sock}
}

func TestAdmissionRetriesTheSameRequestAndTicketAfterDroppedResponses(t *testing.T) {
	home := shortHome(t)
	var posts, polls atomic.Int32
	var requestKey string
	var queued atomic.Int32
	serveHTTPFixture(t, home, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST " + admissionPath + "/requests":
			var request wingd.AdmissionHTTPRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				http.Error(w, "decode", http.StatusBadRequest)
				return
			}
			birth, err := procgroup.ProcessBirth(os.Getpid())
			if err != nil || request.RootPID != os.Getpid() || request.ProcessBirth != birth {
				t.Errorf("holder identity: %+v, birth=%q err=%v", request, birth, err)
			}
			if posts.Add(1) == 1 {
				requestKey = request.RequestKey
				connection, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = connection.Close()
				}
				return
			}
			if request.RequestKey == "" || request.RequestKey != requestKey {
				t.Error("retry changed the client request key")
			}
			writeHTTPFixture(w, wingd.AdmissionHTTPState{ID: "ticket", State: "queued", Arrival: 7, Revision: 7, Queued: &wingwire.Queued{RunID: "job"}})
		case "GET " + admissionPath + "/requests/ticket":
			if r.URL.Query().Get("wait") != "true" || r.URL.Query().Get("after") != "7" {
				t.Error("queue poll lost its durable response revision")
			}
			if polls.Add(1) == 1 {
				http.Error(w, "restart", http.StatusServiceUnavailable)
				return
			}
			writeHTTPFixture(w, wingd.AdmissionHTTPState{ID: "ticket", State: "granted", Arrival: 7, Revision: 8, Grant: &wingwire.Grant{RunID: "job", LeaseToken: "token"}})
		case "DELETE " + admissionPath + "/requests/ticket":
			writeHTTPFixture(w, wingd.AdmissionHTTPState{ID: "ticket", State: "released"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	cl := fixtureClient(t, home)
	held, err := cl.Acquire(t.Context(), wingwire.AdmissionRequest{RunID: "job"}, func(wingwire.Queued) { queued.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	if posts.Load() != 2 || polls.Load() != 2 || queued.Load() != 1 || held.Token != "token" {
		t.Fatalf("posts=%d polls=%d queued=%d lease=%+v", posts.Load(), polls.Load(), queued.Load(), held)
	}
}

func TestLeaseWatchResumesAfterControllerRestartAndObservesCommittedCancel(t *testing.T) {
	home := shortHome(t)
	var polls atomic.Int32
	var releases atomic.Int32
	watching := make(chan struct{})
	committed := make(chan struct{})
	var once sync.Once
	serveHTTPFixture(t, home, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if r.URL.Query().Get("after") != "10" {
				t.Errorf("lease watch lost grant revision: %s", r.URL.RawQuery)
			}
			if polls.Add(1) == 1 {
				http.Error(w, "restart", http.StatusServiceUnavailable)
				return
			}
			once.Do(func() { close(watching) })
			select {
			case <-committed:
			case <-r.Context().Done():
				return
			}
			writeHTTPFixture(w, wingd.AdmissionHTTPState{ID: "ticket", State: "cancelled", Cancel: &wingwire.Cancel{RunID: "job", Reason: "run cancelled"}})
		case http.MethodDelete:
			releases.Add(1)
			writeHTTPFixture(w, wingd.AdmissionHTTPState{ID: "ticket", State: "released"})
		}
	}))
	held := fixtureClient(t, home).newLease(t.Context(), wingd.AdmissionHTTPState{ID: "ticket", Revision: 10, Grant: &wingwire.Grant{RunID: "job", LeaseToken: "token"}})
	done := make(chan error, 1)
	cancelled := make(chan wingwire.Cancel, 1)
	go func() { done <- held.WatchControl(nil, func(value wingwire.Cancel) { cancelled <- value }) }()
	<-watching
	start := time.Now()
	close(committed)
	value := <-cancelled
	if value.Reason != "run cancelled" {
		t.Fatalf("cancel: %+v", value)
	}
	t.Logf("commit-to-cancel-watch latency: %s", time.Since(start))
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if releases.Load() != 1 {
		t.Fatalf("release issued %d mutations", releases.Load())
	}
}

func TestReleaseInterruptsAWaitingLeaseWatch(t *testing.T) {
	home := shortHome(t)
	watching := make(chan struct{})
	var once sync.Once
	serveHTTPFixture(t, home, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			once.Do(func() { close(watching) })
			<-r.Context().Done()
			return
		}
		writeHTTPFixture(w, wingd.AdmissionHTTPState{ID: "ticket", State: "released"})
	}))
	held := fixtureClient(t, home).newLease(t.Context(), wingd.AdmissionHTTPState{ID: "ticket", Grant: &wingwire.Grant{RunID: "job", LeaseToken: "token"}})
	done := make(chan error, 1)
	go func() { done <- held.WatchControl(nil, nil) }()
	<-watching
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestQueuedAdmissionCancellationDeletesItsDurableTicket(t *testing.T) {
	home := shortHome(t)
	polling := make(chan struct{})
	deleted := make(chan struct{})
	var once sync.Once
	serveHTTPFixture(t, home, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			writeHTTPFixture(w, wingd.AdmissionHTTPState{ID: "ticket", State: "queued", Queued: &wingwire.Queued{RunID: "job"}})
		case http.MethodGet:
			once.Do(func() { close(polling) })
			<-r.Context().Done()
		case http.MethodDelete:
			close(deleted)
			writeHTTPFixture(w, wingd.AdmissionHTTPState{ID: "ticket", State: "released"})
		}
	}))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := fixtureClient(t, home).Acquire(ctx, wingwire.AdmissionRequest{RunID: "job"}, nil)
		done <- err
	}()
	<-polling
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquire: %v", err)
	}
	select {
	case <-deleted:
	default:
		t.Fatal("queued ticket was left in the controller")
	}
}

func TestAdmissionDoesNotRetryARefusalOrMalformedSuccess(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			home := shortHome(t)
			var calls atomic.Int32
			serveHTTPFixture(t, home, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				http.Error(w, "invalid response", status)
			}))
			_, err := fixtureClient(t, home).Acquire(t.Context(), wingwire.AdmissionRequest{RunID: "job"}, nil)
			if err == nil || calls.Load() != 1 {
				t.Fatalf("error=%v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestCancelledAdmissionCancelsACommittedRequestWhoseResponseWasLost(t *testing.T) {
	home := shortHome(t)
	committed := make(chan string, 1)
	cancelled := make(chan string, 1)
	serveHTTPFixture(t, home, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var request wingd.AdmissionHTTPRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			committed <- request.RequestKey
			<-r.Context().Done()
			return
		}
		cancelled <- r.URL.Path
		writeHTTPFixture(w, wingd.AdmissionHTTPState{State: "released"})
	}))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := fixtureClient(t, home).Acquire(ctx, wingwire.AdmissionRequest{RunID: "job"}, nil)
		done <- err
	}()
	key := <-committed
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquisition: %v", err)
	}
	if path := <-cancelled; path != admissionPath+"/requests/by-key/"+key {
		t.Fatalf("cancelled path %q omitted committed request key %q", path, key)
	}
}

func TestEnsureDaemonRefusesAControllerWithoutHTTPAdmission(t *testing.T) {
	home := shortHome(t)
	serveHTTPFixture(t, home, http.NotFoundHandler())
	_, err := EnsureDaemon(t.Context(), Options{Home: home, Version: "test", Spawn: func(string, string) error { t.Error("spawned over an incompatible owner"); return nil }})
	if !errors.Is(err, errDaemonTooOld) {
		t.Fatalf("unsupported HTTP controller: %v", err)
	}
}

func TestLeaseWatchReportsLostAuthorityWhenTheTicketIsGone(t *testing.T) {
	home := shortHome(t)
	serveHTTPFixture(t, home, http.NotFoundHandler())
	cl := fixtureClient(t, home)
	held := cl.newLease(t.Context(), wingd.AdmissionHTTPState{ID: "gone", Grant: &wingwire.Grant{RunID: "job", LeaseToken: "token"}})
	defer cl.Close()
	if err := held.WatchControl(nil, nil); !errors.Is(err, ErrReattachRejected) {
		t.Fatalf("lost ticket: %v", err)
	}
	if _, err := cl.Reattach(t.Context(), "token", "job"); !errors.Is(err, ErrReattachRejected) {
		t.Fatalf("lost lease: %v", err)
	}
}
