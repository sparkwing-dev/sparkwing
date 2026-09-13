package cluster

import (
	"context"
	"log/slog"
	"reflect"
	"testing"
)

func TestParseWarmModules(t *testing.T) {
	for _, test := range []struct {
		name       string
		spec       string
		sdkVersion string
		want       []string
	}{
		{
			name:       "empty spec warms the SDK at the runner's own version",
			sdkVersion: "v0.49.1",
			want:       []string{"github.com/sparkwing-dev/sparkwing@v0.49.1"},
		},
		{
			name:       "a runner with no release version warms nothing",
			sdkVersion: "(devel)",
		},
		{
			name:       "off disables the warm",
			spec:       "off",
			sdkVersion: "v0.49.1",
		},
		{
			name:       "OFF is the same opt-out",
			spec:       "  OFF ",
			sdkVersion: "v0.49.1",
		},
		{
			name:       "an explicit list keeps its pins",
			spec:       "example.com/a@v1.2.3, example.com/b@v0.1.0",
			sdkVersion: "v0.49.1",
			want:       []string{"example.com/a@v1.2.3", "example.com/b@v0.1.0"},
		},
		{
			name:       "a bare SDK path takes the runner's version",
			spec:       "github.com/sparkwing-dev/sparkwing",
			sdkVersion: "v0.49.1",
			want:       []string{"github.com/sparkwing-dev/sparkwing@v0.49.1"},
		},
		{
			name:       "another bare path resolves at download time",
			spec:       "example.com/a",
			sdkVersion: "v0.49.1",
			want:       []string{"example.com/a@latest"},
		},
		{
			name:       "a bare SDK path on an unversioned runner resolves at download time",
			spec:       "github.com/sparkwing-dev/sparkwing",
			sdkVersion: "dev",
			want:       []string{"github.com/sparkwing-dev/sparkwing@latest"},
		},
		{
			name:       "an explicit latest is kept",
			spec:       "example.com/a@latest",
			sdkVersion: "v0.49.1",
			want:       []string{"example.com/a@latest"},
		},
		{
			name:       "empty entries are dropped",
			spec:       ",, example.com/a@v1.2.3 ,",
			sdkVersion: "v0.49.1",
			want:       []string{"example.com/a@v1.2.3"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseWarmModules(test.spec, test.sdkVersion)
			if err != nil {
				t.Fatalf("parseWarmModules(%q, %q) error = %v", test.spec, test.sdkVersion, err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parseWarmModules(%q, %q) = %v, want %v", test.spec, test.sdkVersion, got, test.want)
			}
		})
	}
}

func TestParseWarmModulesRejectsUnusableEntries(t *testing.T) {
	for _, test := range []struct {
		name string
		spec string
	}{
		{name: "no module path", spec: "@v1.2.3"},
		{name: "empty version", spec: "example.com/a@"},
		{name: "version is not semver", spec: "example.com/a@main"},
		{name: "embedded whitespace", spec: "example.com/a v1.2.3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseWarmModules(test.spec, "v0.49.1")
			if err == nil {
				t.Fatalf("parseWarmModules(%q) = %v, want an error naming the entry", test.spec, got)
			}
		})
	}
}

func TestWarmModuleCacheWithNoModulesRunsNothing(t *testing.T) {
	t.Setenv("PATH", "")
	warmModuleCache(context.Background(), nil, slog.Default())
}
