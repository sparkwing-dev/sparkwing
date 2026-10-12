package nodehost_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const claimToken = "swc_claim-credential-never-shown"

// safety: every forward must carry the claim token and name the grant's own
// run, so the fake fails the test on any other.
type fakeController struct {
	t   *testing.T
	srv *httptest.Server
	mux *http.ServeMux

	mu    sync.Mutex
	paths []string
}

func newController(t *testing.T) *fakeController {
	t.Helper()
	c := &fakeController{t: t, mux: http.NewServeMux()}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.paths = append(c.paths, r.Method+" "+r.URL.EscapedPath())
		c.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			if r.Header.Get("Authorization") != "Bearer "+claimToken {
				t.Errorf("%s %s: forwarded without the claim token", r.Method, r.URL.Path)
			}
			if !strings.HasPrefix(r.URL.EscapedPath(), "/api/v1/runs/run-1/") {
				t.Errorf("%s %s: forwarded to another run", r.Method, r.URL.EscapedPath())
			}
		} else if r.Header.Get("Authorization") != "" {
			t.Errorf("%s %s: a signed object read carried a bearer", r.Method, r.URL.Path)
		}
		c.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *fakeController) handle(pattern string, h http.HandlerFunc) { c.mux.HandleFunc(pattern, h) }

func (c *fakeController) object(key string, data []byte) store.OutputReadGrant {
	c.handle("GET /objects/"+key, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(data) })
	sum := sha256.Sum256(data)
	return store.OutputReadGrant{URL: "/objects/" + key, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
}

func decodeJSON(r *http.Request, into any) bool { return json.NewDecoder(r.Body).Decode(into) == nil }

func (c *fakeController) asked() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.paths...)
}

func answer(status int, body any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
}

// safety: a controller error quoting a URL and the credential, which must
// reach neither the node nor a log line.
var leak = answer(http.StatusInternalServerError, map[string]string{"error": "boom at http://ctl.internal/?token=" + claimToken})

func claimFor(t *testing.T, c *fakeController) *nodehost.Claim {
	t.Helper()
	claim, err := nodehost.NewClaim(c.srv.URL, claimToken)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func admitClaim(t *testing.T, s *nodehost.Server, node string, claim *nodehost.Claim) (string, *secrets.Masker) {
	t.Helper()
	n := nodeWithClaim(node, claim)
	return admit(t, s, n), n.Masker
}

func send(t *testing.T, s *nodehost.Server, method, path, bearer, body string) reply {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, s.URL()+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set(authwire.NodeProtocolHeader, authwire.NodeProtocolVersion)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), claimToken) || strings.Contains(string(raw), "ctl.internal") {
		t.Fatalf("%s %s: reply leaks the claim token or a controller URL: %s", method, path, raw)
	}
	out := reply{status: resp.StatusCode}
	if err := json.Unmarshal(raw, &out.body); err != nil {
		t.Fatalf("%s: decode reply %q: %v", path, raw, err)
	}
	return out
}

func wantRefusal(t *testing.T, got reply, status int, code string) {
	t.Helper()
	if got.status != status || got.body["code"] != code {
		t.Fatalf("status %d body %v, want %d %s", got.status, got.body, status, code)
	}
}

func noLeak(t *testing.T, logs *syncBuffer) {
	t.Helper()
	if l := logs.String(); strings.Contains(l, claimToken) || strings.Contains(l, "ctl.internal") || strings.Contains(l, "boom") {
		t.Fatalf("logs leak the claim token or controller error text:\n%s", l)
	}
}

func TestNewClaimRefusesAnythingButAClaimToken(t *testing.T) {
	for _, tc := range []struct{ url, token string }{
		{"http://ctl", "swr_runner-token"},
		{"http://ctl", ""},
		{"http://user:pw@ctl", claimToken},
		{"ctl:8080", claimToken},
		{"file:///ctl", claimToken},
	} {
		if _, err := nodehost.NewClaim(tc.url, tc.token); err == nil {
			t.Errorf("NewClaim(%q, %q) accepted", tc.url, tc.token)
		}
	}
}

func TestForwardRoutesWithoutAClaimAnswer502(t *testing.T) {
	s, _ := start(t)
	bearer, _ := admitClaim(t, s, "build", nil)
	got := send(t, s, http.MethodPost, "/node/v1/oidc-token", bearer, `{"audience":"sts"}`)
	wantRefusal(t, got, http.StatusBadGateway, "upstream_unavailable")
}

func nodeWithClaim(node string, claim *nodehost.Claim) nodehost.Node {
	return nodehost.Node{RunID: "run-1", NodeID: node, Resolver: &fakeResolver{}, Masker: secrets.NewMasker(), Claim: claim}
}
