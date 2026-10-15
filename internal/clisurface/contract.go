// safety: Package clisurface owns the final command grammar independently of execution.
package clisurface

import (
	"fmt"
	"strings"
)

// safety: Authority separates team credentials from deployment and in-pod powers.
type Authority string

// safety: These authorities never substitute for the operator-console session.
const (
	TeamCredential  Authority = "team"
	DeploymentAdmin Authority = "deployment_admin"
	InPodBreakGlass Authority = "in_pod_break_glass"
)

// safety: Flag is parser and help metadata; Argument empty means a boolean switch.
// Minimum applies only when HasMinimum is true. Empty Values accepts the named value grammar.
// Required means explicit presence, and Requires and Conflicts refer to long flag names.
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

// safety: GlobalFlags is inherited everywhere; profile selection is command-local,
// but an accepted --profile may appear before the verb. A global flag after -- is a
// pipeline argument. List JSON is NDJSON, single documents are one compact JSON line.
func GlobalFlags() []Flag {
	return []Flag{
		{Name: "output", Short: "o", Argument: "FORMAT", Values: []string{"pretty", "json", "plain"}, Meaning: "Pretty on a terminal; JSON when piped; explicit format wins."},
		{Name: "checkout", Short: "C", Argument: "PATH", Meaning: "Resolve the project from this local checkout path."},
		{Name: "help", Short: "h", Meaning: "Show command help without executing."},
		{Name: "help-all", Meaning: "Include uncommon flags in command help."},
	}
}

// safety: ExitCode keeps usage failures distinguishable from incomplete observations.
type ExitCode int

// safety: These four values apply to every executable in the registry.
const (
	ExitOK      ExitCode = 0
	ExitFailed  ExitCode = 1
	ExitUsage   ExitCode = 2
	ExitUnknown ExitCode = 3
)

// safety: ErrorFormat delegates JSON encoding and codes to internal/apierr;
// the CLI must not create another error envelope or leak pipeline secrets in hints.
type ErrorFormat struct {
	Text        string
	Hint        string
	JSONOwner   string
	JSONFraming string
}

// safety: Errors keeps diagnostics on stderr even when stdout contains records.
func Errors() ErrorFormat {
	return ErrorFormat{
		Text:        "<binary>: <command path without binary>: <message>",
		Hint:        "hint: <hint>",
		JSONOwner:   "internal/apierr",
		JSONFraming: "one compact error envelope per line on stderr",
	}
}

// safety: CommonRules fixes shared meanings so a new command cannot reinterpret a flag.
func CommonRules() map[string]string {
	return map[string]string{
		"profile":    "Connection profiles come only from the user's config; allowed before the verb if the command accepts it. Local run refuses --profile.",
		"repo":       "Repository identity, never a checkout path; typed identity grants no authority.",
		"checkout":   "Local checkout path, never owner/name or a registry basename.",
		"run":        "Stable run ID or per-team run number; '-' reads run IDs from stdin on cancel, retry and prune.",
		"search":     "Match records; never scan log text.",
		"grep":       "Match log text only.",
		"limit":      "Pagination count at least one, continued with --cursor; zero is invalid.",
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

// safety: Validate catches grammar drift before a parser or help generator consumes it.
func Validate(commands []Command) error {
	paths := make(map[string]bool, len(commands))
	for _, c := range commands {
		if c.Path == "" || strings.TrimSpace(c.Path) != c.Path || paths[c.Path] {
			return fmt.Errorf("invalid or duplicate command %q", c.Path)
		}
		paths[c.Path] = true
	}
	for _, c := range commands {
		if i := strings.LastIndexByte(c.Path, ' '); i >= 0 && !paths[c.Path[:i]] {
			return fmt.Errorf("%s: missing parent", c.Path)
		}
		pipeline := c.Path == "sparkwing run" || c.Path == "sparkwing pipeline trigger"
		if c.PipelineArgument != pipeline || c.PipelineArgsAfterSeparator != (pipeline || c.Path == "sparkwing pipeline plan") {
			return fmt.Errorf("%s: invalid positional grammar", c.Path)
		}
		names, shorts := map[string]bool{}, map[string]bool{}
		flags := append(GlobalFlags(), c.Flags...)
		for _, f := range flags {
			if f.Name == "" || names[f.Name] {
				return fmt.Errorf("%s: duplicate or empty flag %q", c.Path, f.Name)
			}
			names[f.Name] = true
			if f.Short != "" {
				expected := map[string]string{"o": "output", "q": "quiet", "f": "follow", "v": "sw-verbose", "C": "checkout", "h": "help"}
				if expected[f.Short] != f.Name || shorts[f.Short] {
					return fmt.Errorf("%s: invalid short flag %q", c.Path, f.Short)
				}
				shorts[f.Short] = true
			}
			if f.Repeatable != f.CommaList || (f.Repeatable && f.Argument == "") {
				return fmt.Errorf("%s: invalid repetition for --%s", c.Path, f.Name)
			}
			if f.Name == "limit" && (!f.HasMinimum || f.Minimum != 1) {
				return fmt.Errorf("%s: invalid page limit", c.Path)
			}
			if f.Name == "name" || f.Name == "id" || f.Name == "prefix" || f.Name == "node" {
				return fmt.Errorf("%s: non-kind selector --%s", c.Path, f.Name)
			}
			if f.Name == "grep" && c.Path != "sparkwing runs grep" && c.Path != "sparkwing runs logs" {
				return fmt.Errorf("%s: grep outside logs", c.Path)
			}
		}
		for _, f := range flags {
			for _, n := range append(append([]string{}, f.Requires...), f.Conflicts...) {
				if !names[n] || n == f.Name {
					return fmt.Errorf("%s: invalid flag relation --%s -> --%s", c.Path, f.Name, n)
				}
			}
		}
		if names["limit"] && !names["cursor"] {
			return fmt.Errorf("%s: limit needs cursor", c.Path)
		}
	}
	return nil
}
