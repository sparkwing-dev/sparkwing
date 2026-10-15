package sparkwing

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

// DescribeArg is one CLI-visible argument. Name is the user-facing
// flag (without leading --); GoName is the original Go field name.
// Type is one of: string, bool, int, int64, float64, duration,
// []string.
type describeArg struct {
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
