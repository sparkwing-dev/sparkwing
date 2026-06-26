// Package pipelinegen is the AI-generation eval harness for sparkwing
// pipelines: a corpus of natural-language pipeline specs, a generator
// that turns each spec into pipeline source, and a scorer that runs the
// generation through the same bar a human pipeline must clear --
// compile, `pipeline explain`, and `pipeline lint`. The harness reports
// a pass-rate and wall-clock latency so "AI can generate idiomatic
// pipelines" is measured, not asserted.
//
// The corpus is fixture-backed by default (each spec ships the source a
// generator is expected to produce, so a run is deterministic and
// reproducible) and pluggable: pass a generator command to score a live
// model instead. Specs carry an expected outcome -- a deliberately bad
// generation is expected to fail lint or explain -- so the corpus is
// also a regression gate on the linter and the template catalog.
package pipelinegen

import (
	"bufio"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Expectation is whether a generated pipeline should clear the
// compile+explain+lint bar.
type Expectation string

const (
	// ExpectPass marks an idiomatic spec: the generation must compile,
	// explain clean, and lint clean.
	ExpectPass Expectation = "pass"
	// ExpectFail marks a deliberately bad spec: the generation must be
	// rejected by at least one check (lint or explain).
	ExpectFail Expectation = "fail"
)

// Spec is one corpus entry: a natural-language prompt plus the metadata
// needed to wire and score the generation it should produce.
type Spec struct {
	// Name is the registered pipeline name the generation uses; it is
	// also the corpus directory name.
	Name string
	// Entrypoint is the Go struct the generation registers under Name.
	Entrypoint string
	// Shape is a free-form label for the pipeline shape (gate, release,
	// matrix-fanout, approval-gated, ...); used only to group the report.
	Shape string
	// Expect is whether scoring should pass or fail.
	Expect Expectation
	// Prompt is the natural-language spec a generator turns into source.
	Prompt string
}

// LoadCorpus reads every spec under root in fsys. Each immediate
// subdirectory is one spec, identified by a spec.md carrying the
// frontmatter (shape, expect, entrypoint) and the prompt body. Specs
// are returned sorted by name for a reproducible report ordering.
func LoadCorpus(fsys fs.FS, root string) ([]Spec, error) {
	entries, err := fs.ReadDir(fsys, root)
	if err != nil {
		return nil, fmt.Errorf("read corpus %q: %w", root, err)
	}
	var specs []Spec
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		raw, err := fs.ReadFile(fsys, path.Join(root, name, "spec.md"))
		if err != nil {
			return nil, fmt.Errorf("spec %q: %w", name, err)
		}
		spec, err := parseSpec(name, string(raw))
		if err != nil {
			return nil, fmt.Errorf("spec %q: %w", name, err)
		}
		specs = append(specs, spec)
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("corpus %q is empty", root)
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs, nil
}

// parseSpec reads a spec.md: a `---`-delimited key/value frontmatter
// block followed by the free-form prompt. Recognized keys are shape,
// expect, and entrypoint; name comes from the directory.
func parseSpec(name, content string) (Spec, error) {
	spec := Spec{Name: name}
	sc := bufio.NewScanner(strings.NewReader(content))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return Spec{}, fmt.Errorf("missing leading --- frontmatter")
	}
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			var body strings.Builder
			for sc.Scan() {
				body.WriteString(sc.Text())
				body.WriteByte('\n')
			}
			spec.Prompt = strings.TrimSpace(body.String())
			return validateSpec(spec)
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			return Spec{}, fmt.Errorf("frontmatter line %q is not key: value", line)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "shape":
			spec.Shape = val
		case "entrypoint":
			spec.Entrypoint = val
		case "expect":
			spec.Expect = Expectation(val)
		default:
			return Spec{}, fmt.Errorf("unknown frontmatter key %q", key)
		}
	}
	return Spec{}, fmt.Errorf("unterminated frontmatter (missing closing ---)")
}

func validateSpec(s Spec) (Spec, error) {
	if s.Entrypoint == "" {
		return Spec{}, fmt.Errorf("entrypoint is required")
	}
	if s.Expect != ExpectPass && s.Expect != ExpectFail {
		return Spec{}, fmt.Errorf("expect must be pass or fail, got %q", s.Expect)
	}
	if strings.TrimSpace(s.Prompt) == "" {
		return Spec{}, fmt.Errorf("prompt body is empty")
	}
	if s.Shape == "" {
		s.Shape = "unspecified"
	}
	return s, nil
}
