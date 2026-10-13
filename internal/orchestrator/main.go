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

	"golang.org/x/term"

	"github.com/sparkwing-dev/sparkwing/internal/protofd"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func Main() {
	// safety: ahead of the other verbs' startup, which claims an inherited
	// descriptor and SPARKWING_* variables that the protocol leaves to the host.
	if len(os.Args) == 2 && os.Args[1] == protofd.Arg {
		if _, err := bindProjectPipelines(); err != nil {
			fmt.Fprintln(os.Stderr, "sw-node-protocol:", err)
			os.Exit(1)
		}
		os.Exit(sdkserve.Main(sdkserve.Config{SDKVersion: sparkwingModuleVersion(), PlanDoc: planDocument}))
	}
	secrets.ShareRegisteredFromEnv()
	if err := takeAgentToken(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	projectCfg, projectErr := bindProjectPipelines()

	if len(os.Args) > 1 && os.Args[1] == "--describe" {
		described, err := sparkwingruntime.DescribeAll()
		if err != nil {
			fmt.Fprintln(os.Stderr, "describe:", err)
			os.Exit(1)
		}
		enc := json.NewEncoder(os.Stdout)
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
	// safety: handle-trigger and run-node record a config error against the
	// run they serve; every other verb has no run to record it on, so it stops
	// here rather than planning without the project's defaults and guards.
	if projectErr != nil {
		fmt.Fprintln(os.Stderr, "sparkwing:", projectErr)
		os.Exit(1)
	}
	// safety: only the exact form is the verb, so a pipeline named plan still
	// runs under its own flags.
	if len(os.Args) == 3 && os.Args[1] == "plan" && os.Args[2] == "--json" {
		if err := runPlanCLI(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "plan:", err)
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
	if len(os.Args) > 1 && os.Args[1] == "ops" {
		if err := runOpsCLI(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "ops:", err)
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

	if len(rest) > 0 && rest[0] == "config" {
		if err := runPipelineConfigInspect(pipeline, rest[1:]); err != nil {
			fmt.Fprintln(os.Stderr, pipeline+":", err)
			os.Exit(1)
		}
		return
	}

	for _, tok := range rest {
		if tok == "-h" || tok == "--help" {
			if err := printPipelineHelp(pipeline); err != nil {
				fmt.Fprintln(os.Stderr, pipeline+":", err)
				os.Exit(1)
			}
			return
		}
	}

	for _, tok := range rest {
		if tok == "--explain" {
			if err := printPipelinePlan(pipeline, filterTok(rest, "--explain")); err != nil {
				fmt.Fprintln(os.Stderr, pipeline+":", err)
				os.Exit(1)
			}
			return
		}
	}

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

	pipelineYAML := loadPipelineYAML(pipeline)

	dispatchWait, err := dispatchWaitSetting()
	if err != nil {
		fmt.Fprintln(os.Stderr, pipeline+":", err)
		os.Exit(1)
	}

	delegate := selectRunRenderer()
	opts := Options{
		DispatchWaitTimeout: dispatchWait,
		Pipeline:            pipeline,
		RunHandlePath:       os.Getenv("SPARKWING_RUN_HANDLE_FILE"),
		Args:                argsMap,
		Git:                 detectGit(),
		Delegate:            delegate,
		Debug:               readDebugDirectivesFromEnv(),
		StartAt:             os.Getenv("SPARKWING_START_AT"),
		StopAt:              os.Getenv("SPARKWING_STOP_AT"),
		Only:                os.Getenv("SPARKWING_ONLY"),
		NoCache:             noCacheFromEnv(),
		Priority:            os.Getenv(PriorityEnv),
		AdmissionClass:      admissionClassFromEnv(),
		DryRun:              dryRunFromEnv(),
		MaxParallel:         runtime.NumCPU(),
		PipelineYAML:        pipelineYAML,
		// safety: this binary serves `run-node` a few lines above, so it can
		// re-enter itself for each node.
		ProcessPerNode: true,
		Verbose:        debugLogLevel(),
	}
	opts.Admission = pipelineAdmission(childAttachTokenFromProcessEnv(), wingwire.OriginLocal)
	if projectCfg != nil {
		opts.DefaultArgs = projectCfg.Defaults.Args
		if pipelineYAML != nil {
			if pipelineYAML.Guards.IsEmpty() {
				pipelineYAML.Guards = projectCfg.Defaults.Guards
			}
			if len(pipelineYAML.Requires) == 0 {
				pipelineYAML.Requires = projectCfg.Defaults.Requires
			}
		}
	}
	if pinErr := RefusePinnedLocalRun(pipelineYAML); pinErr != nil {
		fmt.Fprintln(os.Stderr, "sparkwing run:", pinErr)
		os.Exit(1)
	}
	if profileErr := refuseProfileHandoff(); profileErr != nil {
		fmt.Fprintln(os.Stderr, "sparkwing run:", profileErr)
		os.Exit(1)
	}
	if applyErr := applyCIEmbeddedEnv(&opts); applyErr != nil {
		fmt.Fprintln(os.Stderr, "sparkwing run:", applyErr)
		os.Exit(1)
	}

	res, err := RunLocal(context.Background(), paths, opts)
	if renderer, ok := delegate.(*compactRunRenderer); ok {
		renderer.finishResult(res, err)
		if renderer.Err() != nil {
			fmt.Fprintln(os.Stderr, "run output:", renderer.Err())
			os.Exit(1)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "run:", runDisplayError(delegate, err))
		os.Exit(1)
	}
	if res != nil && res.Error != nil {
		fmt.Fprintln(os.Stderr, "run:", runDisplayError(delegate, res.Error))
	}
	if res != nil && res.Status != "success" {
		os.Exit(1)
	}
}

func bindProjectPipelines() (*projectconfig.Config, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("find the project config: %w", err)
	}
	_, cfg, err := projectconfig.Discover(cwd)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, nil
	}
	sparkwing.BindPipelinesFromYAML(&pipelines.Config{Pipelines: cfg.Pipelines})
	return cfg, nil
}

func selectLocalRenderer() sparkwing.Logger {
	return localRenderer(os.Getenv("SPARKWING_LOG_FORMAT"))
}

func localRenderer(format string) sparkwing.Logger {
	switch strings.ToLower(format) {
	case "json":
		return NewJSONRenderer()
	case "pretty":
		return NewPrettyRenderer()
	case "quiet":
		return NewQuietRenderer()
	}
	if isInteractiveStdout() {
		return NewPrettyRenderer()
	}
	return NewJSONRenderer()
}

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

func printPipelineHelp(pipeline string) error {
	schema, ok, err := sparkwingruntime.DescribePipelineByName(pipeline)
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
	fmt.Fprintf(w, "  sparkwing run %s", schema.Name)
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
			if a.JobID != "" {
				suffix += fmt.Sprintf(" [from job %s]", a.JobID)
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
	printSparkwingFlagsSection(w)
	return nil
}

func printSparkwingFlagsSection(w io.Writer) {
	docs := sparkwing.SparkwingFlagDocs()
	if len(docs) == 0 {
		return
	}
	fmt.Fprintln(w, "SPARKWING FLAGS")
	for _, d := range docs {
		head := "--" + d.Name
		if d.Short != "" {
			head = "-" + d.Short + ", " + head
		}
		if d.Argument != "" {
			head += " " + d.Argument
		}
		fmt.Fprintf(w, "  %-30s %s\n", head, d.Desc)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "See `sparkwing run --help` for prose explanations of each sparkwing flag.")
}

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
		NoCache:  noCacheFromEnv(),
		DryRun:   dryRunFromEnv(),
	}
	plan, err := reg.Invoke(context.Background(), argsMap, rc)
	if err != nil {
		return fmt.Errorf("build plan: %w", err)
	}
	snap, err := marshalPlanSnapshot(plan, rc, planSnapshotMeta{})
	if err != nil {
		return fmt.Errorf("marshal plan: %w", err)
	}
	_, _ = os.Stdout.Write(snap)
	fmt.Println()
	return nil
}

func filterTok(args []string, drop string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a != drop {
			out = append(out, a)
		}
	}
	return out
}

func stripExplainOutputFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		tok := args[i]
		switch {
		case tok == "-o", tok == "--output":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		case strings.HasPrefix(tok, "-o="),
			strings.HasPrefix(tok, "--output="):
		default:
			out = append(out, tok)
		}
	}
	return out
}

func parseTypedFlags(pipeline string, args []string) (map[string]string, error) {
	schema, ok, err := sparkwingruntime.DescribePipelineByName(pipeline)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, unknownPipelineErr(pipeline)
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
			return nil, fmt.Errorf("unknown flag --%s (run `sparkwing run %s --help` for valid flags)", name, pipeline)
		}
		if arg.Type == "bool" {
			if !hasEq {
				value = "true"
			}
			out[arg.Name] = value
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
		if err := arg.CheckEnum(value); err != nil {
			return nil, err
		}
		out[arg.Name] = value
	}
	return out, nil
}

// DebugDirectivesFromEnv reads the pauses `sparkwing debug run` asks for.
func DebugDirectivesFromEnv() DebugDirectives { return readDebugDirectivesFromEnv() }

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

func loadPipelineYAML(pipeline string) *pipelines.Pipeline {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	_, cfg, err := projectconfig.DiscoverPipelines(cwd)
	if err != nil || cfg == nil {
		return nil
	}
	return cfg.Find(pipeline)
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

func detectGit() *sparkwing.Git {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	return DetectGit(cwd)
}

// DetectGit reads the checkout at dir: HEAD, branch, origin's default branch
// and its GitHub repository.
func DetectGit(dir string) *sparkwing.Git {
	g := &sparkwing.Git{}
	if dir != "" {
		g = sparkwing.NewGit(dir, "", "", "", "", "")
	}
	git := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		return cmd.Output()
	}
	if out, err := git("rev-parse", "HEAD"); err == nil {
		g.SHA = strings.TrimSpace(string(out))
	}
	if out, err := git("rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		branch := strings.TrimSpace(string(out))
		if branch != "HEAD" {
			g.Branch = branch
		}
	}
	if out, err := git("symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		name := strings.TrimSpace(string(out))
		g.DefaultBranch = strings.TrimPrefix(name, "origin/")
	}
	if out, err := git("remote", "get-url", "origin"); err == nil {
		g.RepoURL = strings.TrimSpace(string(out))
		owner, repo := parseGithubURL(g.RepoURL)
		if owner != "" && repo != "" {
			g.Repo = owner + "/" + repo
		}
	}
	return g
}

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
