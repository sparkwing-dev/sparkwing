package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestListRunsSerializesNativeIdentityFilters(t *testing.T) {
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Sparkwing-Run-Filter-Version", "1")
		_, _ = w.Write([]byte(`{"runs":[]}`))
	}))
	t.Cleanup(server.Close)

	_, err := New(server.URL, nil).ListRuns(context.Background(), store.RunFilter{
		GitSHAPrefixes: []string{"abc", "def"},
		GitBranches:    []string{"main"},
		Repos:          []string{"acme/app"},
		RepoURLs:       []string{"https://example.com/acme/app.git"},
		RootOnly:       true,
		Limit:          7,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "git_branch=main&git_sha=abc%2Cdef&limit=7&repo=acme%2Fapp&repo_url=https%3A%2F%2Fexample.com%2Facme%2Fapp.git&root_only=true"
	if query != want {
		t.Fatalf("query = %q, want %q", query, want)
	}
}
