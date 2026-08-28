package pipelinegen

import (
	"bufio"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

type Expectation string

const (
	ExpectPass Expectation = "pass"

	ExpectFail Expectation = "fail"
)

type Spec struct {
	Name string

	Entrypoint string

	Shape string

	Expect Expectation

	GuardRequire []string
	GuardReject  []string

	Prompt string
}

func (s Spec) HasGuards() bool { return len(s.GuardRequire) > 0 || len(s.GuardReject) > 0 }

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
		case "guard-require":
			spec.GuardRequire = splitTokens(val)
		case "guard-reject":
			spec.GuardReject = splitTokens(val)
		default:
			return Spec{}, fmt.Errorf("unknown frontmatter key %q", key)
		}
	}
	return Spec{}, fmt.Errorf("unterminated frontmatter (missing closing ---)")
}

func splitTokens(val string) []string {
	var out []string
	for _, part := range strings.Split(val, ",") {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, t)
		}
	}
	return out
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
