package localcontroller

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestDashboardLoginAuthorizesControllerAPIAsMintingPeer(t *testing.T) {
	home := t.TempDir()
	factory := Config{Bind: func(_ context.Context, b Binding) (Services, error) {
		services := serviceFixture(b)
		services.HTTP = controller.New(b.Store, nil).WithEngineController(b.Controller).HTTP()
		return services, nil
	}}
	runtime, err := wingd.NewLocalRuntime(wingd.LocalRuntimeConfig{Admission: wingd.Config{Home: home}, Open: factory.Open})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := wingd.NewLocalLifecycle(runtime, nil)
	if _, err := lifecycle.StartDashboard(t.Context(), wingd.LocalStartOptions{Home: home, Version: "v0.71.0", Stay: true, DashboardAddress: "127.0.0.1:0"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lifecycle.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	login, err := wingclient.MintDashboardLogin(t.Context(), home)
	if err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t.Cleanup(browser.CloseIdleConnections)
	request := func(method, target, session string, status int, cookies ...*http.Cookie) []byte {
		t.Helper()
		r, err := http.NewRequestWithContext(t.Context(), method, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if session != "" {
			r.Header.Set("Authorization", "Bearer "+session)
		}
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		resp, err := browser.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != status {
			t.Fatalf("%s %s: HTTP %d, want %d: %s (%v)", method, target, resp.StatusCode, status, body, err)
		}
		return body
	}
	resp, err := browser.Get(login.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	session, found := strings.CutPrefix(resp.Header.Get("Location"), "/#s=")
	if resp.StatusCode != http.StatusSeeOther || !found || session == "" {
		t.Fatalf("dashboard login: HTTP %d, Location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	link, err := url.Parse(login.URL)
	if err != nil {
		t.Fatal(err)
	}
	base := link.Scheme + "://" + link.Host
	request(http.MethodGet, base+"/api/v1/runs", "", http.StatusUnauthorized)
	request(http.MethodGet, base+"/api/v1/runs", session, http.StatusOK)
	var identity struct {
		Principal string `json:"principal"`
	}
	if err := json.Unmarshal(request(http.MethodGet, base+"/api/v1/auth/whoami", session, http.StatusOK), &identity); err != nil {
		t.Fatal(err)
	}
	subject, err := wingd.CurrentLocalSubject()
	if err != nil || identity.Principal != subject {
		t.Fatalf("dashboard principal %q, want minting peer %q (%v)", identity.Principal, subject, err)
	}
	for _, name := range []string{"sw_session", "__Host-sw_session"} {
		request(http.MethodGet, base+"/api/v1/runs", session, http.StatusOK, &http.Cookie{Name: name, Value: "swses_stale_other_port"})
	}
	request(http.MethodPost, base+"/logout", session, http.StatusNoContent)
	request(http.MethodGet, base+"/api/v1/runs", session, http.StatusUnauthorized)
}
