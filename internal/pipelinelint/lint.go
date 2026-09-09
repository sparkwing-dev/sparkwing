package pipelinelint

import (
	"sort"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

const (
	RulePlanIO            = "plan-io"
	RulePlanRuntimeBranch = "plan-runtime-branch"
	RuleRunnerLabel       = "runner-label"
	RuleUnusedRef         = "unused-ref"
	RuleGuardMisuse       = "guard-misuse"
	RuleGroupCacheShared  = "group-cache-shared"
	RuleDynamicGroupInert = "dynamic-group-inert"
)

type Finding struct {
	Rule     string `json:"rule"`
	Pipeline string `json:"pipeline,omitempty"`
	Message  string `json:"message"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Col      int    `json:"col,omitempty"`
}

type RuleDoc struct {
	Name    string `json:"name"`
	Forbids string `json:"forbids"`
	Why     string `json:"why"`
}

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
			Forbids: "blank runner labels on Requires/Prefers/WhenRunner, and Inline jobs that also declare Requires/Prefers",
			Why:     "A label the author wrote reads as a constraint either way, and neither shape is one: an empty string is dropped when labels are normalized, so the term vanishes, and a whitespace label survives and matches no runner, so the term can never be satisfied. An Inline job runs in-process, where Requires and Prefers select nothing; declaring both signals confused placement intent. WhenRunner is honored on an inline job, matched against the inline runner, so it is not flagged there.",
		},
		{
			Name:    RuleUnusedRef,
			Forbids: "discarding a Ref result (RefTo) via blank assignment or a bare expression statement",
			Why:     "A Ref is the typed handle a downstream job reads an upstream's output through. Creating one and throwing it away is dead code: either wire it into a job (as a field or closure capture) or drop the producing edge.",
		},
		{
			Name:    RuleGroupCacheShared,
			Forbids: "Memoize() applied to a fan-out or grouped set of jobs",
			Why:     "A group's Memoize applies one key function to every member, so the members share a single cache entry and replay each other's results -- a matrix over Go 1.23 and 1.24 would store one pass and reuse it for both, which looks like a fast green build and is not a build at all. Key each member instead: range over JobGroup.Members() and call Memoize on the *JobNode.",
		},
		{
			Name:    RuleDynamicGroupInert,
			Forbids: "JobGroup setters applied to a JobFanOutDynamic result",
			Why:     "A dynamic group has no members until its source job completes, and every JobGroup setter -- Memoize, Requires, Retry, Needs, Env, and the rest -- applies to the members present when it is called. On a dynamic group that is none of them, so the call compiles, reads as configuration, and changes nothing. Configure the generated jobs from the value the fan-out callback returns; Requires, Prefers, and WhenRunner have provider interfaces a Workable can implement.",
		},
		{
			Name:    RuleGuardMisuse,
			Forbids: "pipeline guards that can never be satisfied together",
			Why:     "A token in both require and reject, require listing both profile:local and profile:controller, or a duplicate token, all describe a pipeline that can never dispatch. Guards are validated for syntax at load; this catches unsatisfiable combinations.",
		},
	}
}

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
