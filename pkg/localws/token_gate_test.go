package localws

import (
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
)

// Another account on the machine reaches the loopback listener with no
// credential; the account that started the dashboard holds the token file.
func TestRun_AdmitsOnlyTheServeTokenHolder(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	addr := startLocalws(t, Options{Home: home})
	base := "http://" + addr
	token, err := orchestrator.PathsAt(home).ServeToken()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(orchestrator.PathsAt(home).ServeTokenFile())
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("serve-token mode = %v, %v; want 0600", info.Mode().Perm(), err)
		}
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(method, path, auth string, cookies ...*http.Cookie) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, base+path, strings.NewReader(`{"name":"STOLEN","value":"x","shared":true}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		for _, c := range cookies {
			req.AddCookie(c)
		}
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp
	}

	for _, tc := range []struct{ method, path, auth string }{
		{http.MethodGet, "/api/v1/runs", ""},
		{http.MethodPost, "/api/v1/secrets", ""},
		{http.MethodGet, "/", ""},
		{http.MethodGet, "/api/v1/runs", strings.Repeat("0", len(token))},
		{http.MethodGet, SignInPath + "?token=wrong", ""},
	} {
		resp := send(tc.method, tc.path, tc.auth)
		if resp.StatusCode != http.StatusUnauthorized || len(resp.Cookies()) != 0 {
			t.Errorf("%s %s without the token = %d cookies %v, want 401 and none", tc.method, tc.path, resp.StatusCode, resp.Cookies())
		}
	}
	if resp := send(http.MethodGet, "/api/v1/secrets", token); resp.StatusCode != http.StatusOK {
		t.Fatalf("list secrets with the bearer = %d", resp.StatusCode)
	}
	if resp := send(http.MethodGet, "/api/v1/version", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("version probe without the token = %d, want 200", resp.StatusCode)
	}

	signedIn := send(http.MethodGet, SignInPath+"?token="+token, "")
	if signedIn.StatusCode != http.StatusSeeOther || len(signedIn.Cookies()) != 1 || !signedIn.Cookies()[0].HttpOnly {
		t.Fatalf("sign-in = %d cookies %v, want 303 and one HttpOnly cookie", signedIn.StatusCode, signedIn.Cookies())
	}
	session := signedIn.Cookies()[0]
	for _, path := range []string{"/", "/api/v1/runs"} {
		if resp := send(http.MethodGet, path, "", session); resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s with the session cookie = %d, want 200", path, resp.StatusCode)
		}
	}
}
