package backends_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

func TestBinaryCache_NoCacheSurface(t *testing.T) {
	if got := (backends.Surfaces{}).BinaryCache(); got != nil {
		t.Fatalf("BinaryCache() without a cache surface = %+v, want nil", got)
	}
}

func TestBinaryCache_WithoutSubSpecIsTheCacheSurface(t *testing.T) {
	cache := &backends.Spec{Type: backends.TypeFilesystem, Path: "/var/cache/sparkwing"}
	if got := (backends.Surfaces{Cache: cache}).BinaryCache(); got != cache {
		t.Fatalf("BinaryCache() = %+v, want the cache surface itself", got)
	}
}

func TestBinaryCache_WithSubSpecIsolatesBinaries(t *testing.T) {
	bin := &backends.Spec{Type: backends.TypeS3, Bucket: "sparkwing-binaries", Prefix: "ci/"}
	cache := &backends.Spec{Type: backends.TypeFilesystem, Path: "/var/cache/sparkwing", Binaries: bin}
	if got := (backends.Surfaces{Cache: cache}).BinaryCache(); got != bin {
		t.Fatalf("BinaryCache() = %+v, want the binaries sub-spec", got)
	}
}

// Binaries recurses structurally because it lives on Spec. Only one level
// is read, and a deeper one stays a no-op rather than failing a config that
// loads today.
func TestBinaryCache_IgnoresANestedBinariesSubSpec(t *testing.T) {
	inner := &backends.Spec{Type: backends.TypeS3, Bucket: "inner"}
	outer := &backends.Spec{Type: backends.TypeS3, Bucket: "outer", Binaries: inner}
	cache := &backends.Spec{Type: backends.TypeFilesystem, Path: "/tmp/cache", Binaries: outer}

	surfaces := backends.Surfaces{
		Secrets: &backends.Spec{Type: backends.TypeEnv},
		State:   &backends.Spec{Type: backends.TypeSQLite, Path: "/tmp/state.db"},
		Logs:    &backends.Spec{Type: backends.TypeFilesystem, Path: "/tmp/logs"},
		Cache:   cache,
	}
	if err := surfaces.Validate("nested"); err != nil {
		t.Fatalf("a nested sub-spec must still load: %v", err)
	}
	if got := surfaces.BinaryCache(); got != outer {
		t.Fatalf("BinaryCache() = %+v, want the first level", got)
	}
}
