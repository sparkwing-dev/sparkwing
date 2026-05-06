package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"golang.org/x/term"
)

// Main is the entry point for .sparkwing/main.go. Subcommands:
// --describe (JSON pipeline schemas), <pipeline> (local run),
// handle-trigger <id>, run-node <runID> <nodeID>, replay-node.
// Cluster-mode subcommands live in cluster.Main to keep heavy deps
// out of consumer pipeline binaries.
func Main() {
	if len(os.Args) > 1 && os.Args[1] == "--describe" {
		described, err := sparkwing.DescribeAll()
		if err != nil {
			fmt.Fprintln(os.Stderr, "describe:", err)
			os.Exit(1)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(described); err != nil {
			fmt.Fprintln(os.Stderr, "describe encode:", err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "handle-trigger" {
		if err := runHandleTriggerCLI(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "handle-trigger:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "run-node" {
		if err := runNodeCLI(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "run-node:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "replay-node" {
		if err := runReplayNodeCLI(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "replay-node:", err)
			os.Exit(1)
		}
		return
	}

	args := os.Args[1:]
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		printUsage()
		os.Exit(2)
	}
	pipeline := args[0]
	rest := args[1:]

	// --help short-circuits before typed-flag parsing.
	for _, tok := range rest {
		if tok == "-h" || tok == "--help" {
			if err := printPipelineHelp(pipeline); err != nil {
				fmt.Fprintln(os.Stderr, pipeline+":", err)
				os.Exit(1)
			}
			return
		}
	}

	// --explain emits the plan snapshot without dispatching.
	for _, tok := range rest {
		if tok == "--explain" {
			if err := printPipelinePlan(pipeline, filterTok(rest, "--explain")); err != nil {
				fmt.Fprintln(os.Stderr, pipeline+":", err)
				os.Exit(1)
			}
			return
		}
	}

	// IMP-013: --plan emits the runtime-resolved plan preview --
	// same DAG as --explain plus per-step would-run / would-skip
	// decisions evaluated against the supplied args + --start-at /
	// --stop-at bounds. NO step bodies execute.
	for _, tok := range rest {
		if tok == "--plan" {
			if err := printPipelineRuntimePlan(pipeline, filterTok(rest, "--plan")); err != nil {
				fmt.Fprintln(os.Stderr, pipeline+":", err)
				os.Exit(1)
			}
			return
		}
	}

	argsMap, err := parseTypedFlags(pipeline, rest)
	if err != nil {
		fmt.Fprintln(os.Stderr, pipeline+":", err)
		os.Exit(2)
	}

	paths, err := DefaultPaths()
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve paths:", err)
		os.Exit(1)
	}

	delegate := selectLocalRenderer()
	opts := Options{
		Pipeline:    pipeline,
		Args:        argsMap,
		Git:         detectGit(),
		Delegate:    delegate,
		Debug:       readDebugDirectivesFromEnv(),
		RetryOf:     os.Getenv("SPARKWING_RETRY_OF"),
		Full:        os.Getenv("SPARKWING_RETRY_FULL") == "1",
		StartAt:     os.Getenv("SPARKWING_START_AT"),
		StopAt:      os.Getenv("SPARKWING_STOP_AT"),
		DryRun:      os.Getenv("SPARKWING_DRY_RUN") == "1",
		MaxParallel: runtime.NumCPU(),
	}
	if applyErr := applyCIEmbeddedEnv(&opts); applyErr != nil {
		fmt.Fprintln(os.Stderr, "wing:", applyErr)
		os.Exit(1)
	}
	// --secrets PROF: resolve via SPARKWING_SECRETS_PROFILE.
	if prof := os.Getenv("SPARKWING_SECRETS_PROFILE"); prof != "" {
		src, perr := remoteSecretSource(prof)
		if perr != nil {
			fmt.Fprintln(os.Stderr, "wing: --secrets:", perr)
			os.Exit(1)
		}
		opts.SecretSource = src
	}

	res, err := RunLocal(context.Background(), paths, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(1)
	}
	// IMP-010: run_finish is emitted inside Run() so the envelope
	// tee captures it. The previous outer emission here happened
	// after RunLocal had already closed the envelope file, leaving
	// `runs logs --follow` without a terminal event. Keep the
	// non-zero exit so wrapper scripts still see the failure.
	_ = delegate
	if res.Status != "success" {
		os.Exit(1)
	}
}

// selectLocalRenderer chooses the live delegate based on
// SPARKWING_LOG_FORMAT (explicit) or stdout TTY (default).
func selectLocalRenderer() sparkwing.Logger {
	switch strings.ToLower(os.Getenv("SPARKWING_LOG_FORMAT")) {
	case "json":
		return NewJSONRenderer()
	case "pretty":
		return NewPrettyRenderer()
	}
	if isInteractiveStdout() {
		return NewPrettyRenderer()
	}
	return NewJSONRenderer()
}

// isInteractiveStdout duplicates pkg/color.IsInteractiveStdout to keep
// pkg/color out of consumer pipeline deps. Keep in sync.
func isInteractiveStdout() bool {
	if term.IsTerminal(int(os.Stdout.Fd())) {
		return true
	}
	if runtime.GOOS != "windows" {
		return false
	}
	if os.Getenv("MSYSTEM") != "" {
		return true
	}
	if os.Getenv("TERM_PROGRAM") == "mintty" {
		return true
	}
	switch t := os.Getenv("TERM"); {
	case t == "":
		return false
	case strings.Contains(t, "xterm"), strings.Contains(t, "cygwin"):
		return true
	}
	return false
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage: <pipeline> [--flag VALUE ...]")
	fmt.Fprintln(os.Stderr, "       --describe")
	fmt.Fprintln(os.Stderr, "       handle-trigger <id> --controller URL")
	fmt.Fprintln(os.Stderr, "       run-node <runID> <nodeID> --controller URL")
	if registered := sparkwing.Registered(); len(registered) > 0 {
		fmt.Fprintln(os.Stderr, "\nregistered pipelines:")
		for _, n := range registered {
			fmt.Fprintln(os.Stderr, "  "+n)
		}
	}
}

// printPipelineHelp dumps a help page from the reflected Argser schema.
func printPipelineHelp(pipeline string) error {
	schema, ok, err := sparkwing.DescribePipelineByName(pipeline)
	if err != nil {
		return err
	}
	if !ok {
		return unknownPipelineErr(pipeline)
	}
	w := os.Stdout
	if schema.Help != "" {
		fmt.Fprintln(w, "DESCRIPTION")
		fmt.Fprintf(w, "  %s\n\n", schema.Help)
	}
	fmt.Fprintln(w, "USAGE")
	fmt.Fprintf(w, "  wing %s", schema.Name)
	for _, a := range schema.Args {
		if a.Required {
			fmt.Fprintf(w, " --%s <%s>", a.Name, a.Type)
		}
	}
	if len(schema.Args) > 0 {
		fmt.Fprint(w, " [flags]")
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w)
	if len(schema.Args) > 0 {
		fmt.Fprintln(w, "PIPELINE FLAGS")
		for _, a := range schema.Args {
			tag := "[optional]"
			if a.Required {
				tag = "[required]"
			}
			head := "--" + a.Name
			if a.Short != "" {
				head = "-" + a.Short + ", " + head
			}
			head += " <" + a.Type + ">"
			suffix := ""
			if a.Default != "" {
				suffix += fmt.Sprintf(" (default: %s)", a.Default)
			}
			if len(a.Enum) > 0 {
				suffix += fmt.Sprintf(" [enum: %s]", strings.Join(a.Enum, "|"))
			}
			if a.Secret {
				suffix += " [secret]"
			}
			fmt.Fprintf(w, "  %-30s %s %s  %s%s\n",
				head, tag, a.Type, a.Desc, suffix)
		}
		if schema.Extra {
			fmt.Fprintln(w, "  (additional unrecognized flags are forwarded to the pipeline)")
		}
		fmt.Fprintln(w)
	} else {
		fmt.Fprintln(w, "No pipeline-specific flags.")
		fmt.Fprintln(w)
	}
	if len(schema.Examples) > 0 {
		fmt.Fprintln(w, "EXAMPLES")
		for i, ex := range schema.Examples {
			if i > 0 {
				fmt.Fprintln(w)
			}
			if ex.Comment != "" {
				fmt.Fprintf(w, "  # %s\n", ex.Comment)
			}
			fmt.Fprintf(w, "  %s\n", ex.Command)
		}
		fmt.Fprintln(w)
	}
	// IMP-039: enumerate wing-owned flags from sparkwing.WingFlagDocs()
	// so this footer stays in lockstep with `wing --help` /
	// `sparkwing run --help`. The previous hand-coded line
	// (`-- only --on, --from, --config`) silently drifted whenever a
	// new wing flag landed (--start-at, --dry-run, --allow-* were all
	// invisible despite working end-to-end). Sourcing from one list
	// future-proofs additions.
	printWingFlagsSection(w)
	return nil
}

// printWingFlagsSection renders the "WING FLAGS" block of per-pipeline
// help. Groups (Source / Range / Safety / System) section the output;
// within each group, flags walk in WingFlagDocs order. The trailing
// hint points users at the top-level help for prose detail.
//
// Takes io.Writer (not *os.File) so tests can capture into a buffer.
func printWingFlagsSection(w io.Writer) {
	docs := sparkwing.WingFlagDocs()
	if len(docs) == 0 {
		return
	}
	fmt.Fprintln(w, "WING FLAGS")
	// Order groups in declaration order: walk docs once and emit a
	// blank-line separator the first time a new group appears. Keeps
	// rendering deterministic without a hardcoded group list.
	prevGroup := ""
	first := true
	for _, d := range docs {
		if d.Group != prevGroup {
			if !first {
				fmt.Fprintln(w)
			}
			fmt.Fprintf(w, "  [%s]\n", d.Group)
			prevGroup = d.Group
		}
		first = false
		head := "--" + d.Name
		if d.Short != "" {
			head = "-" + d.Short + ", " + head
		}
		if d.Argument != "" {
			head += " " + d.Argument
		}
		fmt.Fprintf(w, "    %-30s %s\n", head, d.Desc)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "See `wing --help` for prose explanations of each wing flag.")
}

// printPipelinePlan emits the plan snapshot without dispatch. Missing
// required args are non-fatal; a best-effort plan is more useful for
// inspection.
//
// CLI-017: the inner pipeline binary is invoked with the user's full
// argv (e.g. `wing X --explain --skip Y -o json`). `-o` / `--output` /
// `--json` are explain-output formatting flags owned by the wrapper,
// not pipeline args -- they must be stripped before parseTypedFlags
// sees them. Otherwise an unknown-flag error in parseTypedFlags falls
// back to an empty argsMap, silently dropping `--skip` / `--only` /
// any other typed pipeline flag the user passed alongside `-o`. The
// result was a Plan rendered with no SkipFilter applied -- diverging
// from `wing X --explain --skip Y` (no `-o`), which parsed cleanly.
func printPipelinePlan(pipeline string, rest []string) error {
	reg, ok := sparkwing.Lookup(pipeline)
	if !ok {
		return unknownPipelineErr(pipeline)
	}
	rest = stripExplainOutputFlags(rest)
	argsMap, err := parseTypedFlags(pipeline, rest)
	if err != nil {
		argsMap = map[string]string{}
	}
	rc := sparkwing.RunContext{
		Pipeline: pipeline,
		RunID:    "explain",
	}
	plan, err := reg.Invoke(context.Background(), argsMap, rc)
	if err != nil {
		return fmt.Errorf("build plan: %w", err)
	}
	snap, err := marshalPlanSnapshot(plan, rc)
	if err != nil {
		return fmt.Errorf("marshal plan: %w", err)
	}
	os.Stdout.Write(snap)
	fmt.Println()
	return nil
}

// filterTok drops every occurrence of drop from args.
func filterTok(args []string, drop string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a != drop {
			out = append(out, a)
		}
	}
	return out
}

// stripExplainOutputFlags removes explain-output formatting flags
// (`-o` / `--output` / `--json`) from args. The pipeline binary
// always emits a JSON plan snapshot for `--explain`; the surrounding
// `sparkwing pipeline explain` / `wing` wrapper is responsible for
// any pretty-printing, so these flags are noise to the inner Plan-
// builder. Stripping them keeps parseTypedFlags from rejecting them
// as unknown -- which used to drop *all* typed flags (including
// --skip / --only) into an empty map and silently disable
// SkipFilter. CLI-017.
func stripExplainOutputFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		tok := args[i]
		switch {
		case tok == "--json", tok == "--json=true", tok == "--json=false":
			// Boolean toggle; consumed.
		case tok == "-o", tok == "--output":
			// Two-token flag; consume the value too if present and
			// not itself a flag (defensive against malformed input).
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		case strings.HasPrefix(tok, "-o="),
			strings.HasPrefix(tok, "--output="),
			strings.HasPrefix(tok, "--json="):
			// Single-token =value form; consumed.
		default:
			out = append(out, tok)
		}
	}
	return out
}

// parseTypedFlags reflects the pipeline's Argser schema and parses
// args into a string map. Bool flags accept "--flag" or "--flag=v";
// others require values. Unknown flags are rejected unless the
// schema has Extra=true. Enums are validated.
func parseTypedFlags(pipeline string, args []string) (map[string]string, error) {
	schema, ok, err := sparkwing.DescribePipelineByName(pipeline)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("unknown pipeline %q", pipeline)
	}
	byName := map[string]sparkwing.DescribeArg{}
	byShort := map[string]sparkwing.DescribeArg{}
	for _, a := range schema.Args {
		byName[a.Name] = a
		if a.Short != "" {
			byShort[a.Short] = a
		}
	}
	out := map[string]string{}
	seen := map[string]bool{}
	i := 0
	for i < len(args) {
		tok := args[i]
		isShort := strings.HasPrefix(tok, "-") && !strings.HasPrefix(tok, "--")
		if !strings.HasPrefix(tok, "--") && !isShort {
			return nil, fmt.Errorf("unexpected positional argument %q", tok)
		}
		var name string
		if isShort {
			name = strings.TrimPrefix(tok, "-")
		} else {
			name = strings.TrimPrefix(tok, "--")
		}
		var value string
		hasEq := false
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			value = name[eq+1:]
			name = name[:eq]
			hasEq = true
		}
		var arg sparkwing.DescribeArg
		var found bool
		if isShort {
			arg, found = byShort[name]
			if found {
				name = arg.Name
			}
		} else {
			arg, found = byName[name]
		}
		if !found {
			if schema.Extra {
				// Forward to the bag field; treated as scalar string.
				if !hasEq {
					if i+1 >= len(args) {
						return nil, fmt.Errorf("flag --%s expects a value (extra-bag forwarding)", name)
					}
					value = args[i+1]
					i += 2
				} else {
					i++
				}
				out[name] = value
				continue
			}
			return nil, fmt.Errorf("unknown flag --%s (run `wing %s --help` for valid flags)", name, pipeline)
		}
		if arg.Type == "bool" {
			if !hasEq {
				value = "true"
			}
			out[arg.Name] = value
			seen[arg.Name] = true
			i++
			continue
		}
		if !hasEq {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag --%s expects a value", arg.Name)
			}
			value = args[i+1]
			i += 2
		} else {
			i++
		}
		if len(arg.Enum) > 0 && !inEnumList(value, arg.Enum) {
			return nil, fmt.Errorf("--%s=%q not allowed (must be one of %s)",
				arg.Name, value, strings.Join(arg.Enum, ", "))
		}
		out[arg.Name] = value
		seen[arg.Name] = true
	}
	for _, a := range schema.Args {
		if a.Required && !seen[a.Name] {
			return nil, fmt.Errorf("flag --%s is required", a.Name)
		}
	}
	for _, a := range schema.Args {
		if _, ok := out[a.Name]; ok {
			continue
		}
		if a.Default != "" {
			out[a.Name] = a.Default
		}
	}
	return out, nil
}

func inEnumList(v string, enum []string) bool {
	for _, e := range enum {
		if e == v {
			return true
		}
	}
	return false
}

// readDebugDirectivesFromEnv resolves SPARKWING_DEBUG_PAUSE_* vars.
func readDebugDirectivesFromEnv() DebugDirectives {
	d := DebugDirectives{}
	if v := os.Getenv("SPARKWING_DEBUG_PAUSE_BEFORE"); v != "" {
		d.PauseBefore = splitCommaClean(v)
	}
	if v := os.Getenv("SPARKWING_DEBUG_PAUSE_AFTER"); v != "" {
		d.PauseAfter = splitCommaClean(v)
	}
	if v := os.Getenv("SPARKWING_DEBUG_PAUSE_ON_FAILURE"); v == "1" || v == "true" {
		d.PauseOnFailure = true
	}
	return d
}

func splitCommaClean(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// detectGit populates *Git via best-effort `git` calls. Missing git
// or repo yields a workDir-only Git so plan code can call live
// methods safely.
func detectGit() *sparkwing.Git {
	g := &sparkwing.Git{}
	if cwd, err := os.Getwd(); err == nil {
		g = sparkwing.NewGit(cwd, "", "", "", "")
	}
	if out, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		g.SHA = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		branch := strings.TrimSpace(string(out))
		if branch != "HEAD" { // detached -> empty
			g.Branch = branch
		}
	}
	if out, err := exec.Command("git", "remote", "get-url", "origin").Output(); err == nil {
		g.RepoURL = strings.TrimSpace(string(out))
		owner, repo := parseGithubURL(g.RepoURL)
		if owner != "" && repo != "" {
			g.Repo = owner + "/" + repo
		}
	}
	return g
}

// parseGithubURL extracts owner/repo from ssh or https github URLs.
func parseGithubURL(url string) (owner, repo string) {
	url = strings.TrimSuffix(url, ".git")
	var path string
	switch {
	case strings.HasPrefix(url, "git@github.com:"):
		path = strings.TrimPrefix(url, "git@github.com:")
	case strings.HasPrefix(url, "https://github.com/"):
		path = strings.TrimPrefix(url, "https://github.com/")
	case strings.HasPrefix(url, "ssh://git@github.com/"):
		path = strings.TrimPrefix(url, "ssh://git@github.com/")
	default:
		return "", ""
	}
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}
