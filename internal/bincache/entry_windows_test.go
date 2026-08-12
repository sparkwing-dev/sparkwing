//go:build windows

package bincache

import (
	"context"
	"os"
	"testing"
)

func TestMaterializedLeaseAllowsConcurrentReaders(t *testing.T) {
	entry := testEntry(t, t.TempDir(), "11111111-11111111")
	lease, _, err := entry.AcquireOrMaterialize(context.Background(), func(path string) error {
		return os.WriteFile(path, []byte("binary"), 0o755)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Release() }()

	second, acquired, err := openCacheLockPath(entry.lockPath("lease"), cacheLockSharedNonblock)
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("materialized entry retained an exclusive lease")
	}
	defer func() { _ = second.Close() }()
}
