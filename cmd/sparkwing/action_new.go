// `sparkwing pipeline new` scaffolder. Templates: minimal (default) +
// build-test-deploy. Goal: get to a compiling, runnable stub fast.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

func runPipelineNew(args []string) error {
	fs := flag.NewFlagSet(cmdPipelineNew.Path, flag.ContinueOnError)
	pipelineName := fs.String("name", "", "new pipeline name (kebab-case, e.g. deploy-staging)")
	template := fs.String("template", "minimal", "template: minimal (default) | build-test-deploy")
	group := fs.String("group", "", "group name (surfaces in wing <TAB> section header)")
	hidden := fs.Bool("hidden", false, "mark the entry hidden in tab-complete menus")
	short := fs.String("short", "", "short one-line description (ShortHelp / frontmatter desc)")
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

	// Refuse silent clobber on duplicate name.
	if _, cfg, derr := pipelines.Discover(cwd); derr == nil && cfg != nil {
		for _, p := range cfg.Pipelines {
			if p.Name == name {
				return fmt.Errorf("pipeline %q already exists in pipelines.yaml (entrypoint %q)", name, p.Entrypoint)
			}
		}
	}

	if hint := goInstallHint(); hint != "" {
		fmt.Fprintln(os.Stderr, "warning: Go is not on PATH. Scaffolding will succeed but `sparkwing run "+name+"` will fail until Go is installed.")
		fmt.Fprintln(os.Stderr, "  "+hint)
		fmt.Fprintln(os.Stderr)
	}

	switch *template {
	case "minimal":
		return scaffoldGoMinimal(sparkwingDir, name, *group, *hidden, *short, bootstrapped)
	case "build-test-deploy":
		return scaffoldGoBuildTestDeploy(sparkwingDir, name, *group, *hidden, *short, bootstrapped)
	default:
		return fmt.Errorf("new: unknown template %q (valid: minimal, build-test-deploy)", *template)
	}
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
	// GOOS
	"aix": true, "android": true, "darwin": true, "dragonfly": true,
	"freebsd": true, "hurd": true, "illumos": true, "ios": true,
	"js": true, "linux": true, "nacl": true, "netbsd": true,
	"openbsd": true, "plan9": true, "solaris": true, "wasip1": true,
	"windows": true, "zos": true,
	// GOARCH
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
// pipelines.yaml; only the on-disk filename is adjusted.
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

// scaffoldGoMinimal emits a Plan-returning pipeline with one stubbed
// node. Default template: smallest viable shape that still teaches
// the canonical Plan() entry-point so editing means "add another
// sparkwing.Job(plan, ...) line", not "refactor a one-step pipeline into Plan()".
func scaffoldGoMinimal(sparkwingDir, name, group string, hidden bool, short string, bootstrapped bool) error {
	return scaffoldGoFromTemplate(sparkwingDir, name, group, hidden, short, minimalTemplate, bootstrapped)
}

// scaffoldGoBuildTestDeploy: build -> test -> deploy 3-node Plan.
func scaffoldGoBuildTestDeploy(sparkwingDir, name, group string, hidden bool, short string, bootstrapped bool) error {
	return scaffoldGoFromTemplate(sparkwingDir, name, group, hidden, short, buildTestDeployTemplate, bootstrapped)
}

// scaffoldGoFromTemplate is the shared write path. SHORTLIT is the
// strconv.Quote'd literal so quoted user input survives codegen.
func scaffoldGoFromTemplate(sparkwingDir, name, group string, hidden bool, short, tmpl string, bootstrapped bool) error {
	struct_ := kebabToPascal(name)
	file := filepath.Join(sparkwingDir, "jobs", goJobFilename(name))
	if _, err := os.Stat(file); err == nil {
		return fmt.Errorf("refusing to overwrite %s\n  pick a different --name, or delete the file first if you want to regenerate", file)
	}
	if short == "" {
		short = "TODO: one-line description of " + name
	}
	body := strings.NewReplacer(
		"{{STRUCT}}", struct_,
		"{{NAME}}", name,
		"{{SHORTLIT}}", strconv.Quote(short),
	).Replace(tmpl)
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		return err
	}
	if err := appendPipelinesYAML(sparkwingDir, name, struct_, group, hidden); err != nil {
		return err
	}
	rel, err := filepath.Rel(filepath.Dir(sparkwingDir), file)
	if err != nil {
		rel = file
	}
	if bootstrapped {
		fmt.Println()
	}
	fmt.Printf("%s Creating new pipeline\n", color.Cyan("==>"))
	fmt.Printf("  %s %s\n", color.Green("+"), rel)
	fmt.Printf("  %s added %q entry to .sparkwing/pipelines.yaml\n", color.Green("+"), name)
	tidy := tidySkeleton(sparkwingDir, true)
	switch {
	case tidy.Skipped:
		// nothing
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
		{Command: "sparkwing docs read --topic pipelines", Purpose: "pipelines.yaml + DAG concepts"},
		{Command: "sparkwing dashboard start", Purpose: "see runs in local dashboard"},
		{Command: "sparkwing info", Purpose: "find out more about sparkwing"},
	}
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
		{Comment: "Run locally", Command: "wing {{NAME}}"},
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
	w.Step("run", j.run)
	return nil, nil
}

// Paths in ExecIn / BashIn / ReadFile are relative to the repo root,
// not .sparkwing/. See WorkDir().
func ({{STRUCT}}Job) run(ctx context.Context) error {
	sw.Info(ctx, "TODO: replace with your logic")
	return nil
}

func init() {
	sw.Register[sw.NoInputs]("{{NAME}}", func() sw.Pipeline[sw.NoInputs] { return &{{STRUCT}}{} })
}
`

// buildTestDeployTemplate: the canonical CI shape. Three nodes with
// classic build->test->deploy ordering. Each Run shells `echo` so
// `wing <name>` succeeds end-to-end on first invocation; the user
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
		{Comment: "Run locally", Command: "wing {{NAME}}"},
	}
}

// Plan registers the pipeline's DAG on the passed-in *Plan. run
// carries run-time environment: run.Args (CLI flags), run.Git (repo
// state), run.Trigger (push/manual/schedule/webhook), run.Pipeline
// (registered name).
func ({{STRUCT}}) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, run sw.RunContext) error {
	build := sw.Job(plan, "build", &{{STRUCT}}BuildJob{})
	test := sw.Job(plan, "test", &{{STRUCT}}TestJob{}).Needs(build)
	sw.Job(plan, "deploy", &{{STRUCT}}DeployJob{}).Needs(test)
	return nil
}

type {{STRUCT}}BuildJob struct{ sw.Base }

func (j *{{STRUCT}}BuildJob) Work(w *sw.Work) (*sw.WorkStep, error) {
	w.Step("run", j.run)
	return nil, nil
}

// Paths in .Dir() / ReadFile are relative to the repo root, not
// .sparkwing/. See WorkDir().
func ({{STRUCT}}BuildJob) run(ctx context.Context) error {
	_, err := sw.Bash(ctx, ` + "`echo \"TODO: build\"`" + `).Run()
	return err
}

type {{STRUCT}}TestJob struct{ sw.Base }

func (j *{{STRUCT}}TestJob) Work(w *sw.Work) (*sw.WorkStep, error) {
	w.Step("run", j.run)
	return nil, nil
}

func ({{STRUCT}}TestJob) run(ctx context.Context) error {
	_, err := sw.Bash(ctx, ` + "`echo \"TODO: test\"`" + `).Run()
	return err
}

type {{STRUCT}}DeployJob struct{ sw.Base }

func (j *{{STRUCT}}DeployJob) Work(w *sw.Work) (*sw.WorkStep, error) {
	w.Step("run", j.run)
	return nil, nil
}

func ({{STRUCT}}DeployJob) run(ctx context.Context) error {
	_, err := sw.Bash(ctx, ` + "`echo \"TODO: deploy\"`" + `).Run()
	return err
}

func init() {
	sw.Register[sw.NoInputs]("{{NAME}}", func() sw.Pipeline[sw.NoInputs] { return &{{STRUCT}}{} })
}
`

// appendPipelinesYAML tacks a new entry onto .sparkwing/pipelines.yaml
// in the same shape the existing entries use. Plain text append keeps
// the author's formatting (leading comments, spacing) intact -- a yaml
// round-trip would reflow everything. Risk: the user's file could have
// exotic yaml that we don't preserve; mitigated by the simplicity of
// the append (we only add, never modify).
func appendPipelinesYAML(sparkwingDir, name, entrypoint, group string, hidden bool) error {
	path := filepath.Join(sparkwingDir, "pipelines.yaml")
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
	if group != "" {
		fmt.Fprintf(&b, "    group: %s\n", group)
	}
	if hidden {
		b.WriteString("    hidden: true\n")
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}
