package sparks

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/mod/module"
)

// newMockProxy spins an httptest.Server behaving like proxy.golang.org.
// `versions` maps module-path -> sorted list of tags (newest last); the
// server serves @latest = last, @v/list = newline-joined.
func newMockProxy(t *testing.T, versions map[string][]string) *httptest.Server {
	t.Helper()
	// Key the table by escaped path so lookup matches what the resolver
	// actually sends (proxy.golang.org lower-escapes upper-case chars).
	escaped := make(map[string][]string, len(versions))
	for mod, list := range versions {
		e, err := module.EscapePath(mod)
		if err != nil {
			t.Fatalf("escape %q: %v", mod, err)
		}
		escaped[e] = list
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasSuffix(p, "/@latest") {
			mod := strings.TrimSuffix(strings.TrimPrefix(p, "/"), "/@latest")
			list, ok := escaped[mod]
			if !ok || len(list) == 0 {
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, `{"Version":%q,"Time":"2026-04-22T00:00:00Z"}`, list[len(list)-1])
			return
		}
		if strings.HasSuffix(p, "/@v/list") {
			mod := strings.TrimSuffix(strings.TrimPrefix(p, "/"), "/@v/list")
			list, ok := escaped[mod]
			if !ok {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, strings.Join(list, "\n"))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func resolverAt(srv *httptest.Server) *Resolver {
	return &Resolver{Proxies: []string{srv.URL}, HTTPClient: srv.Client()}
}

func TestResolveExactVersion(t *testing.T) {
	// No mock server - an exact pin must not hit the network.
	r := &Resolver{Proxies: []string{"http://proxy.invalid.example"}}
	m := &Manifest{Libraries: []Library{
		{Name: "x", Source: "example.com/x", Version: "v1.2.3"},
	}}
	got, err := r.Resolve(context.Background(), m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["example.com/x"] != "v1.2.3" {
		t.Fatalf("expected v1.2.3, got %q", got["example.com/x"])
	}
}

func TestResolveSemverRange(t *testing.T) {
	mod := "github.com/sparkwing-dev/sparks-core"
	srv := newMockProxy(t, map[string][]string{
		mod: {"v0.9.0", "v0.9.6", "v0.10.0", "v0.10.3", "v0.11.0", "v1.0.0"},
	})
	r := resolverAt(srv)

	cases := []struct {
		constraint string
		want       string
	}{
		{"^v0.10.0", "v0.11.0"}, // same-major compatible
		{"~v0.10.0", "v0.10.3"}, // patch-only
		{">=v0.10.0", "v1.0.0"}, // anything >= 0.10.0
		{"v0.10.3", "v0.10.3"},  // exact
	}
	for _, tc := range cases {
		t.Run(tc.constraint, func(t *testing.T) {
			m := &Manifest{Libraries: []Library{{Source: mod, Version: tc.constraint}}}
			got, err := r.Resolve(context.Background(), m)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got[mod] != tc.want {
				t.Fatalf("constraint %q: want %s, got %s", tc.constraint, tc.want, got[mod])
			}
		})
	}
}

func TestResolveLatest(t *testing.T) {
	mod := "github.com/example/lib"
	srv := newMockProxy(t, map[string][]string{
		mod: {"v0.1.0", "v0.2.0", "v0.3.1"},
	})
	r := resolverAt(srv)
	m := &Manifest{Libraries: []Library{{Source: mod, Version: "latest"}}}
	got, err := r.Resolve(context.Background(), m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[mod] != "v0.3.1" {
		t.Fatalf("expected v0.3.1, got %q", got[mod])
	}
}

func TestResolveRangeSkipsPrereleases(t *testing.T) {
	mod := "example.com/m"
	srv := newMockProxy(t, map[string][]string{
		mod: {"v0.1.0", "v0.2.0-rc.1", "v0.2.0-rc.2"},
	})
	r := resolverAt(srv)
	m := &Manifest{Libraries: []Library{{Source: mod, Version: "^v0.1.0"}}}
	got, err := r.Resolve(context.Background(), m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[mod] != "v0.1.0" {
		t.Fatalf("expected v0.1.0 (prereleases skipped), got %q", got[mod])
	}
}

func TestResolveRangeNoSatisfying(t *testing.T) {
	mod := "example.com/m"
	srv := newMockProxy(t, map[string][]string{
		mod: {"v0.1.0"},
	})
	r := resolverAt(srv)
	m := &Manifest{Libraries: []Library{{Source: mod, Version: "^v2.0.0"}}}
	if _, err := r.Resolve(context.Background(), m); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestConstraintAllows(t *testing.T) {
	cases := []struct {
		constraint, v string
		want          bool
	}{
		{"^v0.10.0", "v0.10.3", true},
		{"^v0.10.0", "v0.11.0", true},
		{"^v0.10.0", "v1.0.0", false},
		{"~v0.10.0", "v0.10.5", true},
		{"~v0.10.0", "v0.11.0", false},
		{">=v0.10.0", "v1.0.0", true},
		{">=v0.10.0", "v0.9.0", false},
		{"latest", "v9.9.9", true},
		{"v1.2.3", "v1.2.3", true},
		{"v1.2.3", "v1.2.4", false},
		{">v1.0.0", "v1.0.0", false},
		{">v1.0.0", "v1.0.1", true},
		{"<v1.0.0", "v0.9.9", true},
	}
	for _, tc := range cases {
		got := constraintAllows(tc.constraint, tc.v)
		if got != tc.want {
			t.Errorf("constraintAllows(%q, %q) = %v, want %v", tc.constraint, tc.v, got, tc.want)
		}
	}
}
