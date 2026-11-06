package wingd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func controllerHTTPRequest(t *testing.T, method, path, body string, socket bool) *http.Request {
	t.Helper()
	peer, err := controllerPeerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	ctx := localhostwire.WithLocalPeer(t.Context(), peer)
	if socket {
		ctx = context.WithValue(ctx, socketPeerContextKey{}, true)
	}
	return httptest.NewRequestWithContext(ctx, method, controllerHTTPPath+path, strings.NewReader(body))
}

func TestControllerHTTPRefusesBrowserIdentityAndCookies(t *testing.T) {
	d, err := New(Config{Home: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	d.localControl = func() LocalStatus { called = true; return LocalStatus{} }
	request := controllerHTTPRequest(t, http.MethodGet, "status", "", false)
	request.Header.Set("Cookie", "sw_session=browser-cookie")
	recorder := httptest.NewRecorder()
	d.controllerHTTP(http.NotFoundHandler()).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || called {
		t.Fatalf("browser authority: status=%d mutation=%t", recorder.Code, called)
	}
}

func TestControllerHTTPReportsOwnerState(t *testing.T) {
	d, err := New(Config{Home: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	d.localControl = func() LocalStatus {
		return LocalStatus{Running: true, PID: 123, Version: "test", Activity: LocalActivity{Runs: 2}}
	}
	recorder := httptest.NewRecorder()
	d.controllerHTTP(http.NotFoundHandler()).ServeHTTP(recorder, controllerHTTPRequest(t, http.MethodGet, "status", "", true))
	var status LocalStatus
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || !status.Running || status.Activity.Runs != 2 {
		t.Fatalf("owner state: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestControllerHTTPDashboardReturnsReadinessFailureAndPolicy(t *testing.T) {
	d, err := New(Config{Home: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	d.localDashboard = func(ctx context.Context, address string, readOnly bool) error {
		if ctx.Err() != nil || address != "127.0.0.1:4000" || !readOnly {
			t.Errorf("dashboard input: address=%q readonly=%t error=%v", address, readOnly, ctx.Err())
		}
		return errors.New("dashboard bind failed")
	}
	recorder := httptest.NewRecorder()
	d.controllerHTTP(http.NotFoundHandler()).ServeHTTP(recorder, controllerHTTPRequest(t, http.MethodPost, "dashboard", `{"address":"127.0.0.1:4000","read_only":true}`, true))
	var result wingwire.LocalDashboardAck
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Error != "dashboard bind failed" || !result.ReadOnly {
		t.Fatalf("dashboard result: %+v", result)
	}
}

func TestControllerHTTPDrainPersistenceFailureStopsDaemon(t *testing.T) {
	persistence := &admissionMemoryStore{}
	d := newHTTPAdmissionTest(t, persistence)
	persistence.fail = true
	recorder := httptest.NewRecorder()
	d.controllerHTTP(http.NotFoundHandler()).ServeHTTP(recorder, controllerHTTPRequest(t, http.MethodPost, "drain", "", true))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("drain persistence failure: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	select {
	case <-d.quit:
	default:
		t.Fatal("failed drain retained the elected daemon")
	}
}
