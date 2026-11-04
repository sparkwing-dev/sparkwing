package cluster

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGoSeedFillsOnlyEmptyCachesForTheRunnersVersion(t *testing.T) {
	root := t.TempDir()
	for _, file := range []string{"v1.2.3/modules/example.com/m@v1.0.0/m.go", "v1.2.3/gocache/ab/abcd-d", "v9.9.9/gocache/ef/efgh-d"} {
		path := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(file), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	// go leaves extracted modules read-only; the copy must still let go add modules beside them.
	if err := os.Chmod(filepath.Join(root, "v1.2.3/modules/example.com/m@v1.0.0"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "v1.2.3/modules/example.com/m@v1.0.0"), 0o755) })

	jobs := t.TempDir()
	modules, gocache := filepath.Join(jobs, "modules"), filepath.Join(jobs, "gocache")
	if err := os.Mkdir(gocache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := seedGoCaches(root, "v1.2.3", modules, gocache); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(modules, "example.com/m@v1.0.0/m.go"))
	if err != nil || string(body) != "v1.2.3/modules/example.com/m@v1.0.0/m.go" {
		t.Fatalf("seeded module = %q, %v", body, err)
	}
	if err := os.WriteFile(filepath.Join(modules, "example.com/m@v1.0.0/added"), nil, 0o644); err != nil {
		t.Errorf("seeded module cache is not writable: %v", err)
	}
	if entries, _ := os.ReadDir(gocache); len(entries) != 0 {
		t.Errorf("an existing build cache was overwritten: %v", entries)
	}
	if _, err := os.Stat(modules + ".seeding"); !os.IsNotExist(err) {
		t.Errorf("seeding left its partial copy: %v", err)
	}

	other := filepath.Join(jobs, "other")
	if err := seedGoCaches(root, "v0.0.1", other, ""); err != nil || exists(other) {
		t.Errorf("a version without a seed created %s: %v", other, err)
	}
}
