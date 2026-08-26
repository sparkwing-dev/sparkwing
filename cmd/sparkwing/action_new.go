// `sparkwing pipeline new` scaffolder. Five shapes, structural only:
// minimal (default), build-test-deploy, ci-pr-check, release,
// scheduled-report. Goal: get to a compiling, runnable stub fast.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"

	templates "github.com/sparkwing-dev/sparks-core/templates"
)

func runPipelineNew(args []string) error {
	fs := flag.NewFlagSet(cmdPipelineNew.Path, flag.ContinueOnError)
	pipelineName := fs.String("name", "", "new pipeline name (kebab-case, e.g. deploy-staging)")
	template := fs.String("template", "minimal", "shape to scaffold: minimal | build-test-deploy | ci-pr-check | release | scheduled-report")
	hidden := fs.Bool("hidden", false, "mark the entry hidden in tab-complete menus")
	short := fs.String("short", "", "short one-line description (ShortHelp / frontmatter desc)")
	on := fs.StringArray("on", nil, "trigger to declare: "+strings.Join(triggerEventNames, " | ")+" (repeatable or comma-separated; default: the shape's own)")
	changeDir := fs.StringP("sw-cd", "C", "", "scaffold as if started in this directory (re-anchors the .sparkwing search)")
	if err := parseAndCheck(cmdPipelineNew, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		PrintHelp(cmdPipelineNew, os.Stderr)
		return fmt.Errorf("new: unexpected positional %q (use --name)", fs.Arg(0))
	}
	if *pipelineName == "" {
		PrintHelp(cmdPipelineNew, os.Stderr)
		return errors.New("new: --name is required (e.g. --name deploy-staging)")
	}
	name := *pipelineName
	if err := validatePipelineName(name); err != nil {
		return err
	}

	if *changeDir != "" {
		if err := os.Chdir(*changeDir); err != nil {
			return fmt.Errorf("new: --sw-cd %q: %w", *changeDir, err)
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	sparkwingDir, ok := walkUpForSparkwing(cwd)
	bootstrapped := !ok
	if !ok {
		if err := bootstrapDotSparkwingOpts(cwd, filepath.Join(cwd, ".sparkwing"), true); err != nil {
			return err
		}
		sparkwingDir = filepath.Join(cwd, ".sparkwing")
	}

	if _, cfg, derr := projectconfig.DiscoverPipelines(cwd); derr == nil && cfg != nil {
		for _, p := range cfg.Pipelines {
			if p.Name == name {
				return fmt.Errorf("pipeline %q already exists in sparkwing.yaml (entrypoint %q)", name, p.Entrypoint)
			}
		}
	}

	if hint := goInstallHint(); hint != "" {
		fmt.Fprintln(os.Stderr, "warning: Go is not on PATH. Scaffolding will succeed but `sparkwing run "+name+"` will fail until Go is installed.")
		fmt.Fprintln(os.Stderr, "  "+hint)
		fmt.Fprintln(os.Stderr)
	}

	shape, ok := builtinShapeByName(*template)
	if !ok {
		return fmt.Errorf("new: unknown shape %q -- pipeline new takes a shape, one of: %s\n"+
			"  a worked pipeline to read instead: sparkwing examples --name %s --body",
			*template, strings.Join(builtinShapeNames(), ", "), *template)
	}
	trigger, err := resolveTrigger(shape, parseOnFlag(*on), fs.Changed("on"))
	if err != nil {
		return err
	}
	if err := scaffoldGoFromTemplate(sparkwingDir, name, *hidden, *short, shape.src, bootstrapped, trigger); err != nil {
		return err
	}
	if !fs.Changed("template") {
		printExamplesHint()
	}
	return nil
}

// printExamplesHint tells an author who just scaffolded a bare shape
// that worked pipelines exist to read.
//
// It prints here because this is the only moment it is certainly
// relevant: whoever just ran `pipeline new` is writing a pipeline right
// now, and everyone else pays nothing. It points at search rather than
// a listing -- browsing forty examples to pick one is the cost this
// reorganization removed.
func printExamplesHint() {
	list, err := templates.List()
	if err != nil || len(list) == 0 {
		return
	}
	fmt.Println()
	fmt.Printf("%s scaffolded the %s shape -- the bodies are placeholders to replace.\n",
		color.Dim("note:"), color.Bold("minimal"))
	fmt.Printf("      %d worked pipelines exist to read for how a real one is built:\n", len(list))
	printAlignedSteps([]InfoNextStep{
		{Command: "sparkwing docs search -q <what you are doing>", Purpose: "usually the fastest way in"},
		{Command: "sparkwing examples --name <name> --body", Purpose: "read one in full"},
	})
}

// scaffoldFromRegistry renders a sparks-core registry template (anything
// `sparkwing examples` lists) into jobs/<name>.go and wires the
// sparkwing.yaml entry. The pipeline's registered name is the --name
// flag: when the template declares a `pipeline-name` param it's set from
// --name, so the rendered Register() call and the yaml entry agree.
func scaffoldFromRegistry(sparkwingDir, name, templateName string, params []string, hidden, bootstrapped bool) error {
	tmpl, err := templates.Get(templateName)
	if err != nil {
		return fmt.Errorf("new: unknown template %q -- run `sparkwing examples` to list them", templateName)
	}
	pm, err := parseTemplateParams(params)
	if err != nil {
		return err
	}
	if manifestDeclaresParam(tmpl.Manifest, "pipeline-name") {
		pm["pipeline-name"] = name
	}
	rendered, err := templates.Render(templateName, pm)
	if err != nil {
		return fmt.Errorf("new: %w", err)
	}

	file := filepath.Join(sparkwingDir, "jobs", goJobFilename(name))
	if _, err := os.Stat(file); err == nil {
		return fmt.Errorf("refusing to overwrite %s\n  pick a different --name, or delete the file first if you want to regenerate", file)
	}
	if err := os.WriteFile(file, []byte(rendered), 0o644); err != nil {
		return err
	}
	if err := appendPipelinesYAML(sparkwingDir, name, kebabToPascal(name), hidden, ""); err != nil {
		return err
	}
	if err := finishScaffold(sparkwingDir, file, name, bootstrapped, ""); err != nil {
		return err
	}
	if pre := strings.TrimSpace(tmpl.Manifest.Prerequisite); pre != "" {
		fmt.Printf("\n%s %s\n", color.Bold("prerequisite:"), pre)
	}
	return nil
}

// parseTemplateParams turns repeated --param k=v flags into a map.
//
// Only `examples scaffold` reaches this now -- `pipeline new` takes a
// shape and renders no parameters -- so the error names that verb.
func parseTemplateParams(params []string) (map[string]string, error) {
	out := make(map[string]string, len(params))
	for _, p := range params {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("examples scaffold: --param %q must be k=v", p)
		}
		out[k] = v
	}
	return out, nil
}

func manifestDeclaresParam(m templates.Manifest, name string) bool {
	for _, p := range m.Parameters {
		if p.Name == name {
			return true
		}
	}
	return false
}

// validatePipelineName enforces kebab-case so the name round-trips
// through yaml + shell + Go-identifier conversion.
func validatePipelineName(name string) error {
	if name == "" {
		return errors.New("name: must not be empty")
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		return errors.New("name: must not start or end with '-'")
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return errors.New("name: must start with a letter")
			}
		case r == '-':
			if i > 0 && name[i-1] == '-' {
				return errors.New("name: must not contain '--'")
			}
		default:
			return fmt.Errorf("name: invalid character %q (kebab-case only: a-z, 0-9, -)", r)
		}
	}
	return nil
}

func kebabToPascal(name string) string {
	var b strings.Builder
	capitalize := true
	for _, r := range name {
		if r == '-' {
			capitalize = true
			continue
		}
		if capitalize {
			b.WriteRune(unicode.ToUpper(r))
			capitalize = false
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func kebabToSnake(name string) string {
	return strings.ReplaceAll(name, "-", "_")
}

// goReservedTrailingTokens are tokens (GOOS / GOARCH / "test") Go
// treats specially as the trailing _-segment of a .go filename. A
// scaffold landing on these silently gets build-tagged out.
var goReservedTrailingTokens = map[string]bool{
	"test": true,
	"aix":  true, "android": true, "darwin": true, "dragonfly": true,
	"freebsd": true, "hurd": true, "illumos": true, "ios": true,
	"js": true, "linux": true, "nacl": true, "netbsd": true,
	"openbsd": true, "plan9": true, "solaris": true, "wasip1": true,
	"windows": true, "zos": true,
	"386": true, "amd64": true, "amd64p32": true, "arm": true,
	"arm64": true, "arm64be": true, "armbe": true, "loong64": true,
	"mips": true, "mips64": true, "mips64le": true, "mips64p32": true,
	"mips64p32le": true, "mipsle": true, "ppc": true, "ppc64": true,
	"ppc64le": true, "riscv": true, "riscv64": true, "s390": true,
	"s390x": true, "sparc": true, "sparc64": true, "wasm": true,
}

// goJobFilename produces a .go filename that Go won't silently exclude
// (leading _/., trailing _test/_<goos>/_<goarch>).
// All transforms preserve the user-chosen pipeline name in
// sparkwing.yaml; only the on-disk filename is adjusted.
func goJobFilename(name string) string {
	snake := kebabToSnake(name)
	if strings.HasPrefix(snake, "_") || strings.HasPrefix(snake, ".") {
		snake = "pipeline_" + snake
	}
	if parts := strings.Split(snake, "_"); len(parts) >= 2 {
		last := parts[len(parts)-1]
		if goReservedTrailingTokens[last] {
			snake += "_pipeline"
		}
	}
	return snake + ".go"
}

// builtinShape is one structural starting point: a DAG and the trigger
// it declares when the author does not say otherwise.
//
// Shape and trigger are separate fields because they are separate
// decisions. Welding them together is what three agent trials in a row
// complained about: `ci-pr-check` was the only shape declaring
// `on: pull_request`, so wanting a PR-triggered single check meant
// scaffolding three nodes and deleting two. `--on` unwelds them.
type builtinShape struct {
	Name string
	// Nodes is how many jobs the shape's Plan builds, surfaced
	// wherever the shape is offered. Trials picked `ci-pr-check` on
	// its name and discovered its three nodes after scaffolding.
	Nodes int
	// Structure is the one-phrase DAG summary shown beside Nodes.
	Structure string
	// DefaultOn is the trigger event the shape declares when --on is
	// absent. Empty means the pipeline runs only when invoked.
	DefaultOn string
	src       string
}

func builtinShapeByName(name string) (builtinShape, bool) {
	for _, s := range builtinShapes {
		if s.Name == name {
			return s, true
		}
	}
	return builtinShape{}, false
}

// Summary is the one-line form used everywhere a shape is offered:
// what it builds, and what fires it.
func (s builtinShape) Summary() string {
	unit := "nodes"
	if s.Nodes == 1 {
		unit = "node"
	}
	out := fmt.Sprintf("%d %s, %s", s.Nodes, unit, s.Structure)
	if s.DefaultOn != "" {
		out += "; on: " + s.DefaultOn
	}
	return out
}

var builtinShapes = []builtinShape{
	{"minimal", 1, "stubbed Run -- the smallest thing that runs", "", minimalTemplate},
	{"build-test-deploy", 3, "in a line", "", buildTestDeployTemplate},
	{"ci-pr-check", 3, "lint and test in parallel, converging on a gate", "pull_request", ciPRCheckTemplate},
	{"release", 3, "version bump, changelog, publish", "", releaseTemplate},
	{"scheduled-report", 5, "one collector fanning out to gatherers", "schedule", scheduledReportTemplate},
}

// triggerBlocks are the per-event entries that go under `on:`, indented
// for a sparkwing.yaml pipeline entry.
//
// Every value carries a comment naming the one thing its reader most
// likely wants next: the filter it does not have, or the fact that a
// webhook still has to point here. A scaffolded trigger that silently
// matches everything is the same trap as no trigger at all.
//
// Filters stay empty. `branches: [main]` would bake in a branch name
// the repo may not use, and shapes have to be correct anywhere.
var triggerBlocks = map[string]string{
	"pull_request": `      # Fires on opened / synchronize / reopened. Add
      # ` + "`branches: [main]`" + ` to record which base branches this is
      # meant for, and point the repo's GitHub webhook at this pipeline.
      pull_request: {}
`,
	"push": `      # Fires on any branch. Add ` + "`branches: [main]`" + ` or
      # ` + "`paths: [\"**/*.go\"]`" + ` to narrow it, and point the repo's
      # GitHub webhook at this pipeline.
      push: {}
`,
	"schedule": `      # Cron cadence (UTC), declarative: drive it with an external
      # timer that runs this pipeline. 09:00 daily.
      schedule: "0 9 * * *"
`,
	"manual": "",
}

// triggerEventNames is the --on vocabulary, in the order it is offered.
// "manual" is last because it is the opt-out, and it is spelled rather
// than left as an empty string so declining a trigger is something an
// author can say out loud.
var triggerEventNames = []string{"pull_request", "push", "schedule", "manual"}

// manualTrigger is the spelling that means "no trigger". It is the only
// value that cannot be combined, because it contradicts every other one.
const manualTrigger = "manual"

// parseOnFlag flattens repeated and comma-separated --on values.
//
// Both forms are accepted because both are things people type: GitHub
// spells this `on: [push, pull_request]`, so a comma reads naturally,
// while a repeatable flag is what shells and generated commands prefer.
// Rejecting either would be a round-trip over punctuation.
func parseOnFlag(values []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" || seen[part] {
				continue
			}
			seen[part] = true
			out = append(out, part)
		}
	}
	return out
}

// resolveTrigger builds the `on:` block. An explicit --on wins;
// otherwise the shape's own default applies, so `--template ci-pr-check`
// keeps declaring pull_request without anyone repeating themselves.
//
// One pipeline can declare several events -- `on:` is a map, and the
// workflow that prompted this declares both push and pull_request. When
// --on took a single value, reproducing that meant hand-editing the
// yaml the scaffolder had just written.
func resolveTrigger(shape builtinShape, on []string, explicit bool) (string, error) {
	events := []string{shape.DefaultOn}
	if explicit {
		events = on
	}
	var blocks []string
	for _, event := range events {
		if event == "" {
			continue
		}
		block, ok := triggerBlocks[event]
		if !ok {
			return "", fmt.Errorf("new: unknown trigger %q -- --on takes any of: %s\n"+
				"  combine them with a comma or by repeating --on\n"+
				"  every trigger type and its fields: sparkwing docs search -q \"on: trigger\"",
				event, strings.Join(triggerEventNames, ", "))
		}
		if event == manualTrigger {
			if len(events) > 1 {
				return "", fmt.Errorf("new: --on %s cannot be combined with %s -- "+
					"manual means no trigger at all\n"+
					"  drop it to declare the others, or pass it alone to declare none",
					manualTrigger, strings.Join(without(events, manualTrigger), ", "))
			}
			continue
		}
		blocks = append(blocks, block)
	}
	if len(blocks) == 0 {
		return "", nil
	}
	return "    on:\n" + strings.Join(blocks, ""), nil
}

func without(list []string, drop string) []string {
	var out []string
	for _, v := range list {
		if v != drop {
			out = append(out, v)
		}
	}
	return out
}

// renderBuiltinTemplate expands the {{STRUCT}} / {{NAME}} / {{SHORTLIT}}
// placeholders in a built-in template into compilable jobs-package
// source for the given pipeline name. SHORTLIT is the strconv.Quote'd
// literal so quoted user input survives codegen.
func renderBuiltinTemplate(name, short, tmpl string) string {
	if short == "" {
		short = "one-line description of " + name
	}
	return strings.NewReplacer(
		"{{STRUCT}}", kebabToPascal(name),
		"{{NAME}}", name,
		"{{SHORTLIT}}", strconv.Quote(short),
	).Replace(tmpl)
}

// scaffoldGoFromTemplate is the shared write path.
func scaffoldGoFromTemplate(sparkwingDir, name string, hidden bool, short, tmpl string, bootstrapped bool, trigger string) error {
	struct_ := kebabToPascal(name)
	file := filepath.Join(sparkwingDir, "jobs", goJobFilename(name))
	if _, err := os.Stat(file); err == nil {
		return fmt.Errorf("refusing to overwrite %s\n  pick a different --name, or delete the file first if you want to regenerate", file)
	}
	body := renderBuiltinTemplate(name, short, tmpl)
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		return err
	}
	if err := appendPipelinesYAML(sparkwingDir, name, struct_, hidden, trigger); err != nil {
		return err
	}
	return finishScaffold(sparkwingDir, file, name, bootstrapped, trigger)
}

// finishScaffold is the shared post-write reporting + tidy step for both
// the built-in string templates and the rendered registry templates.
//
// A written trigger is reported in the created-files list rather than
// left for the author to notice. `pipeline new` wiring a repo into a
// GitHub event is exactly the kind of thing that should not happen
// silently, and the line doubles as the answer to "how do I declare
// this" for anyone who wants a different one.
func finishScaffold(sparkwingDir, file, name string, bootstrapped bool, trigger string) error {
	rel, err := filepath.Rel(filepath.Dir(sparkwingDir), file)
	if err != nil {
		rel = file
	}
	if bootstrapped {
		fmt.Println()
	}
	fmt.Printf("%s Creating new pipeline\n", color.Cyan("==>"))
	fmt.Printf("  %s %s\n", color.Green("+"), rel)
	fmt.Printf("  %s added %q entry to .sparkwing/sparkwing.yaml\n", color.Green("+"), name)
	// "declared", not "enabled". The yaml records intent; the
	// controller still has to receive the event, which means pointing a
	// webhook at this pipeline by hand. An agent trial read the success
	// output, did not open the yaml, and noted it would reasonably have
	// reported the trigger as live -- so the output has to say which
	// half it did.
	if events := triggerEvents(trigger); len(events) > 0 {
		fmt.Printf("  %s declared %s in sparkwing.yaml\n",
			color.Green("+"), color.Bold(strings.Join(events, " + ")+" trigger"))
		// Each says what it still needs, because they need different
		// things: a webhook has to be pointed here, a schedule needs a
		// timer to fire the cadence it records.
		if slices.Contains(events, "schedule") {
			fmt.Printf("    %s\n", color.Dim("schedule: declarative cadence; drive it with an external timer"))
		}
		if slices.Contains(events, "push") || slices.Contains(events, "pull_request") {
			fmt.Printf("    %s\n", color.Dim("not yet live: point the repo's GitHub webhook at this pipeline to deliver the event"))
		}
	}
	tidy := tidySkeleton(sparkwingDir)
	switch {
	case tidy.Skipped:
	case tidy.OK:
		fmt.Printf("  %s %s\n", color.Green("+"), color.Dim(tidy.Note))
	default:
		fmt.Printf("  %s %s\n", color.Red("x"), tidy.Note)
		if tidy.Err != "" {
			for _, line := range strings.Split(tidy.Err, "\n") {
				fmt.Printf("      %s\n", color.Dim(line))
			}
		}
	}
	fmt.Println()
	fmt.Println(color.Bold("TIPS"))
	tips := []InfoNextStep{
		{Command: "sparkwing run " + name, Purpose: "run it"},
		{Command: "sparkwing docs read --topic sdk", Purpose: "SDK reference for editing the stub"},
	}
	// A shape that declared no trigger runs only when someone types its
	// name, and nothing on this screen would say so. That is the gap
	// worth one line: an agent trial that scaffolded `minimal` and
	// wanted a pull-request gate spent twice the median number of calls
	// reading two full reference topics to find the `on:` schema, while
	// the search that answers it in one hop went unused. Name the query,
	// not the topic -- a search hit is a section, a topic is a page.
	if len(triggerEvents(trigger)) == 0 {
		tips = append(tips, InfoNextStep{
			Command: `sparkwing docs search -q "on: trigger"`,
			Purpose: "fire it on push / pull request / schedule instead of by hand",
		})
	} else {
		tips = append(tips, InfoNextStep{
			Command: `sparkwing docs search -q "on: trigger"`,
			Purpose: "the other trigger types, and the fields this one takes",
		})
	}
	tips = append(tips,
		InfoNextStep{Command: "sparkwing docs read --topic pipelines", Purpose: "sparkwing.yaml + DAG concepts"},
		InfoNextStep{Command: "sparkwing dashboard start", Purpose: "see runs in local dashboard"},
	)
	printAlignedSteps(tips)
	return nil
}

const minimalTemplate = `package jobs

import (
	"context"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

// {{STRUCT}} is a sparkwing pipeline. See ` + "`sparkwing docs read --topic sdk`" + ` for SDK helpers.
type {{STRUCT}} struct{ sw.Base }

func (p {{STRUCT}}) ShortHelp() string { return {{SHORTLIT}} }

// Help is the long-form description; defaults to ShortHelp until you have more to say.
func (p {{STRUCT}}) Help() string { return p.ShortHelp() }

func ({{STRUCT}}) Examples() []sw.Example {
	return []sw.Example{
		{Comment: "Run locally", Command: "sparkwing run {{NAME}}"},
	}
}

// Plan registers the pipeline's DAG on the passed-in *Plan. run
// carries run-time environment: run.Args (CLI flags), run.Git (repo
// state), run.Trigger (push/manual/schedule/webhook), run.Pipeline
// (registered name).
func ({{STRUCT}}) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, run sw.RunContext) error {
	sw.Job(plan, run.Pipeline, &{{STRUCT}}Job{})
	return nil
}

type {{STRUCT}}Job struct{ sw.Base }

func (j *{{STRUCT}}Job) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "run", j.run)
	return nil, nil
}

// Paths in Exec / Bash / ReadFile are relative to the repo root, not
// .sparkwing/. See WorkDir().
func ({{STRUCT}}Job) run(ctx context.Context) error {
	sw.Info(ctx, "replace this stub with your logic")
	// Shell out and propagate failure:
	//   if _, err := sw.Bash(ctx, "go test ./...").Run(); err != nil {
	//           return err
	//   }
	return nil
}

func init() {
	sw.Register[sw.NoInputs]("{{NAME}}", func() sw.Pipeline[sw.NoInputs] { return &{{STRUCT}}{} })
}
`

// buildTestDeployTemplate: the canonical CI shape. Three nodes with
// classic build->test->deploy ordering. Each Run shells `echo` so
// `sparkwing run <name>` succeeds end-to-end on first invocation; the user
// fills in real commands once they see the structure pass. The
// inline DAG comment is intentional (pipeline-specific structure,
// not SDK reference) -- the SDK cookbook lives in `docs read
// --topic sdk` and the stub points there rather than copying it.
const buildTestDeployTemplate = `package jobs

import (
	"context"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

// {{STRUCT}} is a build/test/deploy pipeline.
//
//   build -> test -> deploy
//
// See ` + "`sparkwing docs read --topic sdk`" + ` for SDK helpers.
type {{STRUCT}} struct{ sw.Base }

func (p {{STRUCT}}) ShortHelp() string { return {{SHORTLIT}} }

// Help is the long-form description; defaults to ShortHelp until you have more to say.
func (p {{STRUCT}}) Help() string { return p.ShortHelp() }

func ({{STRUCT}}) Examples() []sw.Example {
	return []sw.Example{
		{Comment: "Run locally", Command: "sparkwing run {{NAME}}"},
	}
}

// Plan registers the pipeline's DAG on the passed-in *Plan. run
// carries run-time environment: run.Args (CLI flags), run.Git (repo
// state), run.Trigger (push/manual/schedule/webhook), run.Pipeline
// (registered name).
func ({{STRUCT}}) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, run sw.RunContext) error {
	build := sw.Job(plan, "build", &{{STRUCT}}Build{})
	test := sw.Job(plan, "test", &{{STRUCT}}Test{}).Needs(build)
	sw.Job(plan, "deploy", &{{STRUCT}}Deploy{}).Needs(test)
	return nil
}

type {{STRUCT}}Build struct{ sw.Base }

func (j *{{STRUCT}}Build) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "run", j.run)
	return nil, nil
}

// Paths in .Dir() / ReadFile are relative to the repo root, not
// .sparkwing/. See WorkDir().
func ({{STRUCT}}Build) run(ctx context.Context) error {
	_, err := sw.Bash(ctx, ` + "`echo \"build step - replace with real logic\"`" + `).Run()
	return err
}

type {{STRUCT}}Test struct{ sw.Base }

func (j *{{STRUCT}}Test) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "run", j.run)
	return nil, nil
}

func ({{STRUCT}}Test) run(ctx context.Context) error {
	_, err := sw.Bash(ctx, ` + "`echo \"test step - replace with real logic\"`" + `).Run()
	return err
}

type {{STRUCT}}Deploy struct{ sw.Base }

func (j *{{STRUCT}}Deploy) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "run", j.run)
	return nil, nil
}

func ({{STRUCT}}Deploy) run(ctx context.Context) error {
	_, err := sw.Bash(ctx, ` + "`echo \"deploy step - replace with real logic\"`" + `).Run()
	return err
}

func init() {
	sw.Register[sw.NoInputs]("{{NAME}}", func() sw.Pipeline[sw.NoInputs] { return &{{STRUCT}}{} })
}
`

// ciPRCheckTemplate: the canonical pull-request gate. lint and test
// run in parallel and a final gate job depends on both, so the
// pipeline is green only when every check passes. test declares a
// runner-label preference (Prefers) in plan-snapshot metadata. Prefers
// does not affect runner selection. The gate is Inline (a cheap
// convergence node that runs on the dispatcher's host) so it declares
// no runner label.
const ciPRCheckTemplate = `package jobs

import (
	"context"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

// {{STRUCT}} is a pull-request gate pipeline.
//
//   lint, test (in parallel) -> gate
//
// gate passes only when both lint and test pass. See ` + "`sparkwing docs read --topic sdk`" + ` for SDK helpers.
type {{STRUCT}} struct{ sw.Base }

func (p {{STRUCT}}) ShortHelp() string { return {{SHORTLIT}} }

// Help is the long-form description; defaults to ShortHelp until you have more to say.
func (p {{STRUCT}}) Help() string { return p.ShortHelp() }

func ({{STRUCT}}) Examples() []sw.Example {
	return []sw.Example{
		{Comment: "Run the gate locally", Command: "sparkwing run {{NAME}}"},
		{Comment: "Render the DAG without running", Command: "sparkwing pipeline explain --name {{NAME}}"},
	}
}

// Plan registers the pipeline's DAG on the passed-in *Plan. run
// carries run-time environment: run.Args (CLI flags), run.Git (repo
// state), run.Trigger (push/manual/schedule/webhook), run.Pipeline
// (registered name).
//
// Prefers records plan-snapshot metadata; it
// does not affect runner selection.
func ({{STRUCT}}) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, run sw.RunContext) error {
	lint := sw.Job(plan, "lint", &{{STRUCT}}Lint{})
	test := sw.Job(plan, "test", &{{STRUCT}}Test{}).Prefers("ci-linux")
	sw.Job(plan, "gate", &{{STRUCT}}Gate{}).Needs(lint, test).Inline()
	return nil
}

type {{STRUCT}}Lint struct{ sw.Base }

func (j *{{STRUCT}}Lint) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "run", j.run)
	return nil, nil
}

// Paths in .Dir() / ReadFile are relative to the repo root, not
// .sparkwing/. See WorkDir().
func ({{STRUCT}}Lint) run(ctx context.Context) error {
	_, err := sw.Bash(ctx, ` + "`echo \"lint step - replace with your linter\"`" + `).Run()
	return err
}

type {{STRUCT}}Test struct{ sw.Base }

func (j *{{STRUCT}}Test) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "run", j.run)
	return nil, nil
}

func ({{STRUCT}}Test) run(ctx context.Context) error {
	_, err := sw.Bash(ctx, ` + "`echo \"test step - replace with your test command\"`" + `).Run()
	return err
}

type {{STRUCT}}Gate struct{ sw.Base }

func (j *{{STRUCT}}Gate) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "run", j.run)
	return nil, nil
}

func ({{STRUCT}}Gate) run(ctx context.Context) error {
	sw.Info(ctx, "all checks passed")
	return nil
}

func init() {
	sw.Register[sw.NoInputs]("{{NAME}}", func() sw.Pipeline[sw.NoInputs] { return &{{STRUCT}}{} })
}
`

// releaseTemplate: the canonical release shape. A linear
// version-bump -> changelog -> publish flow with echo Run bodies so
// the first ` + "`sparkwing run <name>`" + ` succeeds end-to-end. publish
// Prefers a release runner label to show placement intent without
// stranding a local run.
const releaseTemplate = `package jobs

import (
	"context"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

// {{STRUCT}} is a release pipeline.
//
//   version-bump -> changelog -> publish
//
// A linear release flow: compute the next version, regenerate the
// changelog, then tag and publish. See ` + "`sparkwing docs read --topic sdk`" + ` for SDK helpers.
type {{STRUCT}} struct{ sw.Base }

func (p {{STRUCT}}) ShortHelp() string { return {{SHORTLIT}} }

// Help is the long-form description; defaults to ShortHelp until you have more to say.
func (p {{STRUCT}}) Help() string { return p.ShortHelp() }

func ({{STRUCT}}) Examples() []sw.Example {
	return []sw.Example{
		{Comment: "Run the release flow", Command: "sparkwing run {{NAME}}"},
		{Comment: "Render the DAG without running", Command: "sparkwing pipeline explain --name {{NAME}}"},
	}
}

// Plan registers the pipeline's DAG on the passed-in *Plan. run
// carries run-time environment: run.Args (CLI flags), run.Git (repo
// state), run.Trigger (push/manual/schedule/webhook), run.Pipeline
// (registered name).
func ({{STRUCT}}) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, run sw.RunContext) error {
	bump := sw.Job(plan, "version-bump", &{{STRUCT}}VersionBump{})
	changelog := sw.Job(plan, "changelog", &{{STRUCT}}Changelog{}).Needs(bump)
	sw.Job(plan, "publish", &{{STRUCT}}Publish{}).Needs(changelog).Prefers("release")
	return nil
}

type {{STRUCT}}VersionBump struct{ sw.Base }

func (j *{{STRUCT}}VersionBump) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "run", j.run)
	return nil, nil
}

// Paths in .Dir() / ReadFile are relative to the repo root, not
// .sparkwing/. See WorkDir().
func ({{STRUCT}}VersionBump) run(ctx context.Context) error {
	_, err := sw.Bash(ctx, ` + "`echo \"version-bump step - compute the next version\"`" + `).Run()
	return err
}

type {{STRUCT}}Changelog struct{ sw.Base }

func (j *{{STRUCT}}Changelog) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "run", j.run)
	return nil, nil
}

func ({{STRUCT}}Changelog) run(ctx context.Context) error {
	_, err := sw.Bash(ctx, ` + "`echo \"changelog step - regenerate the changelog\"`" + `).Run()
	return err
}

type {{STRUCT}}Publish struct{ sw.Base }

func (j *{{STRUCT}}Publish) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "run", j.run)
	return nil, nil
}

func ({{STRUCT}}Publish) run(ctx context.Context) error {
	_, err := sw.Bash(ctx, ` + "`echo \"publish step - tag and publish the release\"`" + `).Run()
	return err
}

func init() {
	sw.Register[sw.NoInputs]("{{NAME}}", func() sw.Pipeline[sw.NoInputs] { return &{{STRUCT}}{} })
}
`

// scheduledReportTemplate: the canonical scheduled-report shape. One
// collect job seeds three parallel gatherers that fan out, and
// publish-report converges them into a single summary. Designed to run
// on a schedule -- the scaffold prints the exact sparkwing.yaml `+"`on:`"+`
// trigger to add. gather-metrics Prefers a report runner label to show
// placement intent; publish-report is Inline so it declares no label.
const scheduledReportTemplate = `package jobs

import (
	"context"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

// {{STRUCT}} is a scheduled report pipeline.
//
//   collect -> { gather-metrics, gather-errors, gather-usage } -> publish-report
//
// A fan-out report: collect seeds three independent gatherers that run
// in parallel, and publish-report converges them into one summary.
// Designed to run on a schedule -- add an "on:" trigger to this
// pipeline's .sparkwing/sparkwing.yaml entry:
//
//   on:
//     schedule: "0 8 * * *"   # daily at 08:00 UTC
//
// See ` + "`sparkwing docs read --topic sdk`" + ` for SDK helpers.
type {{STRUCT}} struct{ sw.Base }

func (p {{STRUCT}}) ShortHelp() string { return {{SHORTLIT}} }

// Help is the long-form description; defaults to ShortHelp until you have more to say.
func (p {{STRUCT}}) Help() string { return p.ShortHelp() }

func ({{STRUCT}}) Examples() []sw.Example {
	return []sw.Example{
		{Comment: "Run the report now", Command: "sparkwing run {{NAME}}"},
		{Comment: "Render the fan-out DAG", Command: "sparkwing pipeline explain --name {{NAME}}"},
	}
}

// Plan registers the pipeline's DAG on the passed-in *Plan. run
// carries run-time environment: run.Args (CLI flags), run.Git (repo
// state), run.Trigger (push/manual/schedule/webhook), run.Pipeline
// (registered name).
func ({{STRUCT}}) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, run sw.RunContext) error {
	collect := sw.Job(plan, "collect", &{{STRUCT}}Collect{})
	metrics := sw.Job(plan, "gather-metrics", &{{STRUCT}}GatherMetrics{}).Needs(collect).Prefers("report")
	errs := sw.Job(plan, "gather-errors", &{{STRUCT}}GatherErrors{}).Needs(collect)
	usage := sw.Job(plan, "gather-usage", &{{STRUCT}}GatherUsage{}).Needs(collect)
	sw.Job(plan, "publish-report", &{{STRUCT}}PublishReport{}).Needs(metrics, errs, usage).Inline()
	return nil
}

type {{STRUCT}}Collect struct{ sw.Base }

func (j *{{STRUCT}}Collect) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "run", j.run)
	return nil, nil
}

// Paths in .Dir() / ReadFile are relative to the repo root, not
// .sparkwing/. See WorkDir().
func ({{STRUCT}}Collect) run(ctx context.Context) error {
	_, err := sw.Bash(ctx, ` + "`echo \"collect step - gather the reporting window\"`" + `).Run()
	return err
}

type {{STRUCT}}GatherMetrics struct{ sw.Base }

func (j *{{STRUCT}}GatherMetrics) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "run", j.run)
	return nil, nil
}

func ({{STRUCT}}GatherMetrics) run(ctx context.Context) error {
	_, err := sw.Bash(ctx, ` + "`echo \"gather-metrics step - summarize metrics\"`" + `).Run()
	return err
}

type {{STRUCT}}GatherErrors struct{ sw.Base }

func (j *{{STRUCT}}GatherErrors) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "run", j.run)
	return nil, nil
}

func ({{STRUCT}}GatherErrors) run(ctx context.Context) error {
	_, err := sw.Bash(ctx, ` + "`echo \"gather-errors step - summarize errors\"`" + `).Run()
	return err
}

type {{STRUCT}}GatherUsage struct{ sw.Base }

func (j *{{STRUCT}}GatherUsage) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "run", j.run)
	return nil, nil
}

func ({{STRUCT}}GatherUsage) run(ctx context.Context) error {
	_, err := sw.Bash(ctx, ` + "`echo \"gather-usage step - summarize usage\"`" + `).Run()
	return err
}

type {{STRUCT}}PublishReport struct{ sw.Base }

func (j *{{STRUCT}}PublishReport) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "run", j.run)
	return nil, nil
}

func ({{STRUCT}}PublishReport) run(ctx context.Context) error {
	sw.Info(ctx, "report published")
	return nil
}

func init() {
	sw.Register[sw.NoInputs]("{{NAME}}", func() sw.Pipeline[sw.NoInputs] { return &{{STRUCT}}{} })
}
`

// appendPipelinesYAML tacks a new entry onto .sparkwing/sparkwing.yaml
// in the same shape the existing entries use. Plain text append keeps
// the author's formatting (leading comments, spacing) intact -- a yaml
// round-trip would reflow everything. Risk: the user's file could have
// exotic yaml that we don't preserve; mitigated by the simplicity of
// the append (we only add, never modify).
//
// trigger, when non-empty, is an already-indented `on:` block appended
// under the entry.
func appendPipelinesYAML(sparkwingDir, name, entrypoint string, hidden bool, trigger string) error {
	path := filepath.Join(sparkwingDir, projectconfig.Filename)
	existing, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var b bytes.Buffer
	b.Write(existing)
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "\n  - name: %s\n    entrypoint: %s\n", name, entrypoint)
	if hidden {
		b.WriteString("    hidden: true\n")
	}
	b.WriteString(trigger)
	return os.WriteFile(path, b.Bytes(), 0o644)
}

// triggerEvent names the event a scaffolded `on:` block declares, for
// the created-files line. It reads the block's own text so the two
// cannot drift: a trigger nobody names here is a trigger nobody reports.
func triggerEvents(trigger string) []string {
	var out []string
	for _, event := range triggerEventNames {
		if event == manualTrigger {
			continue
		}
		if strings.Contains(trigger, "\n      "+event+":") {
			out = append(out, event)
		}
	}
	return out
}
