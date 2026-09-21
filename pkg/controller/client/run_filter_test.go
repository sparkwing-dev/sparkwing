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
		w.Header().Set("X-Sparkwing-Run-Filter-Version", store.RunFilterVersion)
		_, _ = w.Write([]byte(`{"runs":[]}`))
	}))
	t.Cleanup(server.Close)

	_, err := New(server.URL, nil).ListRuns(context.Background(), store.RunFilter{
		GitSHAPrefixes: []string{"abc", "def"},
		GitBranches:    []string{"main"},
		DeclaredRepos:  []string{"acme/app"},
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

func TestListRunsAcceptsCapabilitiesAtAndAboveTheOneTheyNeed(t *testing.T) {
	for _, tc := range []struct {
		version  string
		identity bool
		cursor   bool
	}{
		{version: "", identity: false, cursor: false},
		{version: "1", identity: true, cursor: false},
		{version: "2", identity: true, cursor: true},
		{version: "37", identity: true, cursor: true},
	} {
		t.Run("version "+tc.version, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if tc.version != "" {
					w.Header().Set("X-Sparkwing-Run-Filter-Version", tc.version)
				}
				_, _ = w.Write([]byte(`{"runs":[]}`))
			}))
			t.Cleanup(server.Close)

			_, err := New(server.URL, nil).ListRuns(context.Background(),
				store.RunFilter{GitBranches: []string{"main"}, Limit: 7})
			if got := err == nil; got != tc.identity {
				t.Errorf("identity filter admitted = %v, want %v (err %v)", got, tc.identity, err)
			}

			_, err = New(server.URL, nil).ListRuns(context.Background(),
				store.RunFilter{AfterStartedAt: 1, AfterID: "run-fictional", Limit: 7})
			if got := err == nil; got != tc.cursor {
				t.Errorf("cursor admitted = %v, want %v (err %v)", got, tc.cursor, err)
			}
		})
	}
}

func TestListRunsRefusesAProbeAnOlderControllerWouldClampAway(t *testing.T) {
	serve := func(version string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Sparkwing-Run-Filter-Version", version)
			_, _ = w.Write([]byte(`{"runs":[]}`))
		}))
	}

	old := serve("1")
	t.Cleanup(old.Close)
	if _, err := New(old.URL, nil).ListRuns(context.Background(),
		store.RunFilter{Limit: store.MaxRunListLimit + 1}); err == nil {
		t.Error("a probe past the ceiling was answered by a controller that clamps it away")
	}
	if _, err := New(old.URL, nil).ListRuns(context.Background(),
		store.RunFilter{Limit: 21}); err != nil {
		t.Errorf("a page well under the ceiling was refused: %v", err)
	}

	current := serve(store.RunFilterVersion)
	t.Cleanup(current.Close)
	if _, err := New(current.URL, nil).ListRuns(context.Background(),
		store.RunFilter{Limit: store.MaxRunListLimit + 1}); err != nil {
		t.Errorf("a current controller refused the probe: %v", err)
	}
}
