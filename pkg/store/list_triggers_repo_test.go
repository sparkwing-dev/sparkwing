package store_test

import (
	"context"
	"fmt"
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
