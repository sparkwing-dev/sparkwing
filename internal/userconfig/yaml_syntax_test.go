package userconfig

import (
	"errors"
	"strings"
	"testing"
)

func TestYAMLSyntaxErrorNamesTheLineAndProblem(t *testing.T) {
	type doc struct {
		Pipelines []map[string]any `yaml:"pipelines"`
		Profiles  map[string]any   `yaml:"profiles"`
	}
	for _, tc := range []struct {
		name, raw, want string
	}{
		{"mis-indented key", "pipelines:\n  - name: build\n    entrypoint: Build\n   on:\n      push: {}\n", "sparkwing.yaml: $: line 4: invalid YAML syntax: did not find expected '-' indicator"},
		{"mis-indented secret", "profiles:\n  cloud:\n    url: https://x\n    token: abc\n     token: secretvalue123\n", "sparkwing.yaml: $: line 5: invalid YAML syntax: mapping values are not allowed in this context"},
		{"open flow sequence", "pipelines: [{name: a},\n  {name: b}]\nprofiles: a: b\n", "sparkwing.yaml: $: line 3: invalid YAML syntax: mapping values are not allowed in this context"},
	} {
		_, err := DecodeYAML("sparkwing.yaml", []byte(tc.raw), &doc{})
		var field *FieldError
		if !errors.As(err, &field) || err.Error() != tc.want {
			t.Errorf("%s: error %v, want %q", tc.name, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), "secretvalue123") {
			t.Errorf("%s: syntax error quoted the document: %v", tc.name, err)
		}
	}
}
