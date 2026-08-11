package cachepressure

import (
	"context"
	"os"
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
