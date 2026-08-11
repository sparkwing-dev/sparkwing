package cachepressure

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
)

func seedEntry(t *testing.T, body string) *bincache.Lease {
	t.Helper()
	entry, err := bincache.PipelineEntry("11111111-11111111")
	if err != nil {
		t.Fatal(err)
	}
	lease, _, err := entry.AcquireOrMaterialize(context.Background(), func(path string) error {
		return os.WriteFile(path, []byte(body), 0o755)
	})
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func TestMeasureReportsActiveManagedBytes(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	lease := seedEntry(t, "active")
	defer func() { _ = lease.Release() }()

	status, err := Measure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.ObservedBytes != 6 || status.ActiveBytes != 6 || status.ActiveEntries != 1 {
		t.Fatalf("status = %#v", status)
	}
}

func TestPruneReclaimsWithinCallerBounds(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	lease := seedEntry(t, "remove")
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}

	result, err := Prune(context.Background(), PruneOptions{ReclaimBytes: 1, MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reclaimed != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestPruneResultJSONNamesAccountingAndSkips(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(PruneResult{})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"logical_removed_bytes",
		"observed_capacity_gained_bytes",
		"active_skipped_entries",
		"busy_skipped_entries",
		"work_bound_exhausted",
	} {
		if _, ok := fields[name]; !ok {
			t.Errorf("PruneResult JSON is missing %q: %s", name, raw)
		}
	}
	for _, obsolete := range []string{"removed_bytes", "reclaimed_bytes", "active_entries", "busy_entries", "exhausted"} {
		if _, ok := fields[obsolete]; ok {
			t.Errorf("PruneResult JSON retains ambiguous field %q: %s", obsolete, raw)
		}
	}
}

func TestPruneResultGoNamesAccountingAndWork(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf(PruneResult{})
	for _, name := range []string{
		"ObservedBytes",
		"LogicalRemovedBytes",
		"ObservedCapacityGainedBytes",
		"ExaminedEntries",
		"ReclaimedEntries",
		"ActiveSkippedEntries",
		"BusySkippedEntries",
		"GoalSatisfied",
		"WorkBoundExhausted",
	} {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("PruneResult is missing %s", name)
		}
	}
	for _, obsolete := range []string{"RemovedBytes", "ReclaimedBytes", "Examined", "Reclaimed", "Active", "Busy", "Exhausted"} {
		if _, ok := typ.FieldByName(obsolete); ok {
			t.Errorf("PruneResult retains ambiguous field %s", obsolete)
		}
	}
}
