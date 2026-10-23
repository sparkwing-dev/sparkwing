package controller_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
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
	st, err := teststore.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close() }()

	srv := controller.New(st, nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil))
	var parsed apierr.Envelope
	if w.Code != http.StatusUnauthorized || json.Unmarshal(w.Body.Bytes(), &parsed) != nil || parsed.Error == nil || parsed.Error.Code != apierr.Unauthenticated || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
}

func serverGoPath(t *testing.T) string {
	t.Helper()
	// safety: a reproducible build trims the compiled-in source path, so runtime.Caller
	// yields a module path the filesystem does not hold. A test runs in its own package
	// directory, which is where this source sits.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	return filepath.Join(dir, "server.go")
}
