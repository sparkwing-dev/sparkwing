package store_test

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestListRunsFiltersGitIdentityBeforeLimit(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	for i, run := range []store.Run{
		{ID: "wanted", Pipeline: "push", Status: "failed", GitSHA: "abc123ffff", GitBranch: "topic", RepoURL: "https://example.com/acme/app.git"},
		{ID: "newer-a", Pipeline: "push", Status: "failed", GitSHA: "def456", GitBranch: "other", RepoURL: "https://example.com/acme/app.git"},
		{ID: "newer-b", Pipeline: "push", Status: "failed", GitSHA: "fed654", GitBranch: "topic", RepoURL: "https://example.com/other/app.git"},
	} {
		run.StartedAt = time.Unix(int64(i+1), 0)
		if err := st.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}

	runs, err := st.ListRuns(ctx, store.RunFilter{
		GitSHAPrefixes: []string{"abc123"},
		GitBranches:    []string{"topic"},
		RepoURLs:       []string{"https://example.com/acme/app.git"},
		RootOnly:       true,
		Limit:          1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != "wanted" {
		t.Fatalf("runs = %#v, want only wanted", runs)
	}
}

func TestParseRunFilterGitIdentity(t *testing.T) {
	f := store.ParseRunFilter(url.Values{
		"git_sha":    {"abc123"},
		"git_branch": {"topic,release"},
		"repo_url":   {"https://example.com/acme/app.git"},
		"root_only":  {"true"},
	})
	if len(f.GitSHAPrefixes) != 1 || f.GitSHAPrefixes[0] != "abc123" || len(f.GitBranches) != 2 || len(f.RepoURLs) != 1 || !f.RootOnly {
		t.Fatalf("parsed filter = %#v", f)
	}
}
