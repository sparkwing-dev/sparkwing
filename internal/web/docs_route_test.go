package web

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

func head(body string) string {
	const limit = 240
	if len(body) > limit {
		return strconv.Quote(body[:limit]) + "…"
	}
	return strconv.Quote(body)
}

func getPath(t *testing.T, opts HandlerOptions, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	HandlerFromOptions(opts).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestDocsRouteServesTheEmbeddedPagesNotTheAppShell(t *testing.T) {
	var opts HandlerOptions
	pages := docs.List()
	if len(pages) < 2 {
		t.Fatalf("embedded set has %d pages; this test needs two to compare", len(pages))
	}
	first, second := pages[0], pages[1]

	index := getPath(t, opts, "/docs")
	if index.Code != http.StatusOK {
		t.Fatalf("GET /docs status %d", index.Code)
	}
	if !strings.Contains(index.Body.String(), "?p="+first.Slug) {
		t.Errorf("GET /docs does not link page %q; got %s", first.Slug, head(index.Body.String()))
	}

	page := getPath(t, opts, "/docs?p="+first.Slug)
	if page.Code != http.StatusOK {
		t.Fatalf("GET /docs?p=%s status %d", first.Slug, page.Code)
	}
	if want := "<h1>" + first.Title + "</h1>"; !strings.Contains(page.Body.String(), want) {
		t.Errorf("GET /docs?p=%s does not render %q; got %s",
			first.Slug, want, head(page.Body.String()))
	}

	other := getPath(t, opts, "/docs?p="+second.Slug)
	if other.Body.String() == page.Body.String() {
		t.Errorf("/docs?p=%s and /docs?p=%s returned identical bytes, so the response "+
			"does not depend on the slug", first.Slug, second.Slug)
	}

	if root := getPath(t, opts, "/"); root.Body.String() == page.Body.String() {
		t.Error("/ and /docs returned identical bytes, so the catch-all is answering both")
	}
}

func TestDocsRouteRejectsAnUnknownSlug(t *testing.T) {
	rec := getPath(t, HandlerOptions{}, "/docs?p=no-such-page")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug status %d, want 404; got %s", rec.Code, head(rec.Body.String()))
	}
}

func TestDocsRouteInheritsTheDashboardsAuthPosture(t *testing.T) {
	opts := HandlerOptions{RequireLogin: true, ControllerURL: "http://127.0.0.1:1"}

	rec := getPath(t, opts, "/docs")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("unauthenticated GET /docs status %d, want 303 to the login page; got %s",
			rec.Code, head(rec.Body.String()))
	}
	if loc := rec.Header().Get("Location"); loc != "/login?next=%2Fdocs" {
		t.Errorf("unauthenticated GET /docs redirected to %q, want the login page", loc)
	}

	if open := getPath(t, HandlerOptions{}, "/docs"); open.Code != http.StatusOK {
		t.Errorf("GET /docs on a login-free dashboard status %d, want 200", open.Code)
	}
}
