package wingd

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestLocalControlReportsOwnerAndStay(t *testing.T) {
	f := &runtimeControllerFixture{reconcile: func(context.Context) (LocalActivity, error) {
		return LocalActivity{Runs: 1, QueuedTriggers: 2, HeldLeases: 3, DashboardConnections: 4}, nil
	}}
	r := newRuntimeFixture(t, f)
	lifecycle := NewLocalLifecycle(r, nil)
	if _, err := lifecycle.EnsureStarted(t.Context(), LocalStartOptions{Home: f.home, Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lifecycle.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	for _, stay := range []bool{true, false} {
		w := policyHTTP(t, r.run.daemon, http.MethodPost, controllerHTTPPath+"stay", struct {
			Stay bool `json:"stay"`
		}{stay})
		var status LocalStatus
		if w.Code != http.StatusOK {
			t.Fatalf("stay: HTTP %d: %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if !status.Running || status.Stay != stay || status.PID == 0 || status.Version != "v1" {
			t.Fatalf("owner: %+v", status)
		}
		if status.Activity.Runs != 1 || status.Activity.QueuedTriggers != 2 || status.Activity.HeldLeases != 3 || status.Activity.DashboardConnections != 4 || !status.Activity.IdleSince.IsZero() {
			t.Fatalf("activity: %+v", status.Activity)
		}
	}
}

func TestLocalControlAdmissionOnlyRefuses(t *testing.T) {
	d := handlerDaemon(t, 2)
	w := policyHTTP(t, d, http.MethodGet, controllerHTTPPath+"status", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("admission-only status: HTTP %d: %s", w.Code, w.Body.String())
	}
}

func TestLocalControlGetCannotChangeStay(t *testing.T) {
	d := handlerDaemon(t, 2)
	called := false
	d.localControl = func(*bool) LocalStatus { called = true; return LocalStatus{} }
	w := policyHTTP(t, d, http.MethodGet, controllerHTTPPath+"stay", struct {
		Stay bool `json:"stay"`
	}{true})
	if w.Code != http.StatusMethodNotAllowed || called {
		t.Fatalf("GET stay changed lifecycle: HTTP %d, called=%v", w.Code, called)
	}
}
