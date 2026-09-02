package bincache_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
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

	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "fake-binary")
	if err := os.WriteFile(src, []byte("#!/bin/sh\necho hello\n"), 0o755); err != nil {
		t.Fatalf("write src: %v", err)
	}

	const key = "abcd1234-ef567890"
	ctx := context.Background()

	if err := bincache.UploadToArtifactStore(ctx, store, key, src); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	has, err := bincache.HasInArtifactStore(ctx, store, key)
	if err != nil || !has {
		t.Fatalf("Has = (%v, %v); want (true, nil)", has, err)
	}

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

func TestArtifactStoreFetchRejectsTamperedBlob(t *testing.T) {
	t.Parallel()
	store, err := fs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}
	src := filepath.Join(t.TempDir(), "fake-binary")
	if err := os.WriteFile(src, []byte("honest binary"), 0o755); err != nil {
		t.Fatalf("write src: %v", err)
	}
	const key = "abcd1234-ef567890"
	ctx := context.Background()
	if err := bincache.UploadToArtifactStore(ctx, store, key, src); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if err := store.Put(ctx, "bin/"+key, strings.NewReader("poisoned binary")); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "downloaded")
	if err := bincache.FetchFromArtifactStore(ctx, store, key, dest); !errors.Is(err, bincache.ErrDigest) {
		t.Fatalf("err = %v, want ErrDigest", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("tampered binary was installed: %v", err)
	}
}

func TestArtifactStoreFetchHealsABlobPublishedWithoutADigest(t *testing.T) {
	t.Parallel()
	store, err := fs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}
	const key = "abcd1234-ef567890"
	const payload = "binary published before digests existed"
	ctx := context.Background()
	if err := store.Put(ctx, "bin/"+key, strings.NewReader(payload)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "downloaded")
	if err := bincache.FetchFromArtifactStore(ctx, store, key, dest); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != payload {
		t.Errorf("payload = %q, want %q", got, payload)
	}

	rc, err := store.Get(ctx, "bin/"+key+".sha256")
	if err != nil {
		t.Fatalf("companion object was not backfilled: %v", err)
	}
	defer rc.Close()
	raw, _ := io.ReadAll(rc)
	sum := sha256.Sum256([]byte(payload))
	if strings.TrimSpace(string(raw)) != hex.EncodeToString(sum[:]) {
		t.Errorf("backfilled digest = %q, want %q", raw, hex.EncodeToString(sum[:]))
	}

	if err := store.Put(ctx, "bin/"+key, strings.NewReader("poisoned binary")); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	healed := filepath.Join(t.TempDir(), "downloaded")
	if err := bincache.FetchFromArtifactStore(ctx, store, key, healed); !errors.Is(err, bincache.ErrDigest) {
		t.Fatalf("err = %v, want ErrDigest once the companion exists", err)
	}
}

func TestArtifactStoreFetchRejectsAMalformedStoredDigest(t *testing.T) {
	t.Parallel()
	store, err := fs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}
	const key = "abcd1234-ef567890"
	ctx := context.Background()
	if err := store.Put(ctx, "bin/"+key, strings.NewReader("some binary")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Put(ctx, "bin/"+key+".sha256", strings.NewReader("not-a-digest")); err != nil {
		t.Fatalf("Put digest: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "downloaded")
	err = bincache.FetchFromArtifactStore(ctx, store, key, dest)
	if !errors.Is(err, bincache.ErrDigest) {
		t.Fatalf("err = %v, want ErrDigest", err)
	}
	if bincache.IsNotFound(err) {
		t.Error("IsNotFound = true, want a hard failure rather than a recompilable miss")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("unattested binary was installed: %v", err)
	}
}
