package sparkwing

// Describe surfaces a pipeline's typed-flag schema as a stable JSON
// shape so the sparkwing CLI can parse typed flags, render --help, drive
// tab completion, and feed shells without re-importing the SDK's
// reflect machinery.
//
// DescribePipeline is the wire-format projection of the schema parsed
// by Register[T]: the compiled pipeline binary emits JSON; sparkwing
// reads it.
//
// Pipelines opt into help / examples via the optional provider
// interfaces below.

// (no imports -- types only)

// HelpProvider is optionally implemented by pipelines to contribute
// a short description to `sparkwing run <name> --help`. One or two sentences
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
// worked invocations to `sparkwing run <name> --help`. Each entry pairs a
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

// EnvVarDocer is an optional interface a Pipeline can implement to
// declare environment variables it reads as inputs. When implemented,
// `sparkwing run <pipeline> --help` surfaces these alongside the typed
// Inputs (declared via [Register]).
//
// Prefer typed Inputs for values the user controls -- they show up in
// --help automatically and benefit from the type system.
// EnvVarDocer is for the cases where env vars are genuinely the right
// shape: process-wide config, integration with external systems that
// already use env, or tunables the operator sets outside the
// invocation.
type EnvVarDocer interface {
	EnvVars() []EnvVarDoc
}

// EnvVarDoc is one declared env var read by a pipeline. Default is
// optional (empty when the pipeline has no default).
type EnvVarDoc struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Default     string `json:"default,omitempty"`
}

// DescribePipeline is one pipeline's CLI-facing schema. Emitted as
// JSON by `<pipeline-binary> --describe`; consumed by the sparkwing CLI
// for flag parsing, tab completion, and per-pipeline help output.
type DescribePipeline struct {
	Name     string        `json:"name"`
	Short    string        `json:"short,omitempty"`
	Help     string        `json:"help,omitempty"`
	Examples []Example     `json:"examples,omitempty"`
	Args     []DescribeArg `json:"args"`
	// EnvVars are environment variables the pipeline reads as inputs,
	// declared via the optional [EnvVarDocer] interface. Empty unless
	// the pipeline opts in.
	EnvVars []EnvVarDoc `json:"env_vars,omitempty"`
	// Extra is true when the pipeline's Inputs struct declares a
	// `flag:",extra"` bag; in that mode unknown flags don't error.
	Extra bool `json:"extra,omitempty"`
	// Risks is the sorted, deduplicated union of per-step risk labels
	// declared anywhere in this pipeline's plan. The dispatcher walks
	// this set against --sw-allow so an operator or agent dispatching
	// a pipeline that calls a risk-labeled Step gets a hard refusal
	// until every label is acknowledged (or --sw-dry-run bypasses).
	// Empty when no step declares a risk; the gate stays silent.
	Risks []string `json:"risks,omitempty"`
	// RisksBySteps is the per-step breakdown so a renderer or agent
	// can show "this is the step that will refuse" in the error path.
	// Only populated for pipelines whose Plan() builds successfully
	// without args during --describe; pipelines with required Inputs
	// degrade to the union field above.
	RisksBySteps []DescribeStepRisks `json:"risks_by_step,omitempty"`
}

// DescribeStepRisks is one row of the per-step risk-label list.
// StepID is the inner WorkStep id (within the Plan's Job graph);
// Labels are the author-declared risk labels on that step.
type DescribeStepRisks struct {
	NodeID string   `json:"node_id"`
	StepID string   `json:"step_id"`
	Labels []string `json:"labels"`
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
	// JobID identifies the job that declared this arg when the arg
	// came from a [WithArgs] embedding rather than the pipeline-level
	// [Register] Inputs struct. Empty for pipeline-level args. Used by
	// the help renderer to group "from job X" annotations and by
	// tooling that needs to attribute flags to their source.
	JobID string `json:"job_id,omitempty"`
}
