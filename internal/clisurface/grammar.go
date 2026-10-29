// Package clisurface owns the final command grammar independently of execution.
package clisurface

// Authority separates team credentials from deployment and in-pod powers.
type Authority string

const (
	teamCredential  Authority = "team"
	deploymentAdmin Authority = "deployment_admin"
	inPodBreakGlass Authority = "in_pod_break_glass"
)

// Flag is parser and help metadata; Argument empty means a boolean switch.
type Flag struct {
	Name       string
	Short      string
	Argument   string
	Values     []string
	Repeatable bool
	CommaList  bool
	AllowEmpty bool
	Required   bool
	Minimum    int64
	HasMinimum bool
	Requires   []string
	Conflicts  []string
	Meaning    string
}

// Command separates command selectors, pipeline argument boundaries and authority metadata.
type Command struct {
	Path                       string
	Flags                      []Flag
	Hidden                     bool
	Platforms                  []string
	Authority                  Authority
	PipelineArgument           bool
	PipelineOptionalOnResume   bool
	PipelineArgsAfterSeparator bool
	Rules                      []string
}

// GlobalFlags returns flags inherited by every command.
func GlobalFlags() []Flag {
	return []Flag{
		{Name: "output", Short: "o", Argument: "FORMAT", Values: []string{"pretty", "json", "plain"}, Meaning: "Pretty on a terminal; JSON when piped; explicit format wins."},
		{Name: "checkout", Short: "C", Argument: "PATH", Meaning: "Resolve the project from this local checkout path."},
		{Name: "help", Short: "h", Meaning: "Show command help without executing."},
		{Name: "help-all", Meaning: "Include uncommon flags in command help."},
	}
}

// ExitCode keeps usage failures distinguishable from incomplete observations.
type ExitCode int

const (
	ExitUsage ExitCode = 2
)
