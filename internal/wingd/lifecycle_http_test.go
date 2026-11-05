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
	d.localControl = func(*bool) LocalStatus { called = true; return LocalStatus{} }
	request := controllerHTTPRequest(t, http.MethodPost, "stay", `{"stay":true}`, false)
	request.Header.Set("Cookie", "sw_session=browser-cookie")
	recorder := httptest.NewRecorder()
	d.controllerHTTP(http.NotFoundHandler()).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || called {
		t.Fatalf("browser authority: status=%d mutation=%t", recorder.Code, called)
	}
}

func TestControllerHTTPReportsAndChangesOwnerState(t *testing.T) {
	d, err := New(Config{Home: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	stay := false
	d.localControl = func(next *bool) LocalStatus {
		if next != nil {
			stay = *next
		}
		return LocalStatus{Running: true, Stay: stay, PID: 123, Version: "test", Activity: LocalActivity{Runs: 2}}
	}
	for _, test := range []struct {
		method, path, body string
		wantStay           bool
	}{
		{http.MethodGet, "status", "", false},
		{http.MethodPost, "stay", `{"stay":true}`, true},
		{http.MethodGet, "status", "", true},
	} {
		recorder := httptest.NewRecorder()
		d.controllerHTTP(http.NotFoundHandler()).ServeHTTP(recorder, controllerHTTPRequest(t, test.method, test.path, test.body, true))
		var status LocalStatus
		if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusOK || !status.Running || status.Stay != test.wantStay || status.Activity.Runs != 2 {
			t.Fatalf("owner state: status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
}

func TestControllerHTTPRejectsInvalidStayWithoutMutation(t *testing.T) {
	d, err := New(Config{Home: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	d.localControl = func(*bool) LocalStatus { t.Error("invalid request mutated owner"); return LocalStatus{} }
	for _, body := range []string{`{}`, `{"stay":"yes"}`, `invalid`} {
		recorder := httptest.NewRecorder()
		d.controllerHTTP(http.NotFoundHandler()).ServeHTTP(recorder, controllerHTTPRequest(t, http.MethodPost, "stay", body, true))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid stay: %d %s", recorder.Code, recorder.Body.String())
		}
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
