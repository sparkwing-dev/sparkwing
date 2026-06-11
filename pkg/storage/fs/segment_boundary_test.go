package fs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
)

func TestLogStore_RejectsPathEscapingIDs(t *testing.T) {
	root := t.TempDir()
	ls, err := fs.NewLogStore(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := ls.Append(ctx, "../escape", "n", []byte(`{}`)); err == nil {
		t.Fatal("Append with traversal runID succeeded")
	}
	if err := ls.Append(ctx, "r1", "../../escape", []byte(`{}`)); err == nil {
		t.Fatal("Append with traversal nodeID succeeded")
	}
	if _, err := ls.Read(ctx, "r1", "a/b", storage.ReadOpts{}); err == nil {
		t.Fatal("Read with separator nodeID succeeded")
	}
	if _, err := ls.ReadRun(ctx, ".."); err == nil {
		t.Fatal("ReadRun with traversal runID succeeded")
	}
	if err := ls.DeleteRun(ctx, "../logs"); err == nil {
		t.Fatal("DeleteRun with traversal runID succeeded")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "logs" {
		t.Fatalf("rejected IDs still created entries outside the log root: %v", entries)
	}
}
