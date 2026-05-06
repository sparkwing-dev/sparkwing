package sparkwing

// Describe surfaces a pipeline's typed-flag schema as a stable JSON
// shape so the wing CLI can parse typed flags, render --help, drive
// tab completion, and feed shells without re-importing the SDK's
// reflect machinery.
//
// DescribePipeline is the wire-format projection of the schema parsed
// by Register[T]: the compiled pipeline binary emits JSON; wing reads
// it.
//
// Pipelines opt into help / examples via the optional provider
// interfaces below.

import (
	"fmt"
	"strings"
	"unicode"
)

// HelpProvider is optionally implemented by pipelines to contribute
// a short description to `wing <name> --help`. One or two sentences
// explaining what the pipeline does and when to use it.
type HelpProvider interface {
	Help() string
}

// ShortHelpProvider is optionally implemented by pipelines to
// contribute a one-line hint (<=80 chars, no trailing period) for
// tab completion and list views. When absent, callers fall back to a
// flattened truncation of Help() or the pipeline's trigger summary.
type ShortHelpProvider interface {
	ShortHelp() string
}

// ExampleProvider is optionally implemented by pipelines to contribute
// worked invocations to `wing <name> --help`. Each entry pairs a
// one-line comment (what it accomplishes) with the exact command a
// user would type.
type ExampleProvider interface {
	Examples() []Example
}

// Example is a single help-screen invocation. Comment is rendered as
// `# <text>` above the command so readers can scan by intent.
type Example struct {
	Comment string `json:"comment"`
	Command string `json:"command"`
}

// DescribePipeline is one pipeline's CLI-facing schema. Emitted as
// JSON by `<pipeline-binary> --describe`; consumed by the wing CLI
// for flag parsing, tab completion, and per-pipeline help output.
type DescribePipeline struct {
	Name     string        `json:"name"`
	Short    string        `json:"short,omitempty"`
	Help     string        `json:"help,omitempty"`
	Examples []Example     `json:"examples,omitempty"`
	Args     []DescribeArg `json:"args"`
	// Extra is true when the pipeline's Inputs struct declares a
	// `flag:",extra"` bag; in that mode unknown flags don't error.
	Extra bool `json:"extra,omitempty"`
	// Venue is the author-declared dispatch constraint
	// ("either" / "local-only" / "cluster-only"). IMP-011: the wing
	// dispatcher gates `--on PROFILE` against this so a pipeline
	// that needs laptop-local credentials (terraform / aws SSO) can
	// refuse remote dispatch at CLI time. Empty string means "venue
	// metadata not present in this cache file" — older binaries
	// pre-IMP-011 omit the field entirely; the dispatcher treats
	// absent + "either" as the same permissive default.
	Venue string `json:"venue,omitempty"`
}

// DescribeArg is one CLI-visible argument. Name is the user-facing
// flag (without leading --); GoName is the original Go field name.
// Type is one of: string, bool, int, int64, float64, duration,
// []string.
type DescribeArg struct {
	Name     string   `json:"name"`
	GoName   string   `json:"go_name"`
	Short    string   `json:"short,omitempty"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Desc     string   `json:"desc,omitempty"`
	Default  string   `json:"default,omitempty"`
	Enum     []string `json:"enum,omitempty"`
	Secret   bool     `json:"secret,omitempty"`
}

// DescribeAll returns the schema for every registered pipeline.
func DescribeAll() ([]DescribePipeline, error) {
	names := Registered()
	out := make([]DescribePipeline, 0, len(names))
	for _, n := range names {
		dp, ok, err := DescribePipelineByName(n)
		if err != nil {
			return nil, fmt.Errorf("describe %q: %w", n, err)
		}
		if !ok {
			continue
		}
		out = append(out, dp)
	}
	return out, nil
}

// DescribePipelineByName returns the schema for one registered
// pipeline.
func DescribePipelineByName(name string) (DescribePipeline, bool, error) {
	reg, ok := Lookup(name)
	if !ok {
		return DescribePipeline{}, false, nil
	}
	dp := DescribePipeline{
		Name:  reg.Name,
		Args:  []DescribeArg{},
		Extra: reg.Schema.Extra,
		Venue: PipelineVenue(reg).String(),
	}
	if inst := reg.instance(); inst != nil {
		if s, ok := inst.(ShortHelpProvider); ok {
			dp.Short = strings.TrimSpace(s.ShortHelp())
		}
		if h, ok := inst.(HelpProvider); ok {
			dp.Help = strings.TrimSpace(h.Help())
		}
		if e, ok := inst.(ExampleProvider); ok {
			dp.Examples = e.Examples()
		}
	}
	for _, f := range reg.Schema.Fields {
		if f.isExtraBag {
			continue
		}
		dp.Args = append(dp.Args, DescribeArg{
			Name:     f.Name,
			GoName:   f.GoName,
			Short:    f.Short,
			Type:     f.Type,
			Required: f.Required,
			Desc:     f.Description,
			Default:  f.Default,
			Enum:     f.Enum,
			Secret:   f.Secret,
		})
	}
	return dp, true, nil
}

// ToKebabCase converts FooBarBaz to foo-bar-baz.
func ToKebabCase(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		if unicode.IsUpper(r) {
			prevLower := i > 0 && unicode.IsLower(runes[i-1])
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if i > 0 && (prevLower || nextLower) {
				b.WriteRune('-')
			}
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
