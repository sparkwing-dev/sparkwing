package backends_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

func TestDetectMatch_EqualsAndPresent(t *testing.T) {
	t.Setenv("SW_TEST_DETECT", "yes")
	cases := []struct {
		name string
		d    backends.Detect
		want bool
	}{
		{"equals match", backends.Detect{EnvVar: "SW_TEST_DETECT", Equals: "yes"}, true},
		{"equals mismatch", backends.Detect{EnvVar: "SW_TEST_DETECT", Equals: "no"}, false},
		{"present match", backends.Detect{EnvVar: "SW_TEST_DETECT", Present: true}, true},
		{"present absent var", backends.Detect{EnvVar: "SW_TEST_ABSENT", Present: true}, false},
		{"empty envvar never matches", backends.Detect{Present: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.d.Match(); got != tc.want {
				t.Fatalf("Match() = %v, want %v", got, tc.want)
			}
		})
	}
}

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
