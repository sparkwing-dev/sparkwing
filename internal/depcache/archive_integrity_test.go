package depcache_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/depcache"
)

func TestArchiveTrailerVerifiedBeforePublish(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "tool"), []byte("new bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := depcache.WriteArchive(&archive, source); err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"checksum", "length", "truncated", "cancelled"} {
		t.Run(fault, func(t *testing.T) {
			raw := bytes.Clone(archive.Bytes())
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch fault {
			case "checksum":
				raw[len(raw)-8] ^= 1
			case "length":
				raw[len(raw)-4] ^= 1
			case "truncated":
				raw = raw[:len(raw)-1]
			}
			parent := t.TempDir()
			dest := filepath.Join(parent, "cache")
			if err := os.Mkdir(dest, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dest, "tool"), []byte("preserved"), 0o644); err != nil {
				t.Fatal(err)
			}
			reader := cancelArchiveRead{Reader: bytes.NewReader(raw)}
			if fault == "cancelled" {
				reader.cancel = cancel
			}
			root, err := os.OpenRoot(parent)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := depcache.ExtractArchiveStagedRoot(ctx, reader, root, "cache"); err == nil {
				t.Fatal("corrupt archive published")
			} else if fault == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled extraction = %v", err)
			}
			body, err := os.ReadFile(filepath.Join(dest, "tool"))
			if err != nil || string(body) != "preserved" {
				t.Fatalf("failed restore changed destination: %q, %v", body, err)
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 1 || entries[0].Name() != "cache" {
				t.Fatalf("failed restore left staging files: %v, %v", entries, err)
			}
		})
	}
}

type cancelArchiveRead struct {
	*bytes.Reader
	cancel context.CancelFunc
}

func (r cancelArchiveRead) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if r.cancel != nil {
		r.cancel()
	}
	return n, err
}

func TestCorruptLocalArchiveIsReplacedAfterSuccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	work, spec := cacheWorkspace(t)
	run := depcache.Restore(t.Context(), spec, "job", work)
	cacheFile := func(work, body string) {
		t.Helper()
		dir := filepath.Join(work, "tools")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "tool"), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cacheFile(work, "original")
	run.Save(t.Context(), nil)
	paths, err := filepath.Glob(filepath.Join(home, "depcache", "*.tar.gz"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("saved archives = %v, %v", paths, err)
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-8] ^= 1
	if err := os.WriteFile(paths[0], raw, 0o600); err != nil {
		t.Fatal(err)
	}
	next, _ := cacheWorkspace(t)
	repair := depcache.Restore(t.Context(), spec, "job", next)
	if _, err := os.Stat(filepath.Join(next, "tools")); !os.IsNotExist(err) {
		t.Fatalf("corrupt cache did not run cold: %v", err)
	}
	cacheFile(next, "repaired")
	repair.Save(t.Context(), nil)
	last, _ := cacheWorkspace(t)
	depcache.Restore(t.Context(), spec, "job", last)
	if body, err := os.ReadFile(filepath.Join(last, "tools", "tool")); err != nil || string(body) != "repaired" {
		t.Fatalf("cache not replaced after success: %q, %v", body, err)
	}
}
