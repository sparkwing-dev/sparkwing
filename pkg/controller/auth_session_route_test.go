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

func TestServerHandler_NoDuplicateRouteRegistrations(t *testing.T) {
	path := serverGoPath(t)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
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

func serverGoPath(t *testing.T) string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(here), "server.go")
}
