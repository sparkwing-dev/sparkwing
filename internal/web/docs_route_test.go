package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

func head(body string) string {
	const limit = 240
	if len(body) > limit {
		return strconv.Quote(body[:limit]) + "…"
	}
	return strconv.Quote(body)
}

// safety: the real bundle is gitignored, so a fresh checkout serves the same
// missing-bundle notice for the shell and for every miss; comparing against it
// makes "this is not the shell" true of the shell and passes whatever it asks.
func fixtureBundle() fs.FS {
	shell := []byte("<!doctype html><title>Sparkwing</title><div id=\"app\">dashboard shell</div>")
	return fstest.MapFS{"index.html": &fstest.MapFile{Data: shell}}
}

func getPath(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	Pages(fixtureBundle()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestDocsRouteServesTheEmbeddedPagesNotTheAppShell(t *testing.T) {
	pages := docs.List()
	if len(pages) < 2 {
		t.Fatalf("embedded set has %d pages; this test needs two to compare", len(pages))
	}
	first, second := pages[0], pages[1]

	index := getPath(t, "/docs")
	if index.Code != http.StatusOK {
		t.Fatalf("GET /docs status %d", index.Code)
	}
	if !strings.Contains(index.Body.String(), "?p="+first.Slug) {
		t.Errorf("GET /docs does not link page %q; got %s", first.Slug, head(index.Body.String()))
	}

	page := getPath(t, "/docs?p="+first.Slug)
	if page.Code != http.StatusOK {
		t.Fatalf("GET /docs?p=%s status %d", first.Slug, page.Code)
	}
	if want := "<h1>" + first.Title + "</h1>"; !strings.Contains(page.Body.String(), want) {
		t.Errorf("GET /docs?p=%s does not render %q; got %s",
			first.Slug, want, head(page.Body.String()))
	}

	other := getPath(t, "/docs?p="+second.Slug)
	if other.Body.String() == page.Body.String() {
		t.Errorf("/docs?p=%s and /docs?p=%s returned identical bytes, so the response "+
			"does not depend on the slug", first.Slug, second.Slug)
	}

	if root := getPath(t, "/"); root.Body.String() == page.Body.String() {
		t.Error("/ and /docs returned identical bytes, so the catch-all is answering both")
	}
}

func TestDocsRouteRejectsAnUnknownSlug(t *testing.T) {
	rec := getPath(t, "/docs?p=no-such-page")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug status %d, want 404; got %s", rec.Code, head(rec.Body.String()))
	}
}

func TestDocsRouteAnswersTheTrailingSlashSpelling(t *testing.T) {
	pages := docs.List()
	if len(pages) == 0 {
		t.Fatal("embedded set has no pages; this test needs one to request")
	}
	slug := pages[0].Slug

	// safety: both spellings answer 200, so only the bytes distinguish the docs
	// index from the app shell the catch-all serves.
	for _, target := range []string{"/docs/", "/docs/?p=" + slug} {
		canonical := getPath(t, strings.Replace(target, "/docs/", "/docs", 1)).Body.String()
		slashed := getPath(t, target).Body.String()
		shell := getPath(t, "/").Body.String()

		if slashed == shell {
			t.Errorf("GET %s returned the app shell, not the docs page; got %s", target, head(slashed))
		}
		if slashed != canonical {
			t.Errorf("GET %s and its slash-free spelling returned different bytes (%d against %d)",
				target, len(slashed), len(canonical))
		}
	}
}

func TestDocsRouteRefusesAPathBelowIt(t *testing.T) {
	// safety: the shell carries a fresh CSP nonce per response, so two copies of it
	// are never byte-equal and only the normalized text tells them apart.
	nonce := regexp.MustCompile(`nonce="[^"]*"`)
	normalize := func(body string) string { return nonce.ReplaceAllString(body, "") }
	shell := normalize(getPath(t, "/").Body.String())

	for _, target := range []string{"/docs/getting-started", "/docs/a/b"} {
		rec := getPath(t, target)
		if normalize(rec.Body.String()) == shell {
			t.Errorf("GET %s returned the app shell, a 200 carrying no documentation", target)
		}
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s status %d, want 404; doc pages are addressed by ?p=", target, rec.Code)
		}
	}
}
