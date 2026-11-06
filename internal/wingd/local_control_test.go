package wingd

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestLocalControlReportsOwner(t *testing.T) {
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
	w := policyHTTP(t, r.run.daemon, http.MethodGet, controllerHTTPPath+"status", nil)
	var status LocalStatus
	if w.Code != http.StatusOK {
		t.Fatalf("status: HTTP %d: %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Running || status.PID == 0 || status.Version != "v1" {
		t.Fatalf("owner: %+v", status)
	}
	if status.Activity != (LocalActivity{Runs: 1, QueuedTriggers: 2, HeldLeases: 3, DashboardConnections: 4}) {
		t.Fatalf("activity: %+v", status.Activity)
	}
}

func TestLocalControlAdmissionOnlyRefuses(t *testing.T) {
	d := handlerDaemon(t, 2)
	w := policyHTTP(t, d, http.MethodGet, controllerHTTPPath+"status", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("admission-only status: HTTP %d: %s", w.Code, w.Body.String())
	}
}

func TestLocalControlStayRouteRemoved(t *testing.T) {
	d := handlerDaemon(t, 2)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		w := policyHTTP(t, d, method, controllerHTTPPath+"stay", nil)
		if w.Code == http.StatusOK {
			t.Fatalf("removed stay route accepted %s", method)
		}
	}
}
