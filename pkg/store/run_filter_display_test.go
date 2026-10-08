package store_test

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func displayFilterRuns() []store.Run {
	base := time.Unix(1700000000, 0)
	finished := func(sec int) *time.Time { t := base.Add(time.Duration(sec) * time.Second); return &t }
	return []store.Run{
		{ID: "r-push-main", Pipeline: "build", Status: "success", TriggerSource: "push", GitBranch: "main", GitSHA: "aaaaaaa111", DeclaredRepo: "acme/web", StartedAt: base.Add(1 * time.Second), FinishedAt: finished(5)},
		{ID: "r-cron-main", Pipeline: "nightly", Status: "failed", TriggerSource: "cron", GitBranch: "main", GitSHA: "bbbbbbb222", GithubRepo: "acme/api", StartedAt: base.Add(2 * time.Second), FinishedAt: finished(9)},
		{ID: "r-manual-feat", Pipeline: "build", Status: "running", TriggerSource: "manual", GitBranch: "feat/x", GitSHA: "ccccccc333", DeclaredRepo: "web", StartedAt: base.Add(3 * time.Second)},
		{ID: "r-norepo", Pipeline: "deploy", Status: "cancelled", GitBranch: "main", GitSHA: "aaaaaaa999", StartedAt: base.Add(4 * time.Second), FinishedAt: finished(20)},
		{ID: "r-pct_literal", Pipeline: "deploy", Status: "success", TriggerSource: "push", DeclaredRepo: "100%_done", StartedAt: base.Add(5 * time.Second), FinishedAt: finished(30)},
	}
}

func TestListRuns_DisplayFiltersSelectAcrossTheWholeStoreAndMatchTheInMemoryFilter(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	alpha := tenantFor(t, st, "alpha")
	beta := tenantFor(t, st, "beta")
	runs := displayFilterRuns()
	for _, r := range runs {
		if err := alpha.CreateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		other := r
		other.ID = "beta-" + r.ID
		if err := beta.CreateRun(ctx, other); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Unix(1700000000, 0)
	cases := []struct {
		name   string
		filter store.RunFilter
		want   []string
	}{
		{"trigger", store.RunFilter{TriggerSources: []string{"push"}}, []string{"r-pct_literal", "r-push-main"}},
		{"exclude trigger keeps runs without one", store.RunFilter{ExcludeTriggerSources: []string{"push", "cron"}}, []string{"r-norepo", "r-manual-feat"}},
		{"exclude status", store.RunFilter{ExcludeStatuses: []string{"success", "running"}}, []string{"r-norepo", "r-cron-main"}},
		{"exclude pipeline", store.RunFilter{ExcludePipelines: []string{"build", "deploy"}}, []string{"r-cron-main"}},
		{"exclude branch", store.RunFilter{ExcludeGitBranches: []string{"main"}}, []string{"r-pct_literal", "r-manual-feat"}},
		{"exclude sha prefix", store.RunFilter{ExcludeGitSHAPrefixes: []string{"AAAAAAA"}}, []string{"r-pct_literal", "r-manual-feat", "r-cron-main"}},
		{"repo name is the last segment", store.RunFilter{RepoNames: []string{"web"}}, []string{"r-manual-feat", "r-push-main"}},
		{"repo name falls back to the GitHub repository", store.RunFilter{RepoNames: []string{"api"}}, []string{"r-cron-main"}},
		{"repo name of a run without one", store.RunFilter{RepoNames: []string{"unknown"}}, []string{"r-norepo"}},
		{"repo name wildcards are literal", store.RunFilter{RepoNames: []string{"100%_done"}}, []string{"r-pct_literal"}},
		{"exclude repo name", store.RunFilter{ExcludeRepoNames: []string{"web", "unknown"}}, []string{"r-pct_literal", "r-cron-main"}},
		{"started range is inclusive", store.RunFilter{Since: base.Add(2 * time.Second), StartedBefore: base.Add(4 * time.Second)}, []string{"r-norepo", "r-manual-feat", "r-cron-main"}},
		{"finished range drops unfinished runs", store.RunFilter{FinishedAfter: base.Add(5 * time.Second), FinishedBefore: base.Add(20 * time.Second)}, []string{"r-norepo", "r-cron-main", "r-push-main"}},
		{"text matches any field case-insensitively", store.RunFilter{Text: []string{"NIGHT"}}, []string{"r-cron-main"}},
		{"every text term must match", store.RunFilter{Text: []string{"main", "build"}}, []string{"r-push-main"}},
		{"excluded text", store.RunFilter{Text: []string{"main"}, ExcludeText: []string{"FAILED"}}, []string{"r-norepo", "r-push-main"}},
		{"text wildcards are literal", store.RunFilter{Text: []string{"t_l"}}, []string{"r-pct_literal"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := alpha.ListRuns(ctx, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if ids := runIDs(got); !slices.Equal(ids, tc.want) {
				t.Errorf("ListRuns = %v, want %v", ids, tc.want)
			}
			all := make([]*store.Run, len(runs))
			for i := range runs {
				all[i] = &runs[i]
			}
			mem, err := store.FilterRuns(all, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if ids := runIDs(mem); !slices.Equal(ids, tc.want) {
				t.Errorf("FilterRuns = %v, want %v", ids, tc.want)
			}
			if n, err := alpha.CountRuns(ctx, tc.filter); err != nil || n != len(tc.want) {
				t.Errorf("CountRuns = %d, %v; want %d", n, err, len(tc.want))
			}
		})
	}
}

func TestListRuns_BeforeCursorWalksBackToTheNewestRunExactlyOnce(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	alpha := tenantFor(t, st, "alpha")
	beta := tenantFor(t, st, "beta")
	const total = 23
	instant := time.Unix(1700000000, 0)
	var all []*store.Run
	for i := range total {
		r := store.Run{ID: "run-" + strconv.Itoa(100+i), Pipeline: "build", Status: "success", StartedAt: instant.Add(time.Duration(i/3) * time.Second)}
		if err := alpha.CreateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		other := r
		other.ID = "beta-" + r.ID
		if err := beta.CreateRun(ctx, other); err != nil {
			t.Fatal(err)
		}
		all = append(all, &r)
	}
	newestFirst, err := alpha.ListRuns(ctx, store.RunFilter{Limit: total + 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(newestFirst) != total {
		t.Fatalf("listed %d runs, want %d", len(newestFirst), total)
	}

	for _, list := range []struct {
		name string
		page func(store.RunFilter) ([]*store.Run, error)
	}{
		{"store", func(f store.RunFilter) ([]*store.Run, error) { return alpha.ListRuns(ctx, f) }},
		{"in memory", func(f store.RunFilter) ([]*store.Run, error) { return store.FilterRuns(all, f) }},
	} {
		t.Run(list.name, func(t *testing.T) {
			oldest := newestFirst[total-1]
			filter := store.RunFilter{Limit: 5, BeforeID: oldest.ID, BeforeStartedAt: store.RunCursorKey(oldest.StartedAt)}
			var walked []string
			for range total {
				page, err := list.page(filter)
				if err != nil {
					t.Fatal(err)
				}
				if len(page) == 0 {
					break
				}
				walked = append(runIDs(page), walked...)
				first := page[0]
				filter.BeforeID, filter.BeforeStartedAt = first.ID, store.RunCursorKey(first.StartedAt)
			}
			if want := runIDs(newestFirst[:total-1]); !slices.Equal(walked, want) {
				t.Errorf("walked %v, want %v", walked, want)
			}
		})
	}

	if _, err := alpha.ListRuns(ctx, store.RunFilter{AfterID: "a", BeforeID: "b"}); err == nil {
		t.Error("a listing paging both ways was accepted")
	}
}

func TestParseRunFilterValidated_ReadsTheDisplayFilters(t *testing.T) {
	q := url.Values{
		"exclude_pipeline":       {"a,b"},
		"exclude_status":         {"failed"},
		"exclude_git_sha":        {"abc"},
		"exclude_git_branch":     {"main"},
		"trigger_source":         {"push,cron"},
		"exclude_trigger_source": {"manual"},
		"repo_name":              {"web"},
		"exclude_repo_name":      {"api"},
		"started_after":          {"2026-01-02T03:04:05Z"},
		"started_before":         {"2026-01-03T00:00:00Z"},
		"finished_after":         {"2026-01-02T03:04:05.5Z"},
		"finished_before":        {"2026-01-04T00:00:00+02:00"},
		"q":                      {"deploy -flaky - timeout"},
		"before_id":              {"run-9"},
		"before_started_at":      {"123"},
	}
	f, err := store.ParseRunFilterValidated(q)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.ExcludePipelines, []string{"a", "b"}) || !slices.Equal(f.TriggerSources, []string{"push", "cron"}) ||
		!slices.Equal(f.ExcludeStatuses, []string{"failed"}) || !slices.Equal(f.ExcludeGitSHAPrefixes, []string{"abc"}) ||
		!slices.Equal(f.ExcludeGitBranches, []string{"main"}) || !slices.Equal(f.ExcludeTriggerSources, []string{"manual"}) ||
		!slices.Equal(f.RepoNames, []string{"web"}) || !slices.Equal(f.ExcludeRepoNames, []string{"api"}) {
		t.Errorf("lists = %+v", f)
	}
	if !f.Since.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) || !f.StartedBefore.Equal(time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)) ||
		!f.FinishedAfter.Equal(time.Date(2026, 1, 2, 3, 4, 5, 5e8, time.UTC)) || !f.FinishedBefore.Equal(time.Date(2026, 1, 3, 22, 0, 0, 0, time.UTC)) {
		t.Errorf("times = %v %v %v %v", f.Since, f.StartedBefore, f.FinishedAfter, f.FinishedBefore)
	}
	if !slices.Equal(f.Text, []string{"deploy"}) || !slices.Equal(f.ExcludeText, []string{"flaky", "timeout"}) {
		t.Errorf("text = %v, exclude %v", f.Text, f.ExcludeText)
	}
	if f.BeforeID != "run-9" || f.BeforeStartedAt != 123 {
		t.Errorf("before cursor = %q %d", f.BeforeID, f.BeforeStartedAt)
	}

	for name, bad := range map[string]url.Values{
		"time":             {"finished_after": {"yesterday"}},
		"half cursor":      {"before_id": {"run-9"}},
		"both cursors":     {"before_id": {"a"}, "before_started_at": {"1"}, "after_id": {"b"}, "after_started_at": {"1"}},
		"excluded sha":     {"exclude_git_sha": {"xyz"}},
		"cursor no number": {"before_id": {"a"}, "before_started_at": {"now"}},
	} {
		if _, err := store.ParseRunFilterValidated(bad); err == nil {
			t.Errorf("%s: accepted %v", name, bad)
		}
	}
}
