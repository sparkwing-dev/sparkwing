package localws

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
)

type gateReply struct {
	status  int
	body    string
	cookies int
}

func gateSend(t *testing.T, base, method, path, auth, body string) gateReply {
	t.Helper()
	req, err := http.NewRequest(method, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return gateReply{status: resp.StatusCode, body: string(raw), cookies: len(resp.Cookies())}
}

func signInSession(t *testing.T, base, token string) string {
	t.Helper()
	minted := gateSend(t, base, http.MethodPost, SignInCodePath, token, "")
	var code struct {
		Code string `json:"code"`
	}
	if minted.status != http.StatusOK || json.Unmarshal([]byte(minted.body), &code) != nil || code.Code == "" {
		t.Fatalf("mint with the serve token = %d %s", minted.status, minted.body)
	}
	exchanged := gateSend(t, base, http.MethodPost, SessionExchangePath, "", `{"code":"`+code.Code+`"}`)
	var session struct {
		Session string `json:"session"`
	}
	if exchanged.status != http.StatusOK || json.Unmarshal([]byte(exchanged.body), &session) != nil || session.Session == "" {
		t.Fatalf("exchange = %d %s", exchanged.status, exchanged.body)
	}
	if again := gateSend(t, base, http.MethodPost, SessionExchangePath, "", `{"code":"`+code.Code+`"}`); again.status != http.StatusUnauthorized {
		t.Errorf("a spent sign-in code exchanged again = %d, want 401", again.status)
	}
	return session.Session
}

// Another account on the machine reaches the loopback listener with no
// credential, and a page it serves on another loopback port shares the host
// a cookie is scoped to; the account that started the dashboard holds the
// token file.
func TestRun_AdmitsOnlyTheServeTokenHolder(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	base := "http://" + startLocalws(t, Options{Home: home})
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

	for _, tc := range []struct{ method, path, auth string }{
		{http.MethodGet, "/api/v1/runs", ""},
		{http.MethodPost, "/api/v1/secrets", ""},
		{http.MethodPost, "/api/v1/crons/nightly/pause", ""},
		{http.MethodGet, "/api/v1/runs", strings.Repeat("0", len(token))},
		{http.MethodGet, "/api/v1/runs", "swl1.00.00"},
		{http.MethodPost, SignInCodePath, ""},
		{http.MethodPost, SessionExchangePath, ""},
	} {
		if got := gateSend(t, base, tc.method, tc.path, tc.auth, `{"code":"guess"}`); got.status != http.StatusUnauthorized {
			t.Errorf("%s %s without a credential = %d, want 401", tc.method, tc.path, got.status)
		}
	}
	if got := gateSend(t, base, http.MethodGet, "/", "", ""); got.status != http.StatusOK {
		t.Errorf("the dashboard shell = %d, want it served so the page can sign in", got.status)
	}
	if got := gateSend(t, base, http.MethodGet, "/api/v1/version", "", ""); got.status != http.StatusOK {
		t.Errorf("version probe without a credential = %d, want 200", got.status)
	}
	if got := gateSend(t, base, http.MethodGet, "/api/v1/secrets", token, ""); got.status != http.StatusOK {
		t.Fatalf("list secrets with the serve token = %d", got.status)
	}

	session := signInSession(t, base, token)
	if got := gateSend(t, base, http.MethodPost, SignInCodePath, session, ""); got.status != http.StatusUnauthorized {
		t.Errorf("a browser session minted a sign-in code = %d, want 401", got.status)
	}
	for _, path := range []string{"/api/v1/runs", "/api/v1/secrets"} {
		got := gateSend(t, base, http.MethodGet, path, session, "")
		if got.status != http.StatusOK {
			t.Errorf("GET %s with the session = %d, want 200", path, got.status)
		}
		if got.cookies != 0 {
			t.Errorf("GET %s set %d cookies; no credential may ride a cookie", path, got.cookies)
		}
	}
	if strings.Contains(session, token) {
		t.Fatal("the browser session carries the serve token")
	}

	restarted := "http://" + startLocalws(t, Options{Home: home})
	if got := gateSend(t, restarted, http.MethodGet, "/api/v1/runs", session, ""); got.status != http.StatusOK {
		t.Errorf("the session after a restart on the same home = %d, want 200", got.status)
	}
}

// A DNS-rebound or cross-origin page must not trade a leaked sign-in code
// for a session.
func TestRun_SessionExchangeSitsBehindTheHostAndOriginGuard(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	addr := startLocalws(t, Options{Home: home})
	base := "http://" + addr
	token, err := orchestrator.PathsAt(home).ServeToken()
	if err != nil {
		t.Fatal(err)
	}
	minted := gateSend(t, base, http.MethodPost, SignInCodePath, token, "")
	var code struct {
		Code string `json:"code"`
	}
	if json.Unmarshal([]byte(minted.body), &code) != nil || code.Code == "" {
		t.Fatalf("mint = %d %s", minted.status, minted.body)
	}
	body := `{"code":"` + code.Code + `"}`
	for _, tc := range []struct {
		name, host, origin, contentType string
		want                            int
	}{
		{"rebound host", "rebind.example:4343", "", "application/json", http.StatusForbidden},
		{"foreign origin", "", "http://evil.example", "application/json", http.StatusForbidden},
		{"browser form post", "", "http://" + addr, "text/plain", http.StatusUnsupportedMediaType},
	} {
		req, err := http.NewRequest(http.MethodPost, base+SessionExchangePath, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if tc.host != "" {
			req.Host = tc.host
		}
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		req.Header.Set("Content-Type", tc.contentType)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s: exchange = %d, want %d", tc.name, resp.StatusCode, tc.want)
		}
	}
	if ok := gateSend(t, base, http.MethodPost, SessionExchangePath, "", body); ok.status != http.StatusOK {
		t.Errorf("refused requests must not spend the code; exchange = %d %s", ok.status, ok.body)
	}
}
