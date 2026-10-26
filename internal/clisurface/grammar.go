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

// CommonRules fixes shared meanings so a new command cannot reinterpret a flag.
func CommonRules() map[string]string {
	return map[string]string{
		"profile":    "Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile.",
		"repo":       "Repository identity, never a checkout path; typed identity grants no authority.",
		"checkout":   "Local checkout path, never owner/name or a registry basename.",
		"run":        "Stable run ID or per-team run number; '-' reads run IDs from stdin on cancel, retry and prune.",
		"search":     "Match records; never scan log text.",
		"grep":       "Match log text only.",
		"limit":      "Output count at least one; paginated commands continue with --cursor.",
		"follow":     "Stream until the observed work is done.",
		"all":        "Include entries normally hidden.",
		"dry-run":    "Preview with no side effects.",
		"yes":        "Confirm bulk destruction.",
		"force":      "Replace an existing object.",
		"repeatable": "Singular names accept repeated flags and comma lists; --repos is the explicit runner allow-list exception.",
		"short":      "Only -o output, -q quiet, -f follow, -v run verbosity, -C checkout and -h help.",
		"unknown":    "Unknown engine flags fail with exit 2; only arguments after -- belong to the pipeline.",
	}
}
