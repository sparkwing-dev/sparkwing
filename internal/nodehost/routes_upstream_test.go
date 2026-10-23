package nodehost

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

const claimToken = "swk_parent-credential-never-shown"

func askRoute(t testing.TB, s *server, method, path, bearer, body string) reply {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, s.URL()+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), claimToken) || strings.Contains(string(raw), "ctl.internal") {
		t.Fatalf("response leaked authority: %s", raw)
	}
	out := reply{status: resp.StatusCode}
	if err := json.Unmarshal(raw, &out.body); err != nil {
		t.Fatal(err)
	}
	return out
}
func wantRefusal(t testing.TB, got reply, status int, code string) {
	t.Helper()
	if got.status != status || failureCode(got) != code {
		t.Fatalf("status %d body %v, want %d %s", got.status, got.body, status, code)
	}
}
func TestUnavailableProcessServiceRefuses(t *testing.T) {
	s := start(t)
	bearer := admitService(t, s, newService(t))
	for _, call := range []struct{ method, path, body string }{
		{http.MethodPost, "/node/v1/oidc-token", `{"audience":"sts"}`},
		{http.MethodGet, "/node/v1/jobs/build/output", ""},
		{http.MethodGet, "/node/v1/pipelines/build/latest?job=build", ""},
		{http.MethodPost, "/node/v1/children", `{"pipeline":"build"}`},
		{http.MethodGet, "/node/v1/children/child/await", ""},
	} {
		wantRefusal(t, askRoute(t, s, call.method, call.path, bearer, call.body), http.StatusServiceUnavailable, "unavailable")
	}
}
