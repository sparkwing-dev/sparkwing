package bincache_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/bincache"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
)

func TestArtifactStoreRoundTrip(t *testing.T) {
	t.Parallel()
	storeDir := t.TempDir()
	store, err := fs.NewArtifactStore(storeDir)
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}

	// Source binary: any non-empty file works; bincache treats the
	// payload as opaque bytes.
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "fake-binary")
	if err := os.WriteFile(src, []byte("#!/bin/sh\necho hello\n"), 0o755); err != nil {
		t.Fatalf("write src: %v", err)
	}

	const key = "abcd1234-ef567890"
	ctx := context.Background()

	// Upload + verify Has.
	if err := bincache.UploadToArtifactStore(ctx, store, key, src); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	has, err := bincache.HasInArtifactStore(ctx, store, key)
	if err != nil || !has {
		t.Fatalf("Has = (%v, %v); want (true, nil)", has, err)
	}

	// Fetch into a fresh dest path; verify mode + bytes match.
	dest := filepath.Join(t.TempDir(), "downloaded")
	if err := bincache.FetchFromArtifactStore(ctx, store, key, dest); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "#!/bin/sh\necho hello\n" {
		t.Errorf("payload mismatch: %q", got)
	}
	st, _ := os.Stat(dest)
	if st.Mode()&0o100 == 0 {
		t.Errorf("dest not executable: %v", st.Mode())
	}
}

func TestArtifactStoreFetchMissReturnsNotFound(t *testing.T) {
	t.Parallel()
	store, _ := fs.NewArtifactStore(t.TempDir())
	dest := filepath.Join(t.TempDir(), "x")
	err := bincache.FetchFromArtifactStore(context.Background(), store, "missing", dest)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if !bincache.IsNotFound(err) {
		t.Errorf("IsNotFound = false on ErrNotFound")
	}
}
