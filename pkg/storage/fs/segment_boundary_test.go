package fs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
)

func TestArtifactStore_RejectsPathEscapingKeys(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(secret, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	as, err := fs.NewArtifactStore(filepath.Join(root, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	rejected := []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"dot", "."},
		{"parent", ".."},
		{"leading parent", "../secret.txt"},
		{"nested parent", "../../etc/passwd"},
		{"interior parent", "runs/../../secret.txt"},
		{"absolute", "/etc/passwd"},
		{"empty segment", "runs//state.ndjson"},
		{"backslash", `..\secret.txt`},
		{"control character", "runs/\x00/state.ndjson"},
		{"shard escapes", "..secret"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := as.Get(ctx, tc.key); err == nil {
				t.Error("Get succeeded")
			}
		})
	}

	body, err := os.ReadFile(secret)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "original" {
		t.Fatalf("file outside the store root was rewritten: %q", body)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("rejected keys created entries outside the store root: %v", entries)
	}
}
