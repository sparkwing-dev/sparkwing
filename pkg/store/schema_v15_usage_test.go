package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TestSchemaV15_UpgradeOfARealV14ShapeAddsUsageColumns reconstructs what a
// v14 database actually holds -- node rows with no exit accounting at all --
// opens it with the current binary, and asserts the upgrade adds the two
// columns without disturbing the rows carried across it. The columns are
// additive and default to zero, which every reader treats as "no process was
// supervised" rather than as a node that cost nothing.
func TestSchemaV15_UpgradeOfARealV14ShapeAddsUsageColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "real14.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "legacy", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	if err := st.FinishNode(ctx, "r1", "build", "success", "", []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("finish node: %v", err)
	}
	for _, col := range []string{"cpu_nanos", "max_rss_bytes"} {
		if _, err := st.DB().Exec(`ALTER TABLE nodes DROP COLUMN ` + col); err != nil {
			t.Fatalf("drop %s: %v", col, err)
		}
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 15`); err != nil {
		t.Fatalf("reset version to 14: %v", err)
	}
	if v := readSchemaVersion(t, st.DB()); v != 14 {
		t.Fatalf("seeded version = %d, want 14", v)
	}
	if hasColumn(t, st, "nodes", "cpu_nanos") {
		t.Fatal("cpu_nanos should be absent before the upgrade")
	}
	_ = st.Close()

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#2 (upgrade): %v", err)
	}
	defer func() { _ = up.Close() }()

	if v := readSchemaVersion(t, up.DB()); v != store.ExpectedSchemaVersion() {
		t.Errorf("version after upgrade = %d, want %d", v, store.ExpectedSchemaVersion())
	}
	for _, col := range []string{"cpu_nanos", "max_rss_bytes"} {
		if !hasColumn(t, up, "nodes", col) {
			t.Fatalf("%s should be present after the upgrade", col)
		}
	}
	n, err := up.GetNode(ctx, "r1", "build")
	if err != nil {
		t.Fatalf("GetNode after upgrade: %v", err)
	}
	if n.Outcome != "success" || string(n.Output) != `{"ok":true}` {
		t.Errorf("carried node = %+v, want its outcome and output unchanged", n)
	}
	if n.CPUNanos != 0 || n.MaxRSSBytes != 0 {
		t.Errorf("carried node usage = %d/%d, want zeroes (nothing measured it)", n.CPUNanos, n.MaxRSSBytes)
	}
}

// TestSchemaV15_UpgradeIsSafeToReplay covers the SQLite path applying
// migration steps outside a transaction: a crash between the column add and
// the version stamp replays the step, which must neither fail on the columns
// already being there nor overwrite a measurement already recorded.
func TestSchemaV15_UpgradeIsSafeToReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "measured", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	if err := st.AddNodeUsage(ctx, "r1", "build", store.NodeUsage{
		CPUTime: 1500 * time.Millisecond, MaxRSSBytes: 42 << 20, Wall: 2 * time.Second,
	}); err != nil {
		t.Fatalf("AddNodeUsage: %v", err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 15`); err != nil {
		t.Fatalf("rewind version stamp: %v", err)
	}
	_ = st.Close()

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#2 (replay): %v", err)
	}
	defer func() { _ = up.Close() }()

	n, err := up.GetNode(ctx, "r1", "build")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.CPUNanos != int64(1500*time.Millisecond) || n.MaxRSSBytes != 42<<20 {
		t.Errorf("usage after replay = %d/%d, want the measured 1.5s / 42MiB preserved", n.CPUNanos, n.MaxRSSBytes)
	}
}

// TestAddNodeUsage_RoundTripsAndSurvivesFinishNode pins the write order the
// runner actually uses: the node's own process writes the terminal row from
// inside itself, and the supervisor completes that row with what the kernel
// charged the process only once it has been reaped. The later write must not
// disturb the outcome, and the terminal write must not clear the usage when
// the two land in the other order.
func TestAddNodeUsage_RoundTripsAndSurvivesFinishNode(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"after", "before"} {
		if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: id, Status: "pending"}); err != nil {
			t.Fatal(err)
		}
	}

	if err := st.FinishNode(ctx, "r1", "after", "success", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := st.AddNodeUsage(ctx, "r1", "after", store.NodeUsage{
		CPUTime: 2 * time.Second, MaxRSSBytes: 128 << 20, Wall: 3 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddNodeUsage(ctx, "r1", "before", store.NodeUsage{
		CPUTime: 3 * time.Second, MaxRSSBytes: 256 << 20, Wall: 4 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishNode(ctx, "r1", "before", "success", "", nil); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		id     string
		cpu    time.Duration
		maxRSS int64
		wall   time.Duration
	}{
		{"after", 2 * time.Second, 128 << 20, 3 * time.Second},
		{"before", 3 * time.Second, 256 << 20, 4 * time.Second},
	} {
		n, err := st.GetNode(ctx, "r1", tc.id)
		if err != nil {
			t.Fatalf("GetNode(%s): %v", tc.id, err)
		}
		if n.CPUNanos != int64(tc.cpu) {
			t.Errorf("node %q CPUNanos = %d, want %d", tc.id, n.CPUNanos, int64(tc.cpu))
		}
		if n.MaxRSSBytes != tc.maxRSS {
			t.Errorf("node %q MaxRSSBytes = %d, want %d", tc.id, n.MaxRSSBytes, tc.maxRSS)
		}
		if n.ProcessWallNanos != int64(tc.wall) {
			t.Errorf("node %q ProcessWallNanos = %d, want %d", tc.id, n.ProcessWallNanos, int64(tc.wall))
		}
		if n.Outcome != "success" {
			t.Errorf("node %q Outcome = %q, want success", tc.id, n.Outcome)
		}
	}

	nodes, err := st.ListNodes(ctx, "r1")
	if err != nil || len(nodes) != 2 {
		t.Fatalf("ListNodes = %d nodes, %v", len(nodes), err)
	}
	if nodes[0].CPUNanos == 0 || nodes[1].CPUNanos == 0 {
		t.Error("ListNodes dropped the usage columns the fold reads from")
	}
}

// TestAddNodeUsage_RejectsNegativeFigures keeps a bad reading from being
// stored as one: a negative duration or size is not a measurement, and the
// fold reads zero as "absent" rather than as a number to divide by.
func TestAddNodeUsage_RejectsNegativeFigures(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddNodeUsage(ctx, "r1", "build", store.NodeUsage{
		CPUTime: -time.Second, MaxRSSBytes: -1, Wall: -time.Second,
	}); err != nil {
		t.Fatalf("AddNodeUsage: %v", err)
	}
	n, err := st.GetNode(ctx, "r1", "build")
	if err != nil {
		t.Fatal(err)
	}
	if n.CPUNanos != 0 || n.MaxRSSBytes != 0 || n.ProcessWallNanos != 0 {
		t.Errorf("usage = %d/%d/%d, want every figure clamped to zero",
			n.CPUNanos, n.MaxRSSBytes, n.ProcessWallNanos)
	}
}

// TestAddNodeUsage_AccumulatesAcrossAttempts covers a node executed more than
// once. An auto-retry spawns a fresh process per attempt and the machine paid
// for all of them, so CPU and occupancy add up; peak RSS does not, because the
// attempts did not hold their peaks at the same time. Overwriting instead
// would price a node that burned four core-seconds across two attempts as
// whichever attempt happened to report last.
func TestAddNodeUsage_AccumulatesAcrossAttempts(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "flaky", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []store.NodeUsage{
		{CPUTime: 3 * time.Second, MaxRSSBytes: 512 << 20, Wall: 4 * time.Second},
		{CPUTime: time.Second, MaxRSSBytes: 128 << 20, Wall: 2 * time.Second},
	} {
		if err := st.AddNodeUsage(ctx, "r1", "flaky", attempt); err != nil {
			t.Fatal(err)
		}
	}

	n, err := st.GetNode(ctx, "r1", "flaky")
	if err != nil {
		t.Fatal(err)
	}
	if n.CPUNanos != int64(4*time.Second) {
		t.Errorf("CPUNanos = %d, want both attempts summed (%d)", n.CPUNanos, int64(4*time.Second))
	}
	if n.ProcessWallNanos != int64(6*time.Second) {
		t.Errorf("ProcessWallNanos = %d, want both attempts summed (%d)", n.ProcessWallNanos, int64(6*time.Second))
	}
	if n.MaxRSSBytes != 512<<20 {
		t.Errorf("MaxRSSBytes = %d, want the 512MiB high-water, not the last attempt", n.MaxRSSBytes)
	}
}

// TestNodeMetricSample_CPUTimeRoundTrips keeps the one-shot marker durable.
// The fold tells a per-command report from a sampler tick by this column
// alone, so a sample that lost it on the way to the store would be summed as
// a rate and price a run for concurrency it never had.
func TestNodeMetricSample_CPUTimeRoundTrips(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	if err := st.AddNodeMetricSample(ctx, "r1", "build", store.MetricSample{
		TS: base, CPUMillicores: 2000, MemoryBytes: 1 << 30, CPUTime: 800 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddNodeMetricSample(ctx, "r1", "build", store.MetricSample{
		TS: base.Add(time.Second), CPUMillicores: 500, MemoryBytes: 1 << 30,
	}); err != nil {
		t.Fatal(err)
	}

	samples, err := st.ListNodeMetrics(ctx, "r1", "build")
	if err != nil || len(samples) != 2 {
		t.Fatalf("ListNodeMetrics = %d samples, %v", len(samples), err)
	}
	if !samples[0].OneShot() || samples[0].CPUTime != 800*time.Millisecond {
		t.Errorf("first sample = %+v, want a one-shot carrying 800ms of CPU", samples[0])
	}
	if samples[1].OneShot() {
		t.Errorf("second sample = %+v, want a sampler tick (no CPU duration)", samples[1])
	}
}
