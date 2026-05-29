package profile

import (
	"os"
	"strings"
	"testing"
)

func TestResolveDefaultArgs_NilProfileReturnsNil(t *testing.T) {
	var p *Profile
	got, err := p.ResolveDefaultArgs()
	if err != nil || got != nil {
		t.Fatalf("nil profile should return (nil, nil); got (%v, %v)", got, err)
	}
}

func TestResolveDefaultArgs_EmptyMapReturnsNil(t *testing.T) {
	p := &Profile{Name: "prod"}
	got, err := p.ResolveDefaultArgs()
	if err != nil || got != nil {
		t.Fatalf("empty default-args should return (nil, nil); got (%v, %v)", got, err)
	}
}

func TestResolveDefaultArgs_LiteralValuesPassThrough(t *testing.T) {
	p := &Profile{
		Name: "prod",
		DefaultArgs: map[string]string{
			"target":   "prod",
			"replicas": "5",
		},
	}
	got, err := p.ResolveDefaultArgs()
	if err != nil {
		t.Fatalf("ResolveDefaultArgs: %v", err)
	}
	if got["target"] != "prod" || got["replicas"] != "5" {
		t.Errorf("literal values should pass through; got %+v", got)
	}
}

func TestResolveDefaultArgs_EnvInterpolation(t *testing.T) {
	t.Setenv("MY_VERSION", "1.2.3")
	t.Setenv("MY_TAG", "main")
	p := &Profile{
		Name: "ci",
		DefaultArgs: map[string]string{
			"version": "${MY_VERSION}",
			"image":   "registry/app:${MY_VERSION}-${MY_TAG}",
		},
	}
	got, err := p.ResolveDefaultArgs()
	if err != nil {
		t.Fatalf("ResolveDefaultArgs: %v", err)
	}
	if got["version"] != "1.2.3" {
		t.Errorf("version interpolation: got %q, want 1.2.3", got["version"])
	}
	if got["image"] != "registry/app:1.2.3-main" {
		t.Errorf("multi-var interpolation: got %q, want registry/app:1.2.3-main", got["image"])
	}
}

func TestResolveDefaultArgs_UnsetVarExpandsToEmpty(t *testing.T) {
	p := &Profile{
		Name:        "ci",
		DefaultArgs: map[string]string{"version": "${DEFINITELY_NOT_SET_42}"},
	}
	got, err := p.ResolveDefaultArgs()
	if err != nil {
		t.Fatalf("ResolveDefaultArgs: %v", err)
	}
	if got["version"] != "" {
		t.Errorf("unset env var should expand to empty; got %q", got["version"])
	}
}

func TestResolveDefaultArgs_RejectsShellLikeSyntax(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"posix-default", "${VAR:-default}"},
		{"command-sub", "$(uname -s)"},
		{"bare-var", "$HOME/foo"},
		{"posix-error", "${VAR?missing}"},
		{"posix-alt", "${VAR+alt}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &Profile{
				Name:        "ci",
				DefaultArgs: map[string]string{"version": c.value},
			}
			_, err := p.ResolveDefaultArgs()
			if err == nil || !strings.Contains(err.Error(), "unsupported") {
				t.Fatalf("expected unsupported-syntax error for %q; got %v", c.value, err)
			}
		})
	}
}

func TestResolveDefaultArgs_ErrorNamesProfileAndKey(t *testing.T) {
	p := &Profile{
		Name:        "prod",
		DefaultArgs: map[string]string{"weirdkey": "$(rm -rf /)"},
	}
	_, err := p.ResolveDefaultArgs()
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "prod") || !strings.Contains(msg, "weirdkey") {
		t.Errorf("error should name the profile and key; got %q", msg)
	}
}

func TestMergeDefaultArgs_ChildOverridesParent(t *testing.T) {
	parent := map[string]string{"target": "dev", "version": "0.1.0"}
	child := map[string]string{"target": "prod"}
	got := MergeDefaultArgs(parent, child)
	if got["target"] != "prod" {
		t.Errorf("child should override parent; got target=%q", got["target"])
	}
	if got["version"] != "0.1.0" {
		t.Errorf("parent-only keys should survive; got version=%q", got["version"])
	}
}

func TestMergeDefaultArgs_NilInputs(t *testing.T) {
	if got := MergeDefaultArgs(nil, nil); got != nil {
		t.Errorf("nil + nil should return nil; got %v", got)
	}
	if got := MergeDefaultArgs(map[string]string{"a": "1"}, nil); got["a"] != "1" {
		t.Errorf("nil child should preserve parent")
	}
	if got := MergeDefaultArgs(nil, map[string]string{"a": "1"}); got["a"] != "1" {
		t.Errorf("nil parent should preserve child")
	}
}

func TestDefaultArgsKeys_SortedOrder(t *testing.T) {
	p := &Profile{
		DefaultArgs: map[string]string{"zebra": "z", "apple": "a", "mango": "m"},
	}
	got := p.DefaultArgsKeys()
	want := []string{"apple", "mango", "zebra"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("DefaultArgsKeys[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestProfile_YAMLParseRoundTripDefaultArgs(t *testing.T) {
	// Ensure the yaml tag is correct by round-tripping through Load.
	// Write a tiny profiles.yaml, load it, check DefaultArgs.
	dir := t.TempDir()
	path := dir + "/profiles.yaml"
	src := `
default: ci
profiles:
  ci:
    detect: { env_var: CI, equals: "true" }
    default-args:
      target: dev
      version: "0.5.1"
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write temp profiles.yaml: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ci, ok := cfg.Profiles["ci"]
	if !ok {
		t.Fatalf("ci profile missing; have %v", cfg.Profiles)
	}
	if ci.DefaultArgs["target"] != "dev" || ci.DefaultArgs["version"] != "0.5.1" {
		t.Errorf("round-trip mismatch: %+v", ci.DefaultArgs)
	}
}
