package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	flag "github.com/spf13/pflag"
	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

// Info is the JSON shape of `sparkwing info`. Stable contract: agents
// parse this directly. Field renames here are breaking changes.
type Info struct {
	About        string         `json:"about"`
	Version      InfoVersion    `json:"version"`
	Binary       string         `json:"binary"`
	Project      InfoProject    `json:"project"`
	Toolchain    InfoToolchain  `json:"toolchain"`
	NextSteps    []InfoNextStep `json:"next_steps"`
	ForAgents    []InfoNextStep `json:"for_agents,omitempty"`
	Tips         []InfoTip      `json:"tips,omitempty"`
	Docs         InfoDocs       `json:"docs"`
	FirstRunNote string         `json:"first_run_note"`
}

type InfoTip struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Command string `json:"command,omitempty"`
	Note    string `json:"note,omitempty"`
}

// InfoVersion separates the raw version string from parsed semver and
// build provenance. Agents should branch on BuildType / IsRelease
// rather than string-matching Installed.
type InfoVersion struct {
	Installed  string `json:"installed"`
	Semver     string `json:"semver,omitempty"`
	IsRelease  bool   `json:"is_release"`
	IsDirty    bool   `json:"is_dirty"`
	BuildType  string `json:"build_type"`
	HumanLabel string `json:"human_label,omitempty"`
}

type InfoDocs struct {
	CLI      string `json:"cli"`
	Web      string `json:"web"`
	LLMsFull string `json:"llms_full"`
	LLMsTXT  string `json:"llms_txt"`
}

type InfoProject struct {
	Found         bool             `json:"found"`
	SparkwingDir  string           `json:"sparkwing_dir,omitempty"`
	Pipelines     InfoPipelinesSum `json:"pipelines,omitempty"`
	HowToScaffold string           `json:"how_to_scaffold,omitempty"`
}

type InfoPipelinesSum struct {
	Total     int      `json:"total"`
	Triggered int      `json:"triggered"`
	Manual    int      `json:"manual"`
	Groups    []string `json:"groups,omitempty"`
}

type InfoToolchain struct {
	Go InfoGoToolchain `json:"go"`
}

type InfoGoToolchain struct {
	Found    bool   `json:"found"`
	Version  string `json:"version,omitempty"`
	Required string `json:"required"`
}

type InfoNextStep struct {
	Command string `json:"command"`
	Purpose string `json:"purpose"`
}

func parseInfoVersion(raw string) InfoVersion {
	v := InfoVersion{Installed: raw}
	switch raw {
	case "(devel)":
		v.BuildType = "devel"
		v.HumanLabel = "local source build (no version pin)"
		return v
	case "(unknown)", "":
		v.BuildType = "unknown"
		v.HumanLabel = "version metadata missing"
		return v
	}
	dirty := strings.Contains(raw, "+dirty")
	clean := raw
	if idx := strings.IndexAny(clean, "+-"); idx >= 0 {
		clean = clean[:idx]
	}
	parts := strings.Split(strings.TrimPrefix(clean, "v"), ".")
	if len(parts) == 3 {
		v.Semver = clean
	}
	switch {
	case dirty:
		v.IsDirty = true
		v.BuildType = "local-dirty"
		v.HumanLabel = "local build with uncommitted changes"
	case v.Semver != "":
		v.IsRelease = true
		v.BuildType = "release"
	default:
		v.BuildType = "local-clean"
		v.HumanLabel = "local build (no semver tag)"
	}
	return v
}

const (
	infoAbout = "Self-hosted CI/CD platform. Pipelines are Go programs registered " +
		"in `.sparkwing/`, triggered by webhook / schedule / manual run, executed " +
		"on Kubernetes (or locally for dev). https://sparkwing.dev"

	infoFirstRunNote = "The first run of a pipeline (e.g. `sparkwing run release`) compiles " +
		".sparkwing/ from source and downloads Go module dependencies. That can take " +
		"30-90s the first time; subsequent runs hit the on-disk binary cache and start " +
		"instantly."

	infoGoRequirement = "Go-pipeline path requires the Go toolchain on PATH."
)

// infoBat indentation is load-bearing: the top-left "\" tail hangs off
// the speech bubble drawn above it.
const infoBat = `      /\                        /\
     / \'.__     /\_/\     __.'/ \
    (    '-.___( o   o )___.-'    )
     '-._  __  __'---'__  __  _.-'
         \/  \/         \/  \/`

func runInfo(args []string) error {
	fs := flag.NewFlagSet(cmdInfo.Path, flag.ContinueOnError)
	// Empty default lets resolveOutputFormat distinguish unset from explicit.
	output := fs.StringP("output", "o", "", "output format: table | json | plain (default: table)")
	asJSON := fs.Bool("json", false, "alias for --output json")
	forAgent := fs.Bool("for-agent", false, "emit a paste-ready block for CLAUDE.md / AGENTS.md (no ANSI, no extras)")
	firstTime := fs.Bool("first-time", false, "print the post-install onboarding card (used by install.sh; re-runnable any time)")
	if err := parseAndCheck(cmdInfo, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		PrintHelp(cmdInfo, os.Stderr)
		return fmt.Errorf("info: unexpected positional %q (info takes no positional args)", fs.Arg(0))
	}

	if *forAgent {
		printAgentBlock()
		return nil
	}

	if *firstTime {
		printFirstTimeCard()
		return nil
	}

	format, err := resolveOutputFormat(*output, *asJSON, cmdInfo.Path)
	if err != nil {
		return err
	}

	info := gatherInfo(format == "json")

	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	case "plain":
		for _, ns := range info.NextSteps {
			fmt.Println(ns.Command)
		}
		return nil
	default:
		printInfoTable(info)
		return nil
	}
}

func printAgentBlock() {
	fmt.Println("<!-- Sparkwing context for AI agents. Paste into CLAUDE.md or AGENTS.md and commit. Refresh after major sparkwing upgrades via `sparkwing info --for-agent`. -->")
	fmt.Println()
	fmt.Println("This repo uses **sparkwing** for CI/CD (https://sparkwing.dev). Pipelines are Go")
	fmt.Println("programs in `.sparkwing/`. Ask the binary, don't scrape the repo:")
	fmt.Println()
	fmt.Println("- `sparkwing info --json` -- context: binary, project, next steps (start here)")
	fmt.Println("- `sparkwing commands` -- full CLI surface as JSON (every verb + every flag)")
	fmt.Println("- `sparkwing pipeline list --json` -- this repo's pipelines")
	fmt.Println("- `sparkwing run <name>` -- run a pipeline (`wing <name>` is a human alias; agents prefer `sparkwing run`)")
	fmt.Println("- `sparkwing docs read --topic <slug>` -- offline docs; full corpus: https://sparkwing.dev/llms-full.txt")
}

func printFirstTimeCard() {
	tip := func(cmd, pad, note string) string {
		return color.Cyan(cmd) + pad + color.Dim("# "+note)
	}

	fmt.Println(color.Bold("Welcome to sparkwing!"))
	fmt.Println()

	goMissing := !goOnPath()
	sparkwingMissing := !sparkwingOnPath()
	if goMissing || sparkwingMissing {
		fmt.Println(color.Bold("PREREQUISITES"))
		if sparkwingMissing {
			fmt.Println("  - " + color.Yellow("sparkwing is not on PATH.") + " " +
				color.Dim("typing `sparkwing` or `wing` in a new shell will fail."))
			fmt.Println("    " + color.Dim("Add ~/.local/bin to PATH and reload:"))
			for _, line := range pathHintLines() {
				fmt.Println("    " + color.Cyan(line))
			}
		}
		if goMissing {
			fmt.Println("  - " + color.Yellow("Go is not on PATH.") + " " +
				color.Dim("`pipeline new` runs `go mod tidy`, and `sparkwing run`"))
			fmt.Println("    " + color.Dim("compiles `.sparkwing/` via `go build`. Both fail without it."))
			fmt.Println("    " + goInstallHintForce())
		}
		fmt.Println()
	}

	fmt.Println(color.Bold("NEXT STEPS"))
	fmt.Println("  1. cd into a code repo")
	fmt.Println("  2. " + tip("sparkwing pipeline new --name release", "      ", "bootstrap .sparkwing/ + a minimal pipeline"))
	fmt.Println("  3. " + tip("sparkwing run release", "                      ", "run it - first time downloads dependencies"))
	fmt.Println("  4. " + tip("sparkwing docs read --topic sdk", "            ", "or https://sparkwing.dev/sdk"))
	fmt.Println()
	fmt.Println("  For a build/test/deploy DAG instead:")
	fmt.Println("    " + color.Cyan("sparkwing pipeline new --name release --template build-test-deploy"))
	fmt.Println()
	fmt.Println(color.Bold("TIPS"))
	tips := []InfoNextStep{
		{Command: "wing <pipeline>", Purpose: "human shortcut for 'sparkwing run <pipeline>'"},
		{Command: "sparkwing dashboard start", Purpose: "run the dashboard locally to watch runs in a browser"},
		{Command: "sparkwing info", Purpose: "surveys the current repo + suggests next commands"},
	}
	cmpCmd, cmpNote := firstTimeCompletionHint()
	tips = append(tips, InfoNextStep{Command: cmpCmd, Purpose: cmpNote})
	printAlignedSteps(tips)
	fmt.Println()

	fmt.Println(color.Bold("DOCS"))
	fmt.Printf("  cli:        %s %s\n", color.Cyan("sparkwing docs read --topic <slug>"), color.Dim("(offline, version-locked)"))
	fmt.Printf("  web:        %s\n", color.Cyan("https://sparkwing.dev/docs/"))
	fmt.Printf("  llms-full:  %s %s\n", color.Cyan("https://sparkwing.dev/llms-full.txt"), color.Dim("(full corpus, one fetch)"))
	fmt.Printf("  llms.txt:   %s %s\n", color.Cyan("https://sparkwing.dev/llms.txt"), color.Dim("(short index)"))
	fmt.Println()

	fmt.Println(color.Bold("SEE ALSO"))
	fmt.Println("  Different tools for different jobs: sparkwing runs Go pipelines (DAGs,")
	fmt.Println("  retries, run records); for one-off bash chores in a repo (formatters,")
	fmt.Println("  port-forwards) reach for " + color.Cyan("dowing") + " instead - it runs *.sh files from")
	fmt.Println("  bin/ or scripts/ with no compile cycle. https://github.com/koreyGambill/dowing")
}

func userShellBase() string {
	base := os.Getenv("SHELL")
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	return base
}

func firstTimeCompletionHint() (cmd, note string) {
	switch userShellBase() {
	case "bash":
		return "echo 'source <(sparkwing completion --shell bash)' >> ~/.bashrc", "enable cli tab auto completion"
	case "zsh":
		return "echo 'source <(sparkwing completion --shell zsh)' >> ~/.zshrc", "enable cli tab auto completion"
	case "fish":
		return "sparkwing completion --shell fish > ~/.config/fish/completions/sparkwing.fish", "enable cli tab auto completion"
	default:
		return "sparkwing completion --help", "enable cli tab auto completion (bash | zsh | fish)"
	}
}

func pathHintLines() []string {
	switch userShellBase() {
	case "bash":
		return []string{
			`echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc`,
			`source ~/.bashrc`,
		}
	case "zsh":
		return []string{
			`echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc`,
			`source ~/.zshrc`,
		}
	case "fish":
		return []string{
			`fish_add_path ~/.local/bin`,
		}
	default:
		return []string{
			`# unknown shell ($SHELL=` + os.Getenv("SHELL") + `); adapt for your rc file:`,
			`echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc`,
			`source ~/.bashrc`,
		}
	}
}

// gatherInfo never errors: every field has a sensible "not found"
// fallback. agentMode tilts NextSteps toward discovery surfaces.
func gatherInfo(agentMode bool) Info {
	binary, _ := os.Executable()
	info := Info{
		About:   infoAbout,
		Version: parseInfoVersion(installedVersion()),
		Binary:  binary,
		Docs: InfoDocs{
			CLI:      "sparkwing docs list / read --topic <slug> / all",
			Web:      "https://sparkwing.dev/docs/",
			LLMsFull: "https://sparkwing.dev/llms-full.txt",
			LLMsTXT:  "https://sparkwing.dev/llms.txt",
		},
		ForAgents:    infoForAgents,
		FirstRunNote: infoFirstRunNote,
		Toolchain: InfoToolchain{
			Go: InfoGoToolchain{Required: infoGoRequirement},
		},
	}

	if goVer := goToolchainVersion(); goVer != "" {
		info.Toolchain.Go.Found = true
		info.Toolchain.Go.Version = goVer
	}

	cwd, err := os.Getwd()
	if err == nil {
		if sparkwingDir, ok := walkUpForSparkwing(cwd); ok {
			info.Project.Found = true
			info.Project.SparkwingDir = sparkwingDir
			if pipelineList, perr := gatherPipelinesCatalog(false); perr == nil {
				info.Project.Pipelines = summarizePipelines(pipelineList)
			}
		}
	}
	if !info.Project.Found {
		info.Project.HowToScaffold = "sparkwing pipeline new --name release"
	}

	info.NextSteps = nextStepsFor(info, agentMode)
	info.Tips = gatherTips(info)
	return info
}

// gatherTips runs each tip gate. Each gate must be cheap and fail-soft;
// network probes use a short timeout and silently skip on failure.
func gatherTips(info Info) []InfoTip {
	var tips []InfoTip

	if t, ok := tipTabComplete(); ok {
		tips = append(tips, t)
	}
	if t, ok := tipDashboardNotRunning(); ok {
		tips = append(tips, t)
	}
	if t, ok := tipAgentBlockMissing(info); ok {
		tips = append(tips, t)
	}
	if t, ok := tipCLIBehindLatest(info); ok {
		tips = append(tips, t)
	}

	return tips
}

func tipTabComplete() (InfoTip, bool) {
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	base := shell
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	home, _ := os.UserHomeDir()

	switch base {
	case "bash":
		if completionConfigured(bashInitCandidates(home)) {
			return InfoTip{}, false
		}
		return InfoTip{
			ID:      "tab-complete",
			Title:   "Tab-complete is not set up",
			Command: "echo 'source <(sparkwing completion --shell bash)' >> ~/.bashrc",
		}, true
	case "zsh":
		if completionConfigured(zshInitCandidates(home)) {
			return InfoTip{}, false
		}
		return InfoTip{
			ID:      "tab-complete",
			Title:   "Tab-complete is not set up",
			Command: "echo 'source <(sparkwing completion --shell zsh)' >> ~/.zshrc",
		}, true
	case "fish":
		rc := home + "/.config/fish/completions/sparkwing.fish"
		if _, err := os.Stat(rc); err == nil {
			return InfoTip{}, false
		}
		return InfoTip{
			ID:      "tab-complete",
			Title:   "Tab-complete is not set up",
			Command: "sparkwing completion --shell fish > ~/.config/fish/completions/sparkwing.fish",
		}, true
	}
	return InfoTip{}, false
}

func bashInitCandidates(home string) []string {
	return []string{
		home + "/.bashrc",
		home + "/.bash_profile",
		home + "/.profile",
		home + "/.bashrc.local",
	}
}

func zshInitCandidates(home string) []string {
	return []string{
		home + "/.zshrc",
		home + "/.zprofile",
		home + "/.zshenv",
		home + "/.zlogin",
		home + "/.zshrc.local",
		home + "/.zshrc_profile",
	}
}

func completionConfigured(candidates []string) bool {
	for _, path := range candidates {
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if strings.Contains(string(body), "sparkwing completion") {
			return true
		}
	}
	return false
}

func tipDashboardNotRunning() (InfoTip, bool) {
	dp, err := resolveDashboardPaths("")
	if err != nil {
		return InfoTip{}, false
	}
	if _, alive := readLivePID(dp.pid); alive {
		return InfoTip{}, false
	}
	return InfoTip{
		ID:      "dashboard",
		Title:   "Local dashboard is not running",
		Command: "sparkwing dashboard start",
		Note:    "runs at http://127.0.0.1:4343",
	}, true
}

func tipAgentBlockMissing(info Info) (InfoTip, bool) {
	if !info.Project.Found {
		return InfoTip{}, false
	}
	root := info.Project.SparkwingDir
	// SparkwingDir points at .sparkwing/; agent files live one up.
	if i := strings.LastIndex(root, "/.sparkwing"); i >= 0 {
		root = root[:i]
	}
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		if _, err := os.Stat(root + "/" + name); err == nil {
			return InfoTip{}, false
		}
	}
	return InfoTip{
		ID:      "agent-block",
		Title:   "No CLAUDE.md / AGENTS.md in this repo",
		Command: "sparkwing info --for-agent",
		Note:    "paste the output into CLAUDE.md so AI tools have sparkwing context",
	}, true
}

func tipCLIBehindLatest(info Info) (InfoTip, bool) {
	if !info.Version.IsRelease || info.Version.Semver == "" {
		return InfoTip{}, false
	}
	latest, err := fetchLatestRelease()
	if err != nil || latest == "" {
		return InfoTip{}, false
	}
	if !isSemver(info.Version.Semver) || !isSemver(latest) {
		return InfoTip{}, false
	}
	if !semverBehind(info.Version.Semver, latest) {
		return InfoTip{}, false
	}
	return InfoTip{
		ID:      "cli-behind",
		Title:   "A newer sparkwing release is available",
		Command: "sparkwing version update --cli",
		Note:    "installed " + info.Version.Semver + " → latest " + latest,
	}, true
}

func semverBehind(current, latest string) bool {
	return semver.Compare(current, latest) < 0
}

// goToolchainVersion shells out to `go version`. The user-facing answer
// is "what compiler runs when sparkwing compiles .sparkwing/?" — that's
// the version on PATH, not the one that built the CLI.
func goToolchainVersion() string {
	bin, err := exec.LookPath("go")
	if err != nil {
		return ""
	}
	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	if len(fields) >= 3 {
		return fields[2]
	}
	return strings.TrimSpace(string(out))
}

func summarizePipelines(list []Pipeline) InfoPipelinesSum {
	out := InfoPipelinesSum{Total: len(list)}
	groupSet := map[string]struct{}{}
	for _, p := range list {
		if len(p.Triggers) > 0 {
			out.Triggered++
		} else {
			out.Manual++
		}
		if p.Group != "" {
			groupSet[p.Group] = struct{}{}
		}
	}
	for g := range groupSet {
		out.Groups = append(out.Groups, g)
	}
	sort.Strings(out.Groups)
	return out
}

func nextStepsFor(info Info, agentMode bool) []InfoNextStep {
	if !info.Project.Found {
		return []InfoNextStep{
			{Command: "sparkwing info --first-time", Purpose: "post-install onboarding card: full numbered scaffold steps + tips"},
			{Command: "sparkwing pipeline new --name release", Purpose: "auto-bootstrap .sparkwing/ + scaffold a single-node pipeline"},
			{Command: "sparkwing run release", Purpose: "run the scaffolded pipeline"},
		}
	}
	_ = agentMode
	return []InfoNextStep{
		{Command: "sparkwing pipeline list", Purpose: "see every pipeline this repo defines"},
		{Command: "sparkwing pipeline describe --name <name>", Purpose: "full metadata for one pipeline"},
		{Command: "sparkwing run <name>", Purpose: "run a pipeline (humans: `wing <name>` is the same thing)"},
		{Command: "sparkwing pipeline new --name <name>", Purpose: "scaffold a new pipeline"},
		{Command: "sparkwing docs list", Purpose: "browse embedded docs (offline)"},
		{Command: "sparkwing dashboard start", Purpose: "start the local dashboard at http://127.0.0.1:4343"},
	}
}

var infoForAgents = []InfoNextStep{
	{Command: "sparkwing commands", Purpose: "full CLI surface as JSON (every verb + every flag)"},
	{Command: "sparkwing info --json", Purpose: "machine-readable copy of this card (alias: -o json)"},
	{Command: "sparkwing info --for-agent", Purpose: "paste-ready block for CLAUDE.md / AGENTS.md"},
	{Command: "sparkwing pipeline list --json", Purpose: "this repo's pipelines as JSON"},
	{Command: "sparkwing <verb> --help --json", Purpose: "any verb's spec as JSON"},
}

func printInfoTable(info Info) {
	v := info.Version
	const lblW = 9
	row := func(label, value, dim string) {
		if dim != "" {
			fmt.Printf("  %-*s  %s %s\n", lblW, label, value, color.Dim(dim))
		} else {
			fmt.Printf("  %-*s  %s\n", lblW, label, value)
		}
	}

	if info.About != "" {
		if color.Enabled() {
			fmt.Println(batsay(info.About, 44))
			fmt.Println()
		} else {
			fmt.Println(color.Bold("ABOUT"))
			fmt.Println("  " + info.About)
			fmt.Println()
		}
	}

	fmt.Println(color.Bold("ENVIRONMENT"))

	buildLabel := v.BuildType
	if v.HumanLabel != "" {
		buildLabel = v.BuildType + " — " + v.HumanLabel
	}
	row("sparkwing", v.Installed, "("+buildLabel+")")

	if info.Binary != "" {
		row("binary", info.Binary, "")
	}

	if info.Toolchain.Go.Found {
		row("go", info.Toolchain.Go.Version, "(your local toolchain — used to compile .sparkwing/)")
	} else {
		row("go", color.Dim("not installed"), "(your local toolchain — needed to compile .sparkwing/)")
		fmt.Printf("  %-*s  %s\n", lblW, "", color.Cyan(goInstallHintForce()))
	}

	if info.Project.Found {
		p := info.Project.Pipelines
		noun := "pipelines"
		if p.Total == 1 {
			noun = "pipeline"
		}
		row("project", ".sparkwing/ at "+info.Project.SparkwingDir, fmt.Sprintf("(%d %s: %d triggered, %d manual)", p.Total, noun, p.Triggered, p.Manual))
		if len(p.Groups) > 0 {
			row("groups", strings.Join(p.Groups, ", "), "")
		}
	} else {
		row("project", color.Dim("no .sparkwing/ in this directory or any parent"), "")
	}
	fmt.Println()

	fmt.Println(color.Bold("NEXT STEPS"))
	printAlignedSteps(info.NextSteps)
	fmt.Println()

	fmt.Println(color.Bold("SEE ALSO"))
	fmt.Printf("  %s %s\n", color.Cyan("dowing"), color.Dim("- run *.sh tasks from bin/ or scripts/ for repo-local chores (formatters, port-forwards). https://github.com/koreyGambill/dowing"))
	fmt.Println()

	if len(info.ForAgents) > 0 {
		fmt.Println(color.Bold("FOR AGENTS"))
		printAlignedSteps(info.ForAgents)
		fmt.Println()
	}

	if len(info.Tips) > 0 {
		fmt.Println(color.Bold("TIPS"))
		for _, t := range info.Tips {
			fmt.Printf("  %s %s\n", color.Yellow("•"), t.Title)
			if t.Command != "" {
				fmt.Printf("      %s\n", color.Cyan(t.Command))
			}
			if t.Note != "" {
				fmt.Printf("      %s\n", color.Dim(t.Note))
			}
		}
		fmt.Println()
	}

	fmt.Println(color.Bold("DOCS"))
	fmt.Printf("  cli:        %s %s\n", color.Cyan(info.Docs.CLI), color.Dim("(offline, version-locked)"))
	fmt.Printf("  web:        %s\n", color.Cyan(info.Docs.Web))
	fmt.Printf("  llms-full:  %s %s\n", color.Cyan(info.Docs.LLMsFull), color.Dim("(full corpus, one fetch)"))
	fmt.Printf("  llms.txt:   %s %s\n", color.Cyan(info.Docs.LLMsTXT), color.Dim("(short index)"))
}

func batsay(msg string, width int) string {
	lines := wrapLinesAt(msg, width)
	if len(lines) == 0 {
		lines = []string{""}
	}
	var b strings.Builder
	b.WriteString(" " + strings.Repeat("_", width+2) + "\n")
	for i, line := range lines {
		left, right := "|", "|"
		switch {
		case len(lines) == 1:
			left, right = "<", ">"
		case i == 0:
			left, right = "/", "\\"
		case i == len(lines)-1:
			left, right = "\\", "/"
		}
		fmt.Fprintf(&b, "%s %-*s %s\n", left, width, line, right)
	}
	b.WriteString(" " + strings.Repeat("-", width+2) + "\n")
	b.WriteString("    \\\n")
	b.WriteString("     \\\n")
	b.WriteString(infoBat)
	return b.String()
}

func wrapLinesAt(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var out []string
	line := words[0]
	for _, w := range words[1:] {
		if len(line)+1+len(w) > width {
			out = append(out, line)
			line = w
			continue
		}
		line += " " + w
	}
	out = append(out, line)
	return out
}

func printAlignedSteps(steps []InfoNextStep) {
	width := 0
	for _, ns := range steps {
		if n := len(ns.Command); n > width {
			width = n
		}
	}
	for _, ns := range steps {
		pad := strings.Repeat(" ", width-len(ns.Command))
		fmt.Printf("  %s%s  %s\n", color.Cyan(ns.Command), pad, color.Dim(ns.Purpose))
	}
}
