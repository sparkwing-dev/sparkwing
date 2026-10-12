package backends_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

func TestValidateFields_RequiresWhatEachTypeCannotWorkWithout(t *testing.T) {
	cases := []struct {
		name string
		spec backends.Spec
		want string
	}{
		{"s3 without bucket", backends.Spec{Type: backends.TypeS3}, "requires bucket"},
		{"s3 with prefix only", backends.Spec{Type: backends.TypeS3, Prefix: "runs"}, "requires bucket"},
		{"filesystem without path", backends.Spec{Type: backends.TypeFilesystem}, "requires path"},
		{"controller without a name", backends.Spec{Type: backends.TypeController}, "requires controller"},
		{"no type at all", backends.Spec{}, "type is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.ValidateFields("cache")
			if err == nil {
				t.Fatalf("got nil, want an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should mention %q", err.Error(), tc.want)
			}
			if !strings.Contains(err.Error(), "cache") {
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

func TestValidateFields_RefusesTheRemovedStateTypes(t *testing.T) {
	cases := []struct {
		surface string
		spec    backends.Spec
		key     string
	}{
		{"state", backends.Spec{Type: backends.TypeS3, Bucket: "b"}, "state.type: s3"},
		{"state", backends.Spec{Type: "postgres", URL: "postgres://db/sw"}, "state.type: postgres"},
	}
	for _, tc := range cases {
		err := tc.spec.ValidateFields(tc.surface)
		if err == nil {
			t.Fatalf("%+v on %s: got nil, want the removal refused", tc.spec, tc.surface)
		}
		for _, want := range []string{tc.key, "is removed", "sparkwing pipeline trigger", backends.MigrationGuideURL} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%+v on %s: error %q should mention %q", tc.spec, tc.surface, err, want)
			}
		}
	}
	if err := (&backends.Spec{Type: backends.TypeS3, Bucket: "b"}).ValidateFields("cache"); err != nil {
		t.Errorf("an s3 cache surface = %v, want nil", err)
	}
}
