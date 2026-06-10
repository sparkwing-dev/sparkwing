package backends_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

func TestLayerSurfaces_OverWinsPerSurface(t *testing.T) {
	base := backends.Surfaces{
		State: &backends.Spec{Type: backends.TypeSQLite, Path: "/base.db"},
		Cache: &backends.Spec{Type: backends.TypeFilesystem, Path: "/base/cache"},
	}
	over := backends.Surfaces{
		State: &backends.Spec{Type: backends.TypeS3, Bucket: "team", Prefix: "state"},
	}
	eff := backends.LayerSurfaces(base, over)
	if eff.State.Type != backends.TypeS3 || eff.State.Bucket != "team" {
		t.Fatalf("state surface = %+v, want s3/team", eff.State)
	}
	if eff.Cache == nil || eff.Cache.Path != "/base/cache" {
		t.Fatalf("cache surface = %+v, want base filesystem", eff.Cache)
	}
	if eff.Logs != nil {
		t.Fatalf("logs surface = %+v, want nil", eff.Logs)
	}
}

// layerSpec keeps base fields when over omits them but shares the type.
func TestLayerSurfaces_SameTypeFillsBlanks(t *testing.T) {
	base := backends.Surfaces{State: &backends.Spec{Type: backends.TypeS3, Bucket: "team", Prefix: "state"}}
	over := backends.Surfaces{State: &backends.Spec{Type: backends.TypeS3, Prefix: "override"}}
	eff := backends.LayerSurfaces(base, over)
	if eff.State.Bucket != "team" || eff.State.Prefix != "override" {
		t.Fatalf("state surface = %+v, want bucket=team prefix=override", eff.State)
	}
}
