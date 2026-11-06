package wingd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
)

type runnerHTTPFixture struct {
	FuncRunStore
	mu       sync.Mutex
	instance localhostwire.LocalRunnerInstance
}

func (f *runnerHTTPFixture) RegisterLocalRunner(_ context.Context, _ string, instance localhostwire.LocalRunnerInstance) (localhostwire.LocalRunnerInstance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	instance.State, instance.Machine = "registered", "machine"
	f.instance = instance
	return instance, nil
}

func (f *runnerHTTPFixture) ReadLocalRunner(context.Context, string, string) (localhostwire.LocalRunnerInstance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.instance, nil
}

func (f *runnerHTTPFixture) ActivateLocalRunner(context.Context, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.instance.State = "active"
	return nil
}

func (f *runnerHTTPFixture) DrainLocalRunner(context.Context, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.instance.State = "draining"
	return nil
}

func (f *runnerHTTPFixture) StopLocalRunner(context.Context, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.instance.State = "stopped"
	return nil
}

func runnerHTTPRequest(t *testing.T, ctx context.Context, method, path string, input any) *http.Request {
	t.Helper()
	r := admissionHTTPRequest(t, ctx, method, "", input)
	parsed := httptest.NewRequestWithContext(ctx, method, localRunnerPath+path, nil)
	r.URL = parsed.URL
	return r
}

func TestLocalRunnerHTTPRequiresSocketAndLiveProcess(t *testing.T) {
	d := newHTTPAdmissionTest(t, &admissionMemoryStore{})
	fixture := &runnerHTTPFixture{}
	d.cfg.Runs = fixture
	handler := d.localRunnerHTTP(http.NotFoundHandler())
	birth, err := admissionProcessBirth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	instance := localhostwire.LocalRunnerInstance{ID: "instance", CapabilityPrefix: "claimant", PID: os.Getpid(), ProcessBirth: birth, Version: "v1", BuildIdentity: "build"}
	peer, err := controllerPeerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	browser := runnerHTTPRequest(t, t.Context(), http.MethodPost, "", instance)
	browser = browser.WithContext(localhostwire.WithLocalPeer(t.Context(), peer))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, browser)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("browser registered a runner: %d %s", w.Code, w.Body.String())
	}
	instance.ProcessBirth = "reused"
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, runnerHTTPRequest(t, t.Context(), http.MethodPost, "", instance))
	if w.Code != http.StatusForbidden || fixture.instance.ID != "" {
		t.Fatalf("unverified process persisted: %d %+v", w.Code, fixture.instance)
	}
	instance.ProcessBirth = birth
	instance.State = "active"
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, runnerHTTPRequest(t, t.Context(), http.MethodPost, "", instance))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("caller activated during registration: %d", w.Code)
	}
	instance.State = ""
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, runnerHTTPRequest(t, t.Context(), http.MethodPost, "", instance))
	if w.Code != http.StatusOK || fixture.instance.State != "registered" {
		t.Fatalf("verified registration: %d %s", w.Code, w.Body.String())
	}
}

func TestLocalRunnerHTTPDrainWakesIndependentWatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newHTTPAdmissionTest(t, &admissionMemoryStore{})
		fixture := &runnerHTTPFixture{instance: localhostwire.LocalRunnerInstance{ID: "instance", State: "active"}}
		d.cfg.Runs = fixture
		handler := d.localRunnerHTTP(http.NotFoundHandler())
		watched := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, runnerHTTPRequest(t, t.Context(), http.MethodGet, "/instance?wait_state=active", nil))
			watched <- w
		}()
		synctest.Wait()
		select {
		case w := <-watched:
			t.Fatalf("watch returned without a lifecycle change: %d", w.Code)
		default:
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, runnerHTTPRequest(t, t.Context(), http.MethodPost, "/instance/drain", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("drain: %d %s", w.Code, w.Body.String())
		}
		w = <-watched
		var instance localhostwire.LocalRunnerInstance
		if err := json.Unmarshal(w.Body.Bytes(), &instance); err != nil || instance.State != "draining" {
			t.Fatalf("watch missed committed drain: %+v %v", instance, err)
		}
	})
}
