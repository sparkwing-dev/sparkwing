package store_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// The repo filter reads GITHUB_REPOSITORY out of each row, so it has to
// keep reading past the newest page to fill the caller's limit.
func TestListTriggers_RepoFilterLooksPastTheFirstPage(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	seedRepoTrigger := func(id, repo string, created time.Time) {
		t.Helper()
		if err := s.CreateTrigger(ctx, store.Trigger{
			ID:         id,
			Pipeline:   "demo",
			CreatedAt:  created,
			TriggerEnv: map[string]string{"GITHUB_REPOSITORY": repo},
		}); err != nil {
			t.Fatalf("CreateTrigger(%s): %v", id, err)
		}
	}

	seedRepoTrigger("want-1", "want/repo", base)
	for i := 0; i < 25; i++ {
		seedRepoTrigger(fmt.Sprintf("other-%02d", i), "other/repo", base.Add(time.Duration(i+1)*time.Minute))
	}

	got, err := s.ListTriggers(ctx, store.TriggerFilter{Repo: "want/repo", Limit: 20})
	if err != nil {
		t.Fatalf("ListTriggers: %v", err)
	}
	if len(got) != 1 || got[0].ID != "want-1" {
		ids := make([]string, len(got))
		for i, tr := range got {
			ids[i] = tr.ID
		}
		t.Fatalf("ListTriggers(repo=want/repo) = %v, want [want-1]", ids)
	}
}

func TestListTriggers_RepoFilterHonoursLimit(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	for i := 0; i < 6; i++ {
		repo := "want/repo"
		if i%2 == 0 {
			repo = "other/repo"
		}
		if err := s.CreateTrigger(ctx, store.Trigger{
			ID:         fmt.Sprintf("t-%02d", i),
			Pipeline:   "demo",
			CreatedAt:  base.Add(time.Duration(i) * time.Minute),
			TriggerEnv: map[string]string{"GITHUB_REPOSITORY": repo},
		}); err != nil {
			t.Fatalf("CreateTrigger: %v", err)
		}
	}

	got, err := s.ListTriggers(ctx, store.TriggerFilter{Repo: "want/repo", Limit: 2})
	if err != nil {
		t.Fatalf("ListTriggers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(ListTriggers(limit=2)) = %d, want 2", len(got))
	}
	if got[0].ID != "t-05" || got[1].ID != "t-03" {
		t.Fatalf("ListTriggers(limit=2) = [%s %s], want newest-first [t-05 t-03]", got[0].ID, got[1].ID)
	}
	for _, tr := range got {
		if tr.TriggerEnv["GITHUB_REPOSITORY"] != "want/repo" {
			t.Fatalf("trigger %s has repo %q", tr.ID, tr.TriggerEnv["GITHUB_REPOSITORY"])
		}
	}
}

// One page is 200 rows, so a match older than that is only reachable if
// the walk carries its cursor across the page boundary.
func TestListTriggers_RepoFilterCrossesAPageBoundary(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	base := time.Now().Add(-24 * time.Hour)

	seed := func(id, repo string, created time.Time) {
		t.Helper()
		if err := s.CreateTrigger(ctx, store.Trigger{
			ID:         id,
			Pipeline:   "demo",
			CreatedAt:  created,
			TriggerEnv: map[string]string{"GITHUB_REPOSITORY": repo},
		}); err != nil {
			t.Fatalf("CreateTrigger(%s): %v", id, err)
		}
	}

	seed("want-oldest", "want/repo", base)
	for i := 0; i < 450; i++ {
		seed(fmt.Sprintf("other-%03d", i), "other/repo", base.Add(time.Duration(i+1)*time.Second))
	}
	seed("want-newest", "want/repo", base.Add(time.Hour))

	got, err := s.ListTriggers(ctx, store.TriggerFilter{Repo: "want/repo", Limit: 20})
	if err != nil {
		t.Fatalf("ListTriggers: %v", err)
	}
	ids := make([]string, len(got))
	for i, tr := range got {
		ids[i] = tr.ID
	}
	if len(ids) != 2 || ids[0] != "want-newest" || ids[1] != "want-oldest" {
		t.Fatalf("ListTriggers(repo=want/repo) = %v, want [want-newest want-oldest]", ids)
	}
}

// Triggers sharing a created_at must not be dropped or repeated at a page
// boundary, which is what the id half of the cursor is for.
func TestListTriggers_RepoFilterTiedTimestampsAcrossPages(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	tied := time.Now().Add(-time.Hour)

	for i := 0; i < 260; i++ {
		repo := "other/repo"
		if i%130 == 0 {
			repo = "want/repo"
		}
		if err := s.CreateTrigger(ctx, store.Trigger{
			ID:         fmt.Sprintf("t-%03d", i),
			Pipeline:   "demo",
			CreatedAt:  tied,
			TriggerEnv: map[string]string{"GITHUB_REPOSITORY": repo},
		}); err != nil {
			t.Fatalf("CreateTrigger: %v", err)
		}
	}

	got, err := s.ListTriggers(ctx, store.TriggerFilter{Repo: "want/repo", Limit: 20})
	if err != nil {
		t.Fatalf("ListTriggers: %v", err)
	}
	seen := map[string]int{}
	for _, tr := range got {
		seen[tr.ID]++
	}
	if len(got) != 2 || seen["t-000"] != 1 || seen["t-130"] != 1 {
		t.Fatalf("ListTriggers with tied timestamps = %v (seen %v), want t-000 and t-130 exactly once", len(got), seen)
	}
}

// A caller cannot tell an empty result from a search that gave up, so the
// store says so in the log when it stops at the horizon short of the limit.
func TestListTriggers_RepoFilterWarnsWhenItStopsAtTheHorizon(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	base := time.Now().Add(-24 * time.Hour)

	for i := 0; i < 5100; i++ {
		if err := s.CreateTrigger(ctx, store.Trigger{
			ID:         fmt.Sprintf("other-%04d", i),
			Pipeline:   "demo",
			CreatedAt:  base.Add(time.Duration(i) * time.Second),
			TriggerEnv: map[string]string{"GITHUB_REPOSITORY": "other/repo"},
		}); err != nil {
			t.Fatalf("CreateTrigger: %v", err)
		}
	}

	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	defer slog.SetDefault(prev)

	got, err := s.ListTriggers(ctx, store.TriggerFilter{Repo: "want/repo", Limit: 20})
	if err != nil {
		t.Fatalf("ListTriggers: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ListTriggers = %d triggers, want 0 (no matching repo)", len(got))
	}
	if !strings.Contains(logged.String(), "search horizon") {
		t.Fatalf("no horizon warning logged; got %q", logged.String())
	}
}
