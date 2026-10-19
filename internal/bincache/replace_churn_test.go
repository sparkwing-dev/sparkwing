package bincache

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestReplaceTargetHashIgnoresConcurrentDotDirectoryChurn(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.test/sdk\n")
	writeFile(t, filepath.Join(root, "sdk.go"), "package sdk\n")
	writeFile(t, filepath.Join(root, "sdk", "typescript", "test", "command.test.ts"), "test\n")
	var baseline bytes.Buffer
	if _, err := hashDirIntoCounted(&baseline, root, allFiles, true); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var churn sync.WaitGroup
	churn.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			dir := filepath.Join(root, "sdk", "typescript", "test", fmt.Sprintf(".command-%d", i))
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Error(err)
				return
			}
			if err := os.WriteFile(filepath.Join(dir, "protocol"), []byte("x"), 0o600); err != nil {
				t.Error(err)
				return
			}
			if err := os.RemoveAll(dir); err != nil {
				t.Error(err)
				return
			}
		}
	})
	for range 200 {
		var digest bytes.Buffer
		if _, err := hashDirIntoCounted(&digest, root, allFiles, true); err != nil {
			close(stop)
			churn.Wait()
			t.Fatalf("hash failed during concurrent churn: %v", err)
		}
		if !bytes.Equal(digest.Bytes(), baseline.Bytes()) {
			close(stop)
			churn.Wait()
			t.Fatal("transient dot directories changed the replace-target key")
		}
	}
	close(stop)
	churn.Wait()
}

func TestWalkHashableSkipsDotAndUnderscoreDirectoriesButNotTheRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".sparkwing")
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")
	writeFile(t, filepath.Join(root, ".cache", "x"), "x")
	writeFile(t, filepath.Join(root, "_scratch", "y"), "y")
	files, err := walkHashable(root, allFiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || filepath.Base(files[0]) != "main.go" {
		t.Fatalf("hashed files = %v, want only main.go", files)
	}
}
