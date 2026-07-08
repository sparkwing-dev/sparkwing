// Package pipelinelint statically checks a sparkwing pipeline's source
// for the idiomatic anti-patterns that make a Plan() non-deterministic,
// impure, or misconfigured. It is the machine-checkable definition of
// "idiomatic": each rule cites exactly what it forbids and why, so the
// linter can be wired as an enforced gate (non-zero exit on any
// violation) rather than a style suggestion.
//
// Two surfaces are analyzed:
//
//   - Go source (AnalyzeSource): the body of every pipeline Plan method,
//     using go/ast. Only statements that execute while the DAG is being
//     built are inspected; bodies of nested function literals (job/step
//     closures, SkipIf, BeforeRun, ...) run at dispatch and are skipped.
//   - Pipeline config (AnalyzeGuards): the guards: block of each pipeline
//     in sparkwing.yaml, for tokens that can never be satisfied together.
package pipelinelint

import (
	"sort"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

// Rule identifiers. Stable: messages and docs reference them, and an
// operator may grep CI output by rule name.
const (
	RulePlanIO            = "plan-io"
	RulePlanRuntimeBranch = "plan-runtime-branch"
	RuleRunnerLabel       = "runner-label"
	RuleUnusedRef         = "unused-ref"
	RuleGuardMisuse       = "guard-misuse"
)

// Finding is one rule violation. File/Line/Col are zero for findings
// that come from config (guards) rather than source.
type Finding struct {
	Rule     string `json:"rule"`
	Pipeline string `json:"pipeline,omitempty"`
	Message  string `json:"message"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Col      int    `json:"col,omitempty"`
}

// RuleDoc is the human-readable charter for one rule: what it forbids
// and why. Surfaced by `sparkwing pipeline lint --rules` so the rule set
// is self-documenting.
type RuleDoc struct {
	Name    string `json:"name"`
	Forbids string `json:"forbids"`
	Why     string `json:"why"`
}

// Rules returns every rule's charter in stable order.
func Rules() []RuleDoc {
	return []RuleDoc{
		{
			Name:    RulePlanIO,
			Forbids: "I/O calls (shell, exec, file, http) in the Plan() body",
			Why:     "Plan() must be pure-declarative: it builds the DAG and returns. I/O there runs every time the plan is read (explain, plan, dispatch) and is exactly what the runtime plan-guard panics on. Move it into a Job or Step body, which runs at dispatch.",
		},
		{
			Name:    RulePlanRuntimeBranch,
			Forbids: "branching on the runtime environment (os.Getenv, runtime.GOOS/GOARCH, IsLocal) in the Plan() body",
			Why:     "Plan() must be deterministic so explain and dispatch agree on the shape. A Plan that branches on the host environment renders a different DAG depending on where it runs. Express the condition as a job-level SkipIf / Requires or a pipeline guard instead.",
		},
		{
			Name:    RuleRunnerLabel,
			Forbids: "blank runner labels and Inline jobs that also declare Requires/Prefers",
			Why:     "A blank label matches no runner (a typo that strands the job). An Inline job runs in-process, so a runner-label requirement on it can never be honored; declaring both signals confused placement intent.",
		},
		{
			Name:    RuleUnusedRef,
			Forbids: "discarding a Ref result (RefTo) via blank assignment or a bare expression statement",
			Why:     "A Ref is the typed handle a downstream job reads an upstream's output through. Creating one and throwing it away is dead code: either wire it into a job (as a field or closure capture) or drop the producing edge.",
		},
		{
			Name:    RuleGuardMisuse,
			Forbids: "pipeline guards that can never be satisfied together",
			Why:     "A token in both require and reject, require listing both profile:local and profile:controller, or a duplicate token, all describe a pipeline that can never dispatch. Guards are validated for syntax at load; this catches unsatisfiable combinations.",
		},
	}
}

// Analyze runs every rule. sourceDir, when non-empty, is scanned for
// pipeline Plan methods; cfg, when non-nil, supplies the guard tokens.
// Findings are returned sorted by location then rule. An error is
// returned only when the source directory cannot be parsed at all.
func Analyze(sourceDir string, cfg *pipelines.Config) ([]Finding, error) {
	var findings []Finding
	if sourceDir != "" {
		src, err := AnalyzeSource(sourceDir)
		if err != nil {
			return nil, err
		}
		findings = append(findings, src...)
	}
	findings = append(findings, AnalyzeGuards(cfg)...)
	sortFindings(findings)
	return findings, nil
}

// AnalyzeGuards checks every pipeline's guards: block for token
// combinations that can never be satisfied. Syntax is already validated
// at config load (pipelines.Guards.Validate); this layer catches
// unsatisfiable pipelines that parse cleanly.
func AnalyzeGuards(cfg *pipelines.Config) []Finding {
	if cfg == nil {
		return nil
	}
	var findings []Finding
	for i := range cfg.Pipelines {
		p := &cfg.Pipelines[i]
		findings = append(findings, guardFindings(p)...)
	}
	return findings
}

func guardFindings(p *pipelines.Pipeline) []Finding {
	var out []Finding
	add := func(msg string) {
		out = append(out, Finding{Rule: RuleGuardMisuse, Pipeline: p.Name, Message: msg})
	}

	require := p.Guards.Require
	reject := p.Guards.Reject

	out = append(out, dupGuardFindings(p.Name, "require", require)...)
	out = append(out, dupGuardFindings(p.Name, "reject", reject)...)

	rejectSet := map[string]struct{}{}
	for _, t := range reject {
		rejectSet[t] = struct{}{}
	}
	seenContradiction := map[string]struct{}{}
	for _, t := range require {
		if _, ok := rejectSet[t]; ok {
			if _, dup := seenContradiction[t]; dup {
				continue
			}
			seenContradiction[t] = struct{}{}
			add("guard " + quote(t) + " is in both require and reject; the pipeline can never dispatch")
		}
	}

	if containsToken(require, "profile:local") && containsToken(require, "profile:controller") {
		add("require lists both profile:local and profile:controller, which are mutually exclusive; the pipeline can never dispatch")
	}
	return out
}

func dupGuardFindings(pipeline, field string, tokens []string) []Finding {
	var out []Finding
	seen := map[string]struct{}{}
	reported := map[string]struct{}{}
	for _, t := range tokens {
		if _, ok := seen[t]; ok {
			if _, done := reported[t]; done {
				continue
			}
			reported[t] = struct{}{}
			out = append(out, Finding{
				Rule:     RuleGuardMisuse,
				Pipeline: pipeline,
				Message:  "duplicate guard " + quote(t) + " in " + field,
			})
			continue
		}
		seen[t] = struct{}{}
	}
	return out
}

func containsToken(tokens []string, want string) bool {
	for _, t := range tokens {
		if t == want {
			return true
		}
	}
	return false
}

func quote(s string) string { return "\"" + s + "\"" }

func sortFindings(f []Finding) {
	sort.SliceStable(f, func(i, j int) bool {
		if f[i].File != f[j].File {
			return f[i].File < f[j].File
		}
		if f[i].Line != f[j].Line {
			return f[i].Line < f[j].Line
		}
		if f[i].Col != f[j].Col {
			return f[i].Col < f[j].Col
		}
		if f[i].Pipeline != f[j].Pipeline {
			return f[i].Pipeline < f[j].Pipeline
		}
		return f[i].Rule < f[j].Rule
	})
}
