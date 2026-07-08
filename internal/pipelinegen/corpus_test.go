package pipelinegen

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

func TestLoadCorpusParsesEmbeddedSpecs(t *testing.T) {
	fsys, root := DefaultCorpus()
	specs, err := LoadCorpus(fsys, root)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(specs) < 4 {
		t.Fatalf("expected at least 4 specs, got %d", len(specs))
	}

	var pass, fail int
	for i, s := range specs {
		if i > 0 && specs[i-1].Name > s.Name {
			t.Errorf("specs not sorted by name: %q before %q", specs[i-1].Name, s.Name)
		}
		if s.Entrypoint == "" {
			t.Errorf("spec %q has empty entrypoint", s.Name)
		}
		if strings.TrimSpace(s.Prompt) == "" {
			t.Errorf("spec %q has empty prompt", s.Name)
		}
		switch s.Expect {
		case ExpectPass:
			pass++
		case ExpectFail:
			fail++
		default:
			t.Errorf("spec %q has invalid expect %q", s.Name, s.Expect)
		}
	}
	if pass == 0 || fail == 0 {
		t.Fatalf("corpus must cover both outcomes: pass=%d fail=%d", pass, fail)
	}
}

func TestParseSpecRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"no leading fence":   "shape: x\nexpect: pass\nentrypoint: P\n---\nbody",
		"unknown key":        "---\nmood: happy\n---\nbody",
		"bad expect":         "---\nexpect: maybe\nentrypoint: P\n---\nbody",
		"missing entrypoint": "---\nexpect: pass\n---\nbody",
		"empty prompt":       "---\nexpect: pass\nentrypoint: P\n---\n   \n",
		"unterminated":       "---\nexpect: pass\nentrypoint: P\n",
	}
	for name, content := range cases {
		if _, err := parseSpec("t", content); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestParseSpecDefaultsShape(t *testing.T) {
	s, err := parseSpec("t", "---\nexpect: fail\nentrypoint: P\n---\ndo a thing\n")
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	if s.Shape != "unspecified" {
		t.Errorf("shape = %q, want unspecified", s.Shape)
	}
	if s.Expect != ExpectFail || s.Entrypoint != "P" || s.Prompt != "do a thing" {
		t.Errorf("unexpected spec: %+v", s)
	}
}

func TestFixtureGeneratorReadsCandidate(t *testing.T) {
	fsys, root := DefaultCorpus()
	specs, err := LoadCorpus(fsys, root)
	if err != nil {
		t.Fatal(err)
	}
	gen := FixtureGenerator{FS: fsys, Root: root}
	if gen.Label() != "fixture" {
		t.Errorf("Label = %q, want fixture", gen.Label())
	}
	for _, s := range specs {
		src, err := gen.Generate(context.Background(), s)
		if err != nil {
			t.Errorf("Generate(%q): %v", s.Name, err)
			continue
		}
		if !strings.Contains(src, s.Entrypoint) {
			t.Errorf("candidate for %q does not mention entrypoint %q", s.Name, s.Entrypoint)
		}
	}

	if _, err := gen.Generate(context.Background(), Spec{Name: "does-not-exist"}); err == nil {
		t.Error("expected error for missing fixture")
	}
}

func TestCommandGeneratorPipesPromptThroughArgv(t *testing.T) {
	gen := CommandGenerator{Argv: []string{"cat"}}
	src, err := gen.Generate(context.Background(), Spec{Name: "x", Prompt: "package jobs // hi"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(src, "package jobs") {
		t.Errorf("source did not round-trip the prompt: %q", src)
	}

	if _, err := (CommandGenerator{}).Generate(context.Background(), Spec{}); err == nil {
		t.Error("empty argv should error")
	}
	if _, err := (CommandGenerator{Argv: []string{"true"}}).Generate(context.Background(), Spec{Prompt: "x"}); err == nil {
		t.Error("a generator that emits nothing should error")
	}
}

func TestWriteRebasedGoModAbsolutizesLocalReplace(t *testing.T) {
	base := t.TempDir()
	const mod = "module sample\n\ngo 1.26.0\n\nrequire example.com/sdk v1.0.0\n\nreplace example.com/sdk => ..\n"
	if err := os.WriteFile(filepath.Join(base, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "go.mod")
	if err := writeRebasedGoMod(filepath.Join(base, "go.mod"), dst, base); err != nil {
		t.Fatalf("writeRebasedGoMod: %v", err)
	}
	raw, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	mf, err := modfile.Parse(dst, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(mf.Replace) != 1 {
		t.Fatalf("expected 1 replace, got %d", len(mf.Replace))
	}
	got := mf.Replace[0].New.Path
	if !filepath.IsAbs(got) {
		t.Fatalf("replace target %q is not absolute", got)
	}
	want := filepath.Clean(filepath.Join(base, ".."))
	if got != want {
		t.Errorf("replace target = %q, want %q", got, want)
	}
}

func TestWriteRebasedGoModLeavesAbsoluteAndModuleReplaces(t *testing.T) {
	base := t.TempDir()
	abs := filepath.Join(t.TempDir(), "sdk")
	mod := "module sample\n\ngo 1.26.0\n\nreplace example.com/sdk => " + abs + "\nreplace example.com/other => example.com/fork v1.2.3\n"
	if err := os.WriteFile(filepath.Join(base, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "go.mod")
	if err := writeRebasedGoMod(filepath.Join(base, "go.mod"), dst, base); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(dst)
	mf, err := modfile.Parse(dst, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range mf.Replace {
		switch r.Old.Path {
		case "example.com/sdk":
			if r.New.Path != abs {
				t.Errorf("absolute replace mutated: %q != %q", r.New.Path, abs)
			}
		case "example.com/other":
			if r.New.Path != "example.com/fork" || r.New.Version != "v1.2.3" {
				t.Errorf("module replace mutated: %+v", r.New)
			}
		}
	}
}
