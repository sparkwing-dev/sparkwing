package controller_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TestServerHandler_NoDuplicateRouteRegistrations guards against
// silent route-shadowing. The previous incident: `GET
// /api/v1/auth/session` was registered twice in server.go -- once
// inside the bearer-auth-wrapped mux and once on the outer router.
// Go's ServeMux specificity rules made the outer (exact-method-path)
// registration always win, so the inner copy was dead code -- but
// it also wasn't observable through behavior. The cleanest guard is
// a static scan that counts route literals; if anyone re-introduces
// a dup, this test fails loudly with a pointer to the offending
// line.
func TestServerHandler_NoDuplicateRouteRegistrations(t *testing.T) {
	path := serverGoPath(t)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	// Match the route literal inside every `mux.Handle(...)` /
	// `mux.HandleFunc(...)` / `router.Handle(...)` /
	// `router.HandleFunc(...)` call.
	re := regexp.MustCompile(`(?:mux|router)\.Handle(?:Func)?\("((?:GET|POST|PUT|DELETE|PATCH) [^"]+)"`)
	seen := map[string]int{}
	for _, m := range re.FindAllSubmatch(body, -1) {
		seen[string(m[1])]++
	}
	var dups []string
	for route, count := range seen {
		if count > 1 {
			dups = append(dups, route)
		}
	}
	if len(dups) > 0 {
		t.Fatalf("duplicate route registrations in %s: %v\n"+
			"Go's ServeMux specificity makes the outer-router exact path "+
			"always win, so the inner copy is unreachable dead code. "+
			"Remove the redundant mux.Handle line.", path, dups)
	}
}

// TestController_SessionRoute_OutsideBearerAuth guards the other
// failure mode for the same endpoint: someone deletes the outer
// registration and leaves only the inner one (now reachable, but
// behind bearer auth). The session endpoint intentionally resolves
// `Authorization: Session <id>` -- the bearer middleware would
// reject the request before the handler ran, breaking the dashboard
// login flow. We wire an actual Authenticator so the middleware is
// live, and assert the handler's own error body shape comes back
// (not the middleware's authErrorBody).
func TestController_SessionRoute_OutsideBearerAuth(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close() }()

	srv := controller.New(st, nil).WithAuthenticator(
		controller.NewAuthenticator(st, time.Minute),
	)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/auth/session")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode body %q: %v", body, err)
	}
	// handleSession writes {"error": "session header required"}.
	// authMiddleware writes authErrorBody, which also includes
	// `message` (and may include `missing_scope`). Presence of
	// `message` is the distinguishing signal.
	if _, hasMessage := parsed["message"]; hasMessage {
		t.Fatalf("bearer middleware caught the request before handleSession ran: "+
			"someone gated /api/v1/auth/session behind auth, breaking the dashboard "+
			"login flow. body=%s", body)
	}
	if got, _ := parsed["error"].(string); got != "session header required" {
		t.Fatalf("error=%q want %q (full body: %s)",
			got, "session header required", body)
	}
}

// serverGoPath returns the absolute path to server.go relative to
// this test file. Works regardless of where `go test` is invoked
// from.
func serverGoPath(t *testing.T) string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(here), "server.go")
}
