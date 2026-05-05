package git

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveCloneURL_NoCache(t *testing.T) {
	t.Setenv("SPARKWING_GITCACHE", "")

	// Point the probe at an unbound port so detection fails fast.
	prev := gitcacheProbeURL
	gitcacheProbeURL = "http://127.0.0.1:1"
	defer func() { gitcacheProbeURL = prev }()

	upstream := "git@github.com:owner/repo.git"
	got := resolveCloneURL(context.Background(), upstream)
	if got != upstream {
		t.Fatalf("got %q, want upstream %q", got, upstream)
	}
}

func TestResolveCloneURL_StubServerNotHealthy(t *testing.T) {
	t.Setenv("SPARKWING_GITCACHE", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	prev := gitcacheProbeURL
	gitcacheProbeURL = srv.URL
	defer func() { gitcacheProbeURL = prev }()

	upstream := "git@github.com:owner/repo.git"
	got := resolveCloneURL(context.Background(), upstream)
	if got != upstream {
		t.Fatalf("404 health: got %q, want upstream %q", got, upstream)
	}
}

func TestResolveCloneURL_StubServerHealthy(t *testing.T) {
	t.Setenv("SPARKWING_GITCACHE", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	prev := gitcacheProbeURL
	gitcacheProbeURL = srv.URL
	defer func() { gitcacheProbeURL = prev }()

	upstream := "git@github.com:owner/repo.git"
	got := resolveCloneURL(context.Background(), upstream)
	want := srv.URL + "/git/repo"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveCloneURL_EnvOverride(t *testing.T) {
	t.Setenv("SPARKWING_GITCACHE", "http://cache.local:9999/")

	upstream := "https://github.com/owner/repo.git"
	got := resolveCloneURL(context.Background(), upstream)
	want := "http://cache.local:9999/git/repo"
	if got != want {
		t.Fatalf("env override: got %q, want %q", got, want)
	}
}

func TestCacheCloneURL_FormatVariants(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"ssh", "git@github.com:owner/repo.git", "http://c/git/repo"},
		{"https", "https://github.com/owner/repo.git", "http://c/git/repo"},
		{"no-suffix", "git@host:owner/repo", "http://c/git/repo"},
		{"trailing-slash-base", "git@github.com:owner/repo.git", "http://c/git/repo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := cacheCloneURL("http://c", c.in)
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
			if !strings.HasPrefix(got, "http://c/") {
				t.Fatalf("base lost: %q", got)
			}
		})
	}
}
