package store_test

import (
	"context"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestListRunsFiltersGitIdentityBeforeLimit(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
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

func TestListRunsRejectsNonHexGitSHAPrefix(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.ListRuns(context.Background(), store.RunFilter{GitSHAPrefixes: []string{"not-a-sha"}}); err == nil {
		t.Fatal("ListRuns accepted a non-hex git SHA prefix")
	}
}

func TestParseRunFilterClampsLimit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit string
		want  int
	}{
		{name: "under the cap", limit: "25", want: 25},
		{name: "at the cap", limit: "1000", want: store.MaxRunListLimit},
		{name: "over the cap", limit: "500000", want: store.MaxRunListLimit},
		{name: "absurd", limit: "9223372036854775807", want: store.MaxRunListLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := store.ParseRunFilter(url.Values{"limit": {tc.limit}})
			if f.Limit != tc.want {
				t.Fatalf("Limit = %d, want %d", f.Limit, tc.want)
			}
		})
	}
}

func TestListRunsClampsLimitToTheCap(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	for i := range store.MaxRunListLimit + 5 {
		if err := st.CreateRun(ctx, store.Run{
			ID:        "run-" + strconv.Itoa(i),
			Pipeline:  "push",
			Status:    "passed",
			StartedAt: time.Unix(int64(i+1), 0),
		}); err != nil {
			t.Fatal(err)
		}
	}

	runs, err := st.ListRuns(ctx, store.RunFilter{Limit: 1 << 40})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != store.MaxRunListLimit {
		t.Fatalf("rows = %d, want %d", len(runs), store.MaxRunListLimit)
	}
}

func TestListTriggersClampsLimitToTheCap(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	for i := range store.MaxRunListLimit + 5 {
		if err := st.CreateTrigger(ctx, store.Trigger{
			ID:        "tg-" + strconv.Itoa(i),
			Pipeline:  "push",
			CreatedAt: time.Unix(int64(i+1), 0),
		}); err != nil {
			t.Fatal(err)
		}
	}

	trigs, err := st.ListTriggers(ctx, store.TriggerFilter{Limit: 1 << 40})
	if err != nil {
		t.Fatalf("ListTriggers: %v", err)
	}
	if len(trigs) != store.MaxRunListLimit {
		t.Fatalf("rows = %d, want %d", len(trigs), store.MaxRunListLimit)
	}
}

func TestListEventsAfterClampsLimitToTheCap(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "push", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	for range store.MaxRunListLimit + 5 {
		if _, err := st.AppendEvent(ctx, "run-1", "node-1", "log", []byte(`{"line":"x"}`)); err != nil {
			t.Fatal(err)
		}
	}

	events, err := st.ListEventsAfter(ctx, "run-1", 0, 1<<40)
	if err != nil {
		t.Fatalf("ListEventsAfter: %v", err)
	}
	if len(events) != store.MaxRunListLimit {
		t.Fatalf("rows = %d, want %d", len(events), store.MaxRunListLimit)
	}
}
