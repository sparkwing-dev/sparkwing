package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopySQLiteSnapshotReturnsHardReinspectionErrorWithoutRetry(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	destinationPath := filepath.Join(t.TempDir(), "state.db")
	if err := preparePrivateSQLite(destinationPath); err != nil {
		t.Fatal(err)
	}

	hardErr := errors.New("injected stable reinspection I/O failure")
	calls := 0
	inspect := func(path string, optional bool) (snapshotSource, error) {
		calls++
		if calls == 3 {
			return snapshotSource{}, hardErr
		}
		return inspectSnapshotSource(path, optional)
	}
	err = copySQLiteSnapshotWithInspect(sourcePath, destinationPath, inspect)
	if !errors.Is(err, hardErr) {
		t.Fatalf("snapshot error = %v, want injected hard cause", err)
	}
	if strings.Contains(err.Error(), "kept changing") {
		t.Fatalf("hard reinspection failure was mislabeled as churn: %v", err)
	}
	if calls != 3 {
		t.Fatalf("inspection calls = %d, want immediate return after first reinspection failure", calls)
	}
	if _, statErr := os.Stat(sourcePath); statErr != nil {
		t.Fatalf("hard reinspection handling changed source: %v", statErr)
	}
}
