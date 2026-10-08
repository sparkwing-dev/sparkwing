package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func runsFoldHome(t *testing.T, runs ...store.Run) orchestrator.Paths {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_CONFIG", home+"/config.yaml")
	t.Setenv("SPARKWING_PROFILE", "")
	t.Chdir(home)
	paths := orchestrator.PathsAt(home)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := teststore.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if err := st.CreateRun(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return paths
}

func TestRunsStatusFollowTimeoutExitsTwo(t *testing.T) {
	runsFoldHome(t, store.Run{ID: "run-busy", Pipeline: "build", Status: "running", StartedAt: time.Now()})
	var err error
	captureStdout(t, func() {
		err = runJobs([]string{"status", "run-busy", "--follow", "--timeout", "50ms", "--poll", "10ms", "-o", "json"})
	})
	if exitCodeFor(err) != 2 {
		t.Fatalf("err = %v (exit %d), want exit 2 on timeout", err, exitCodeFor(err))
	}
}

func TestRunsStatusFollowReturnsTheTerminalRecord(t *testing.T) {
	runsFoldHome(t, store.Run{ID: "run-done", Pipeline: "build", Status: "failed", StartedAt: time.Now()})
	var err error
	out := captureStdout(t, func() {
		err = runJobs([]string{"status", "run-done", "--follow", "--timeout", "1m", "-o", "json"})
	})
	if exitCodeFor(err) != 1 || !strings.Contains(out, `"run-done"`) {
		t.Fatalf("out=%q err=%v, want the run record and exit 1 for a failed run", out, err)
	}
}

func TestRunsStatusTimeoutNeedsFollow(t *testing.T) {
	runsFoldHome(t)
	if err := runJobs([]string{"status", "run-x", "--timeout", "1m"}); err == nil || !strings.Contains(err.Error(), "--follow") {
		t.Fatalf("err = %v, want --timeout to require --follow", err)
	}
}

func TestRunsStatusRejectsAnUnknownView(t *testing.T) {
	runsFoldHome(t, store.Run{ID: "run-done", Pipeline: "build", Status: "success", StartedAt: time.Now()})
	if err := runJobs([]string{"status", "run-done", "--view", "gantt"}); err == nil || !strings.Contains(err.Error(), "summary|timeline|receipt|errors|tree") {
		t.Fatalf("err = %v, want the view list", err)
	}
}

func TestRunsListFiltersByRepositoryAndRootOnly(t *testing.T) {
	runsFoldHome(t,
		store.Run{ID: "run-web", Pipeline: "build", Status: "success", StartedAt: time.Now(), DeclaredRepo: "acme/web"},
		store.Run{ID: "run-api", Pipeline: "build", Status: "success", StartedAt: time.Now(), DeclaredRepo: "acme/api"},
		store.Run{ID: "run-child", Pipeline: "build", Status: "success", StartedAt: time.Now(), DeclaredRepo: "acme/web", ParentRunID: "run-web"},
	)
	out := captureStdout(t, func() {
		if err := runJobs([]string{"list", "--repo", "acme/web", "--root-only", "-q", "-o", "plain"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.TrimSpace(out) != "run-web" {
		t.Fatalf("list = %q, want only the root run of acme/web", out)
	}
}

func TestRunsListWaitTimesOutWithExitTwo(t *testing.T) {
	runsFoldHome(t)
	err := runJobs([]string{"list", "--pipeline", "never", "--wait", "--wait-timeout", "10ms"})
	if exitCodeFor(err) != 2 {
		t.Fatalf("err = %v, want exit 2 when no run appears", err)
	}
}

func TestRunsListGroupByNeedsFailedStatus(t *testing.T) {
	runsFoldHome(t)
	if err := runJobs([]string{"list", "--group-by", "step"}); err == nil || !strings.Contains(err.Error(), "--status failed") {
		t.Fatalf("err = %v, want --group-by to require --status failed", err)
	}
}

func TestRunsLogsWithoutRunNeedsGrep(t *testing.T) {
	runsFoldHome(t)
	if err := runJobs([]string{"logs"}); err == nil || !strings.Contains(err.Error(), "--grep") {
		t.Fatalf("err = %v, want a pointer at --run or --grep", err)
	}
	if err := runJobs([]string{"logs", "--run", "run-x", "--pipeline", "build"}); err == nil || !strings.Contains(err.Error(), "without --run") {
		t.Fatalf("err = %v, want search flags refused with --run", err)
	}
}

func TestRunsListGroupByHonoursTheListingFilters(t *testing.T) {
	paths := runsFoldHome(t,
		store.Run{ID: "run-denied", Pipeline: "build", Status: "running", StartedAt: time.Now()},
		store.Run{ID: "run-timeout", Pipeline: "build", Status: "running", StartedAt: time.Now()},
	)
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	for id, msg := range map[string]string{"run-denied": "permission denied", "run-timeout": "deadline exceeded"} {
		if err := st.FinishRun(context.Background(), id, "failed", msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := runJobs([]string{"list", "--status", "failed", "--group-by", "run", "--error", "permission", "-o", "json"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "run-denied") || strings.Contains(out, "run-timeout") {
		t.Fatalf("failures = %q, want only the run whose error matches --error", out)
	}
}

func TestWatchedRunHonoursQuiet(t *testing.T) {
	run := &store.Run{ID: "run-new", Pipeline: "build", Status: "running", StartedAt: time.Now()}
	for _, tc := range []struct {
		json, quiet bool
		want        string
	}{
		{false, true, "run-new\n"},
		{true, true, "\"run-new\"\n"},
	} {
		var buf bytes.Buffer
		if err := writeWatchedRun(&buf, run, tc.json, tc.quiet); err != nil {
			t.Fatal(err)
		}
		if buf.String() != tc.want {
			t.Errorf("json=%v quiet=%v: wrote %q, want %q", tc.json, tc.quiet, buf.String(), tc.want)
		}
	}
	var full bytes.Buffer
	if err := writeWatchedRun(&full, run, true, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(full.String(), `"pipeline":"build"`) {
		t.Errorf("non-quiet JSON = %q, want the run record", full.String())
	}
}

func TestRunsLogsSinceAcceptsDays(t *testing.T) {
	runsFoldHome(t)
	var err error
	captureStdout(t, func() {
		err = runJobs([]string{"logs", "--grep", "boom", "--since", "7d", "-o", "json"})
	})
	if err != nil {
		t.Fatalf("runs logs --grep --since 7d: %v", err)
	}
}

func TestRunsListGroupByKeepsTheStoresNormalizedSHAMatch(t *testing.T) {
	paths := runsFoldHome(t,
		store.Run{ID: "run-sha", Pipeline: "build", Status: "running", StartedAt: time.Now(), GitSHA: "abc123def456"},
	)
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRun(context.Background(), "run-sha", "failed", "boom"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := runJobs([]string{"list", "--status", "failed", "--group-by", "run", "--sha", " abc123 ", "-o", "json"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "run-sha") {
		t.Fatalf("failures = %q, want the run the store matched on its trimmed SHA prefix", out)
	}
}

func TestWalkRunPagesFindsAMatchBehindFullPagesOfMisses(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	var seeded []store.Run
	for i := 0; i < 7; i++ {
		seeded = append(seeded, store.Run{ID: fmt.Sprintf("run-%d", i), Pipeline: "build", Status: "failed", StartedAt: base.Add(time.Duration(i) * time.Minute)})
	}
	paths := runsFoldHome(t, seeded...)
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	fetch := func(f store.RunFilter) ([]orchestrator.TaggedRun, bool, error) {
		runs, err := st.ListRuns(context.Background(), f)
		return orchestrator.TagShared(runs), len(runs) < f.Limit, err
	}
	keep := func(r *store.Run) bool { return r.ID == "run-0" || r.ID == "run-1" }
	kept, err := walkRunPages(store.RunFilter{Limit: 2}, 5, fetch, keep)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range kept {
		got = append(got, r.ID)
	}
	if strings.Join(got, ",") != "run-1,run-0" {
		t.Fatalf("walk kept %v, want the two oldest runs found behind pages of misses", got)
	}
}

func TestRemoteFailuresStopOnAShortPageWithoutACursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/runs" {
			if r.URL.Query().Get("after_id") != "" {
				t.Errorf("walk sent a cursor after a short page: %s", r.URL.RawQuery)
			}
			w.Header().Set("X-Sparkwing-Run-Filter-Version", "1")
			_, _ = w.Write([]byte(`{"runs":[{"id":"run-old","pipeline":"build","status":"failed"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"nodes":[]}`))
	}))
	t.Cleanup(srv.Close)
	rows, err := collectRemoteFailures(context.Background(), srv.URL, "", store.RunFilter{Statuses: []string{"failed"}, Limit: 20}, 20,
		func(*store.Run) bool { return true })
	if err != nil {
		t.Fatalf("a cursorless controller with one failure: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "run-old" {
		t.Fatalf("rows = %+v, want the one failed run", rows)
	}
}

func TestWalkRunPagesKeepsOneRowPerRunPreferringTheSharedCopy(t *testing.T) {
	now := time.Now()
	row := func(id, label string, at time.Time) orchestrator.TaggedRun {
		return orchestrator.TaggedRun{Run: &store.Run{ID: id, StartedAt: at}, Store: label}
	}
	for name, pages := range map[string][][]orchestrator.TaggedRun{
		"shared copy first": {{row("run-x", orchestrator.SharedStoreLabel, now)}, {row("run-x", "standalone/a", now.Add(-time.Minute))}},
		"shared copy later": {{row("run-x", "standalone/a", now)}, {row("run-x", orchestrator.SharedStoreLabel, now.Add(-time.Minute))}},
	} {
		t.Run(name, func(t *testing.T) {
			call := 0
			fetch := func(store.RunFilter) ([]orchestrator.TaggedRun, bool, error) {
				if call >= len(pages) {
					return nil, true, nil
				}
				call++
				return pages[call-1], false, nil
			}
			kept, err := walkRunPages(store.RunFilter{Limit: 1}, 10, fetch, func(*store.Run) bool { return true })
			if err != nil {
				t.Fatal(err)
			}
			if len(kept) != 1 || kept[0].Store != orchestrator.SharedStoreLabel {
				t.Fatalf("kept %+v, want the run once, from the shared store", kept)
			}
		})
	}
}
