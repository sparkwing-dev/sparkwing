package bincache

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestHashChurnNeverYieldsAKeyFromChangingInputs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.test/sdk\n")
	writeFile(t, filepath.Join(root, "sdk.go"), "package sdk\n")
	test := filepath.Join(root, "sdk", "typescript", "test")
	writeFile(t, filepath.Join(test, "command.test.ts"), "test\n")
	stop := make(chan struct{})
	var churn sync.WaitGroup
	churn.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			dir := filepath.Join(test, fmt.Sprintf(".command-%d", i))
			if os.MkdirAll(dir, 0o700) != nil || os.WriteFile(filepath.Join(dir, "protocol"), []byte("x"), 0o600) != nil || os.RemoveAll(dir) != nil {
				t.Error("fixture churn failed")
				return
			}
		}
	})
	defer func() {
		close(stop)
		churn.Wait()
	}()
	for range 200 {
		var digest bytes.Buffer
		if _, err := hashDirIntoCounted(&digest, root, allFiles, true); err != nil && !errors.Is(err, ErrUnstableInputs) {
			t.Fatalf("hash under concurrent churn = %v, want a key or ErrUnstableInputs", err)
		}
	}
}

func TestHashDetectsAFileThatChangesWhileHashing(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "embedded", "data.txt")
	writeFile(t, target, "before")
	walks := 0
	keep := func(name string) bool {
		if name == "data.txt" {
			walks++
			if walks == 2 {
				writeFile(t, target, "after, and longer")
			}
		}
		return true
	}
	var digest bytes.Buffer
	if _, err := hashDirIntoCounted(&digest, root, keep, true); !errors.Is(err, ErrUnstableInputs) {
		t.Fatalf("hash of a file rewritten mid-hash = %v, want ErrUnstableInputs", err)
	}
	if digest.Len() != 0 {
		t.Fatal("an unstable hash wrote key material")
	}
}

func TestHashIgnoresChangesToExcludedFiles(t *testing.T) {
	for _, mutation := range []string{"rewrite", "create", "remove"} {
		for _, hashAllFiles := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/hash_all_files=%t", mutation, hashAllFiles), func(t *testing.T) {
				root := newGitPipelineDir(t)
				writeFile(t, filepath.Join(root, ".gitignore"), "generated\n")
				target := filepath.Join(root, "generated")
				if mutation != "create" {
					writeFile(t, target, "before")
				}
				var expected bytes.Buffer
				if _, err := hashDirIntoCounted(&expected, root, allFiles, hashAllFiles); err != nil {
					t.Fatal(err)
				}
				walks := 0
				keep := func(name string) bool {
					if name == "main.go" {
						walks++
						if walks == 2 {
							if mutation == "remove" {
								if err := os.Remove(target); err != nil {
									t.Fatal(err)
								}
							} else {
								writeFile(t, target, "after, and longer")
							}
						}
					}
					return true
				}
				var digest bytes.Buffer
				_, err := hashDirIntoCounted(&digest, root, keep, hashAllFiles)
				if hashAllFiles {
					if !errors.Is(err, ErrUnstableInputs) || digest.Len() != 0 {
						t.Fatalf("hash of changing included input = %q, %v", &digest, err)
					}
					return
				}
				if err != nil || !bytes.Equal(digest.Bytes(), expected.Bytes()) {
					t.Fatalf("hash of changing excluded input = %q, %v; want %q", &digest, err, &expected)
				}
			})
		}
	}
}

func TestEmbeddedUnderscoreAndDotDirectoriesChangeTheKey(t *testing.T) {
	for _, dir := range []string{"_assets", ".assets"} {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "main.go"), "package main\n\nimport \"embed\"\n\n//go:embed all:"+dir+"\nvar assets embed.FS\n")
		file := filepath.Join(root, dir, "page.html")
		writeFile(t, file, "one")
		var first, second bytes.Buffer
		if _, err := hashDirIntoCounted(&first, root, allFiles, true); err != nil {
			t.Fatal(err)
		}
		writeFile(t, file, "two")
		if _, err := hashDirIntoCounted(&second, root, allFiles, true); err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(first.Bytes(), second.Bytes()) {
			t.Errorf("a change under embedded %s left the key unchanged", dir)
		}
	}
}

func TestHashDetectsLateCreationWithRestoredDirectoryTime(t *testing.T) {
	for _, hashAllFiles := range []bool{false, true} {
		t.Run(fmt.Sprintf("hash_all_files=%t", hashAllFiles), func(t *testing.T) {
			root := newGitPipelineDir(t)
			writeFile(t, filepath.Join(root, ".gitignore"), "*.generated\n")
			assets := filepath.Join(root, "assets")
			writeFile(t, filepath.Join(assets, "existing"), "original")
			writeFile(t, filepath.Join(root, "z-last.go"), "package main\n")
			var expected bytes.Buffer
			if _, err := hashDirIntoCounted(&expected, root, allFiles, hashAllFiles); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(assets)
			if err != nil {
				t.Fatal(err)
			}
			walks := 0
			keep := func(name string) bool {
				if name == "z-last.go" {
					walks++
					if walks == 2 {
						writeFile(t, filepath.Join(assets, "late.generated"), "included only with hash-all-files")
						if err := os.Chtimes(assets, info.ModTime(), info.ModTime()); err != nil {
							t.Fatal(err)
						}
					}
				}
				return true
			}
			var digest bytes.Buffer
			_, err = hashDirIntoCounted(&digest, root, keep, hashAllFiles)
			if hashAllFiles {
				if !errors.Is(err, ErrUnstableInputs) || digest.Len() != 0 {
					t.Fatalf("late included file escaped = %q %v", digest.Bytes(), err)
				}
			} else if err != nil || !bytes.Equal(digest.Bytes(), expected.Bytes()) {
				t.Fatalf("late excluded file changed the key = %q %v", digest.Bytes(), err)
			}
		})
	}
}
