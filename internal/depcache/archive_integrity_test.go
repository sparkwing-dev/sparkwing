package depcache_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/depcache"
	"github.com/sparkwing-dev/sparkwing/internal/tarsafe"
)

func TestArchiveTrailerVerifiedBeforePublish(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "tool"), []byte("new bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := tarsafe.WriteGzip(t.Context(), &archive, source); err != nil {
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
