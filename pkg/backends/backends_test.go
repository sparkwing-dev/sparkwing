package backends_test

import (
	"strings"
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

func TestLayerSurfaces_SameTypeFillsBlanks(t *testing.T) {
	base := backends.Surfaces{State: &backends.Spec{Type: backends.TypeS3, Bucket: "team", Prefix: "state"}}
	over := backends.Surfaces{State: &backends.Spec{Type: backends.TypeS3, Prefix: "override"}}
	eff := backends.LayerSurfaces(base, over)
	if eff.State.Bucket != "team" || eff.State.Prefix != "override" {
		t.Fatalf("state surface = %+v, want bucket=team prefix=override", eff.State)
	}
}

func TestValidateFields_RequiresWhatEachTypeCannotWorkWithout(t *testing.T) {
	cases := []struct {
		name string
		spec backends.Spec
		want string
	}{
		{"s3 without bucket", backends.Spec{Type: backends.TypeS3}, "requires bucket"},
		{"s3 with prefix only", backends.Spec{Type: backends.TypeS3, Prefix: "runs"}, "requires bucket"},
		{"gcs without bucket", backends.Spec{Type: backends.TypeGCS}, "requires bucket"},
		{"azure without bucket", backends.Spec{Type: backends.TypeAzureBlob}, "requires bucket"},
		{"filesystem without path", backends.Spec{Type: backends.TypeFilesystem}, "requires path"},
		{"postgres without url", backends.Spec{Type: backends.TypePostgres}, "requires url or url_source"},
		{"mysql without url", backends.Spec{Type: backends.TypeMySQL}, "requires url or url_source"},
		{"controller without a name", backends.Spec{Type: backends.TypeController}, "requires controller"},
		{"no type at all", backends.Spec{}, "type is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.ValidateFields("state")
			if err == nil {
				t.Fatalf("got nil, want an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should mention %q", err.Error(), tc.want)
			}
			if !strings.Contains(err.Error(), "state") {
				t.Errorf("error %q should name the surface", err.Error())
			}
		})
	}
}

func TestValidateFields_AllowsWhatHasARealDefault(t *testing.T) {
	ok := []backends.Spec{
		{Type: backends.TypeSQLite},
		{Type: backends.TypeStdout},
		{Type: backends.TypeS3, Bucket: "b"},
		{Type: backends.TypeFilesystem, Path: "/tmp/x"},
		{Type: backends.TypePostgres, URLSource: "DATABASE_URL"},
	}
	for _, spec := range ok {
		if err := spec.ValidateFields("logs"); err != nil {
			t.Errorf("%+v: got %v, want nil", spec, err)
		}
	}
}

func TestSurfacesValidate_RejectsBucketlessS3(t *testing.T) {
	surf := backends.Surfaces{
		Secrets: &backends.Spec{Type: backends.TypeNone},
		State:   &backends.Spec{Type: backends.TypeSQLite},
		Cache:   &backends.Spec{Type: backends.TypeFilesystem, Path: "/tmp/c"},
		Logs:    &backends.Spec{Type: backends.TypeS3},
	}
	err := surf.Validate("bucket")
	if err == nil {
		t.Fatal("got nil, want the logs surface rejected")
	}
	if !strings.Contains(err.Error(), "logs") || !strings.Contains(err.Error(), "requires bucket") {
		t.Errorf("error %q should name the logs surface and the missing bucket", err.Error())
	}
}
