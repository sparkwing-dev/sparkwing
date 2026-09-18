package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func seedListRuns(t *testing.T, path string, n int) {
	t.Helper()
	seedRuns(t, path, "run-page", n, time.Now().Add(-24*time.Hour))
}

func seedStandaloneRuns(t *testing.T, path string, n int) {
	t.Helper()
	seedRuns(t, path, "run-standalone", n, time.Now().Add(-48*time.Hour))
}

func seedRuns(t *testing.T, path, prefix string, n int, base time.Time) {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	defer func() { _ = st.Close() }()
	for i := range n {
		if err := st.CreateRun(context.Background(), store.Run{
			ID:        fmt.Sprintf("%s-%04d", prefix, i),
			Pipeline:  "demo",
			Status:    "success",
			StartedAt: base.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("seed CreateRun: %v", err)
		}
	}
}

type listing struct {
	ids     []string
	page    RunsPage
	rows    []PipelinePivotRow
	summary PipelinePivotSummary
}

func decodeListing(t *testing.T, raw string) listing {
	t.Helper()
	var out listing
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if line == "" {
			continue
		}
		var probe struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		switch {
		case probe.Kind == "page":
			mustDecode(t, line, &out.page)
		case probe.Kind == "summary":
			mustDecode(t, line, &out.summary)
		case probe.ID != "":
			out.ids = append(out.ids, probe.ID)
		default:
			var row PipelinePivotRow
			mustDecode(t, line, &row)
			out.rows = append(out.rows, row)
		}
	}
	return out
}

func mustDecode(t *testing.T, line string, into any) {
	t.Helper()
	if err := json.Unmarshal([]byte(line), into); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
}

func listPage(t *testing.T, p *profile.Profile, opts ListOpts) ([]string, RunsPage) {
	t.Helper()
	opts.Profile = p
	opts.JSON = true
	var buf bytes.Buffer
	if err := ListJobs(context.Background(), Paths{Root: t.TempDir()}, opts, &buf); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	got := decodeListing(t, buf.String())
	if got.page.Kind != "page" {
		t.Fatal("listing carried no kind:page record, so a caller cannot tell a cut result from a whole one")
	}
	return got.ids, got.page
}

func newSeededProfile(t *testing.T, runs int) *profile.Profile {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "page-state.db")
	seedListRuns(t, dbPath, runs)
	return &profile.Profile{Name: "local", State: &backends.Spec{Type: backends.TypeSQLite, Path: dbPath}}
}

func TestListJobs_CursorWalksEveryRunExactlyOnce(t *testing.T) {
	p := newSeededProfile(t, 25)

	seen := map[string]int{}
	cursor := ""
	for page := 0; page < 20; page++ {
		ids, meta := listPage(t, p, ListOpts{Limit: 7, Cursor: cursor})
		for _, id := range ids {
			seen[id]++
		}
		if !meta.Truncated {
			break
		}
		if meta.NextCursor == cursor && cursor != "" {
			t.Fatal("next_cursor did not advance, so paging cannot terminate")
		}
		cursor = meta.NextCursor
	}

	if len(seen) != 25 {
		t.Errorf("walked %d distinct runs across pages, want 25", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("run %s appeared %d times across pages, want once", id, n)
		}
	}
}

func TestRunsPager_WindowAdvancesThroughAFullyFilteredWindow(t *testing.T) {
	pager := newRunsPager(3, CompiledFilter{})
	raw := make([]TaggedRun, 0, pager.budget)
	for i := range pager.budget {
		raw = append(raw, TaggedRun{Run: &store.Run{
			ID:        fmt.Sprintf("run-window-%04d", i),
			Pipeline:  "demo",
			Status:    "success",
			StartedAt: time.Unix(int64(1700000000+i), 0),
		}})
	}

	rejectAll := CompiledFilter{Search: ParseSearch("-demo")}
	rows, resume, more := pager.window(raw, rejectAll)

	if len(rows) != 0 {
		t.Fatalf("filter kept %d rows, want none", len(rows))
	}
	if !more {
		t.Error("a window read to its budget reported that the source held no more")
	}
	if resume == nil {
		t.Fatal("a fully filtered window offered no resume point, so a walk over it cannot terminate")
	}
	if want := raw[pager.budget-2].ID; resume.ID != want {
		t.Errorf("resume = %s, want %s, the last run examined, not the last kept", resume.ID, want)
	}
}

func TestParseRunsCursor(t *testing.T) {
	for _, raw := range []string{"", "not-a-cursor", "abc:run-1:0", "123", "1:run-1", "42::7"} {
		if _, err := parseRunsCursor(raw); err == nil {
			t.Errorf("parseRunsCursor(%q) succeeded, want a refusal", raw)
		}
	}
	got, err := parseRunsCursor("1750000000000000000:run:with:colons:99")
	if err != nil {
		t.Fatalf("parseRunsCursor: %v", err)
	}
	want := runsCursor{StartedAt: 1750000000000000000, ID: "run:with:colons", Since: 99}
	if got != want {
		t.Errorf("parseRunsCursor = %+v, want %+v", got, want)
	}
}

func TestEffectiveRunsPageLimit_NeverExceedsTheCeiling(t *testing.T) {
	for _, tc := range []struct {
		name string
		ask  int
		want int
	}{
		{name: "unset falls back to the ceiling", ask: 0, want: store.MaxRunListLimit},
		{name: "under the ceiling is honored", ask: 5, want: 5},
		{name: "at the ceiling", ask: store.MaxRunListLimit, want: store.MaxRunListLimit},
		{name: "above the ceiling is clamped", ask: store.MaxRunListLimit + 1, want: store.MaxRunListLimit},
		{name: "far above the ceiling is clamped", ask: 5000, want: store.MaxRunListLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveRunsPageLimit(tc.ask); got != tc.want {
				t.Fatalf("effectiveRunsPageLimit(%d) = %d, want %d", tc.ask, got, tc.want)
			}
		})
	}
}

func TestListJobs_TotalIsThePopulationOnEveryPage(t *testing.T) {
	p := newSeededProfile(t, 25)

	_, first := listPage(t, p, ListOpts{Limit: 10})
	if first.Total == nil || *first.Total != 25 {
		t.Fatalf("first page total = %v, want 25", first.Total)
	}
	_, second := listPage(t, p, ListOpts{Limit: 10, Cursor: first.NextCursor})
	if second.Total == nil || *second.Total != 25 {
		t.Errorf("second page total = %v, want 25, the population, not the remainder", second.Total)
	}
}

func TestListJobs_StandaloneRunsAreCountedAndRolledUp(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	paths := Paths{Root: root}
	seedListRuns(t, filepath.Join(root, "state.db"), 10)
	seedStandaloneRuns(t, standaloneDB(t, root), 5)

	list := func(opts ListOpts) listing {
		t.Helper()
		var buf bytes.Buffer
		opts.JSON = true
		if err := ListJobs(ctx, paths, opts, &buf); err != nil {
			t.Fatalf("ListJobs: %v", err)
		}
		return decodeListing(t, buf.String())
	}

	if page := list(ListOpts{Limit: 3}).page; page.Total != nil {
		t.Errorf("total = %d while the listing merges a standalone store the counter cannot see", *page.Total)
	}

	rollup := list(ListOpts{Limit: 3, ByPipeline: true, Pivot: PivotOpts{SparklineLen: 30, Style: SparkASCII}})
	total := 0
	for _, row := range rollup.rows {
		total += row.Total
	}
	if total != 15 {
		t.Errorf("rollup counted %d runs, want 15: 10 shared plus 5 standalone across pages", total)
	}
}

func standaloneDB(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "standalone")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "state.db")
}

type cursorBlindLister struct {
	page  []*store.Run
	calls int
}

func (c *cursorBlindLister) ListRuns(context.Context, store.RunFilter) ([]*store.Run, error) {
	c.calls++
	return c.page, nil
}

func TestForEachRunPage_StopsAndReportsWhenTheCursorIsIgnored(t *testing.T) {
	pager := newRunsPager(3, CompiledFilter{})
	page := make([]*store.Run, 0, pager.budget)
	for i := range pager.budget {
		page = append(page, &store.Run{
			ID:        fmt.Sprintf("run-blind-%04d", i),
			Pipeline:  "demo",
			Status:    "success",
			StartedAt: time.Unix(int64(1700000000+i), 0),
		})
	}
	lister := &cursorBlindLister{page: page}

	_, resume, more := pager.window(TagShared(page), CompiledFilter{})
	if !more || resume == nil {
		t.Fatalf("fixture did not produce a continuing window: more=%v resume=%v", more, resume)
	}

	folded := 0
	stopped, err := forEachRunPage(context.Background(), lister, pager, store.RunFilter{},
		CompiledFilter{}, resume, more, func(page []TaggedRun) { folded += len(page) })
	if err != nil {
		t.Fatalf("forEachRunPage: %v", err)
	}
	if stopped == "" {
		t.Error("the walk reported no reason, so a rollup over this store reads as complete")
	}
	if lister.calls != 1 {
		t.Errorf("walked %d pages against a store that never advances, want 1", lister.calls)
	}
	if folded != 0 {
		t.Errorf("folded %d rows the walk had already seen", folded)
	}
}

func TestListJobs_MergedWalkCountsTiedRunsOnce(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	instant := time.Now().Add(-time.Hour).Truncate(time.Second)

	shared, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	standaloneDir := filepath.Join(root, "standalone")
	if err := os.MkdirAll(standaloneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	side, err := store.Open(filepath.Join(standaloneDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	const tied = store.MaxRunListLimit + 8
	for i := range tied {
		st := shared
		if i%2 == 1 {
			st = side
		}
		if err := st.CreateRun(ctx, store.Run{
			ID: fmt.Sprintf("run-tie-%04d", i), Pipeline: "demo", Status: "success", StartedAt: instant,
		}); err != nil {
			t.Fatal(err)
		}
	}
	_ = shared.Close()
	_ = side.Close()

	var buf bytes.Buffer
	opts := ListOpts{
		JSON: true, ByPipeline: true,
		Pivot: PivotOpts{SparklineLen: 30, Style: SparkASCII},
	}
	if err := ListJobs(ctx, Paths{Root: root}, opts, &buf); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	rows := decodeListing(t, buf.String()).rows
	if len(rows) != 1 {
		t.Fatalf("rollup wrote %d pipeline rows, want 1", len(rows))
	}
	row := rows[0]
	if row.Total != tied {
		t.Errorf("rollup counted %d runs sharing one instant, want %d", row.Total, tied)
	}
}

func TestRunsCursor_CarriesTheSinceAnchorAcrossPages(t *testing.T) {
	since := time.Now().Add(-30 * 24 * time.Hour)
	run := &store.Run{ID: "run-anchor", StartedAt: time.Now().Add(-time.Hour)}

	encoded := cursorFor(run, since)
	got, err := parseRunsCursor(encoded)
	if err != nil {
		t.Fatalf("parse %q: %v", encoded, err)
	}
	if got.Since != since.UnixNano() {
		t.Errorf("cursor lost the since anchor: got %d, want %d", got.Since, since.UnixNano())
	}

	filter, _, _, err := runsQueryFor(ListOpts{Since: 30 * 24 * time.Hour, Cursor: encoded})
	if err != nil {
		t.Fatalf("runsQueryFor: %v", err)
	}
	if !filter.Since.Equal(time.Unix(0, since.UnixNano())) {
		t.Errorf("second page re-derived Since as %v, want the anchor %v", filter.Since, since)
	}

	plain := cursorFor(run, time.Time{})
	bare, err := parseRunsCursor(plain)
	if err != nil {
		t.Fatalf("parse %q: %v", plain, err)
	}
	if bare.Since != 0 {
		t.Errorf("a cursor with no window carried an anchor of %d", bare.Since)
	}
}

func TestListJobs_AnEmptyStandaloneStoreLeavesTheCountExact(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	seedListRuns(t, filepath.Join(root, "state.db"), 12)

	standaloneDir := filepath.Join(root, "standalone")
	if err := os.MkdirAll(standaloneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	side, err := store.Open(filepath.Join(standaloneDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = side.Close()

	var buf bytes.Buffer
	if err := ListJobs(ctx, Paths{Root: root}, ListOpts{Limit: 5, JSON: true}, &buf); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	var page RunsPage
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.Contains(line, `"kind":"page"`) {
			if err := json.Unmarshal([]byte(line), &page); err != nil {
				t.Fatalf("decode page: %v", err)
			}
		}
	}
	if page.Total == nil {
		t.Fatal("total was omitted although the only standalone store holds nothing")
	}
	if *page.Total != 12 {
		t.Errorf("total = %d, want 12", *page.Total)
	}
}

func TestStandaloneStores_ADroppedStoreIsReportedNotSwallowed(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	standaloneDir := filepath.Join(root, "standalone")
	if err := os.MkdirAll(standaloneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(standaloneDir, "state.db")
	side, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := side.CreateRun(ctx, store.Run{
		ID: "run-side", Pipeline: "demo", Status: "success", StartedAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	_ = side.Close()

	stores := OpenStandaloneStores(ctx, Paths{Root: root})
	t.Cleanup(func() { _ = stores.Close() })
	if got := len(stores.ListRuns(ctx, store.RunFilter{Limit: 10})); got != 1 {
		t.Fatalf("read %d runs before the store was lost, want 1", got)
	}
	if stores.Failed() {
		t.Fatal("a healthy store reported itself failed")
	}

	if err := os.WriteFile(dbPath, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		stores.ListRuns(ctx, store.RunFilter{Limit: 10})
	}
	if !stores.Failed() {
		t.Error("a store that stopped answering was swallowed, so a rollup would call its totals whole")
	}
	if n := len(stores.Notes()); n != 1 {
		t.Errorf("three failing reads left %d notes, want one per store", n)
	}
}

func TestListJobs_PageRecord(t *testing.T) {
	for _, tc := range []struct {
		name      string
		seeded    int
		opts      ListOpts
		returned  int
		limit     int
		truncated bool
		total     *int
	}{
		{
			name:   "a page smaller than the population is cut and says so",
			seeded: 25, opts: ListOpts{Limit: 10},
			returned: 10, limit: 10, truncated: true, total: ptr(25),
		},
		{
			name:   "a page holding every match is not cut",
			seeded: 25, opts: ListOpts{Limit: 100},
			returned: 25, limit: 100, truncated: false, total: ptr(25),
		},
		{
			name:   "a request above the ceiling is served at the ceiling",
			seeded: 25, opts: ListOpts{Limit: 5000},
			returned: 25, limit: store.MaxRunListLimit, truncated: false, total: ptr(25),
		},
		{
			name:   "a page filled to the ceiling with more behind it is cut",
			seeded: store.MaxRunListLimit + 50, opts: ListOpts{Limit: store.MaxRunListLimit},
			returned: store.MaxRunListLimit, limit: store.MaxRunListLimit, truncated: true,
			total: ptr(store.MaxRunListLimit + 50),
		},
		{
			name:   "a filter this listing applies in Go omits the total",
			seeded: 25, opts: ListOpts{Limit: 10, Filter: CompiledFilter{Search: ParseSearch("demo")}},
			returned: 10, limit: 10, truncated: true, total: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, page := listPage(t, newSeededProfile(t, tc.seeded), tc.opts)
			if page.Returned != tc.returned {
				t.Errorf("returned = %d, want %d", page.Returned, tc.returned)
			}
			if page.Limit != tc.limit {
				t.Errorf("limit = %d, want %d", page.Limit, tc.limit)
			}
			if page.Truncated != tc.truncated {
				t.Errorf("truncated = %v, want %v", page.Truncated, tc.truncated)
			}
			if tc.truncated && page.NextCursor == "" {
				t.Error("a cut page carried no next_cursor, so the caller cannot page past it")
			}
			if !tc.truncated && page.NextCursor != "" {
				t.Error("an exhausted listing offered a next_cursor to nowhere")
			}
			switch {
			case tc.total == nil && page.Total != nil:
				t.Errorf("total = %d where it cannot be counted exactly", *page.Total)
			case tc.total != nil && page.Total == nil:
				t.Error("page carried no total, so a caller cannot tell a page from a population")
			case tc.total != nil && *page.Total != *tc.total:
				t.Errorf("total = %d, want %d", *page.Total, *tc.total)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
