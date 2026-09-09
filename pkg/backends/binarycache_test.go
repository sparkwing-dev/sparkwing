package backends_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

func TestBinaryCache_NilSpec(t *testing.T) {
	var spec *backends.Spec
	if got := spec.BinaryCache(); got != nil {
		t.Fatalf("BinaryCache() on a nil spec = %+v, want nil", got)
	}
}

func TestBinaryCache_WithoutSubSpecIsTheCacheSurface(t *testing.T) {
	spec := &backends.Spec{Type: backends.TypeFilesystem, Path: "/var/cache/sparkwing"}
	if got := spec.BinaryCache(); got != spec {
		t.Fatalf("BinaryCache() = %+v, want the cache surface itself", got)
	}
}

func TestBinaryCache_WithSubSpecIsolatesBinaries(t *testing.T) {
	bin := &backends.Spec{Type: backends.TypeS3, Bucket: "sparkwing-binaries", Prefix: "ci/"}
	spec := &backends.Spec{Type: backends.TypeFilesystem, Path: "/var/cache/sparkwing", Binaries: bin}
	if got := spec.BinaryCache(); got != bin {
		t.Fatalf("BinaryCache() = %+v, want the binaries sub-spec", got)
	}
}
