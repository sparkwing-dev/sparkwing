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
		{"postgres without url", backends.Spec{Type: backends.TypePostgres}, "requires url or url_source"},
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
