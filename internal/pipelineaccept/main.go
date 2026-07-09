// Command pipelineaccept runs the AI-pipeline acceptance harness on
// demand. It generates every corpus spec with a chosen generator, scores
// each through the gofmt/compile/vet/explain/lint oracle bar, prints the
// aggregate report, and exits nonzero when any spec disagrees with its
// expectation.
//
// With the default fixture generator the run is deterministic and needs no
// model: it is a regression gate on the pipeline templates, the authoring
// guide the fixtures imitate, the linter, and the SDK pin the candidate
// project builds against. With --generator=command it drives a live
// cold-author model through the same bar, so "can a cold agent author a
// working pipeline" becomes a repeatable check rather than a manual spike.
//
// Exit codes: 0 every spec matched its expectation; 1 a spec disagreed (a
// regression, or a cold author that failed the bar); 2 the harness could
// not run.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/pipelinegen"
)

type options struct {
	output    string
	generator string
	command   string
	spec      string
	base      string
	sparkwing string
}

func main() {
	var opts options
	flag.StringVar(&opts.output, "output", "pretty", "output format: pretty | json")
	flag.StringVar(&opts.generator, "generator", "fixture", "generator: fixture | command")
	flag.StringVar(&opts.command, "command", "", "cold-author command (whitespace-split argv) when --generator=command; the spec prompt is written to its stdin and pipeline source read from its stdout")
	flag.StringVar(&opts.spec, "spec", "", "run only the named corpus spec (default: all)")
	flag.StringVar(&opts.base, "base", "", "base .sparkwing dir seeding each candidate project (default: <repo>/.sparkwing)")
	flag.StringVar(&opts.sparkwing, "sparkwing", "", "path to a prebuilt sparkwing binary (default: build ./cmd/sparkwing)")
	flag.Parse()

	rep, err := run(context.Background(), opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pipelineaccept:", err)
		os.Exit(2)
	}
	if err := emit(os.Stdout, opts.output, rep); err != nil {
		fmt.Fprintln(os.Stderr, "pipelineaccept:", err)
		os.Exit(2)
	}
	os.Exit(exitCode(rep))
}

func run(ctx context.Context, opts options) (pipelinegen.Report, error) {
	root, err := repoRoot(ctx)
	if err != nil {
		return pipelinegen.Report{}, err
	}

	bin := opts.sparkwing
	if bin == "" {
		built, cleanup, err := buildSparkwing(ctx, root)
		if err != nil {
			return pipelinegen.Report{}, err
		}
		defer cleanup()
		bin = built
	}

	base := opts.base
	if base == "" {
		base = filepath.Join(root, ".sparkwing")
	}

	fsys, croot := pipelinegen.DefaultCorpus()
	specs, err := pipelinegen.LoadCorpus(fsys, croot)
	if err != nil {
		return pipelinegen.Report{}, err
	}
	if opts.spec != "" {
		specs, err = filterSpec(specs, opts.spec)
		if err != nil {
			return pipelinegen.Report{}, err
		}
	}
	// The expect=fail specs ship deliberately-bad fixture source to prove
	// the linter rejects anti-patterns; asking a live author to reproduce
	// an anti-pattern is not a cold-author test, so drop them for the
	// command generator (and say how many, never silently).
	if opts.generator == "command" {
		specs = dropAntiPatterns(specs)
	}

	gen, err := makeGenerator(opts, fsys, croot)
	if err != nil {
		return pipelinegen.Report{}, err
	}
	scorer := pipelinegen.NewProjectScorer(bin, base)
	return pipelinegen.Run(ctx, specs, gen, scorer), nil
}

// makeGenerator selects the generator from the flags. The fixture
// generator reads each spec's committed candidate source (deterministic);
// the command generator shells an external cold author, whitespace-split
// from --command.
func makeGenerator(opts options, fsys fs.FS, croot string) (pipelinegen.Generator, error) {
	switch opts.generator {
	case "fixture", "":
		return pipelinegen.FixtureGenerator{FS: fsys, Root: croot}, nil
	case "command":
		argv := strings.Fields(opts.command)
		if len(argv) == 0 {
			return nil, fmt.Errorf("--generator=command requires a non-empty --command")
		}
		return pipelinegen.CommandGenerator{Argv: argv}, nil
	default:
		return nil, fmt.Errorf("unknown generator %q (want fixture or command)", opts.generator)
	}
}

func exitCode(rep pipelinegen.Report) int {
	if rep.Matched < rep.Total {
		return 1
	}
	return 0
}

// dropAntiPatterns returns the expect=pass specs, noting on stderr how
// many expect=fail specs it skipped so a live run never silently narrows.
func dropAntiPatterns(specs []pipelinegen.Spec) []pipelinegen.Spec {
	kept := make([]pipelinegen.Spec, 0, len(specs))
	skipped := 0
	for _, s := range specs {
		if s.Expect == pipelinegen.ExpectFail {
			skipped++
			continue
		}
		kept = append(kept, s)
	}
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "pipelineaccept: skipped %d anti-pattern spec(s); they are fixture-only\n", skipped)
	}
	return kept
}

func filterSpec(specs []pipelinegen.Spec, name string) ([]pipelinegen.Spec, error) {
	for _, s := range specs {
		if s.Name == name {
			return []pipelinegen.Spec{s}, nil
		}
	}
	return nil, fmt.Errorf("no corpus spec named %q", name)
}

func repoRoot(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("locate repo root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// buildSparkwing compiles the sparkwing CLI the explain and lint oracles
// invoke, into a temp file the caller removes via cleanup.
func buildSparkwing(ctx context.Context, root string) (bin string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "pipelineaccept-")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	bin = filepath.Join(dir, "sparkwing")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/sparkwing")
	cmd.Dir = root
	if out, buildErr := cmd.CombinedOutput(); buildErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("build sparkwing: %w\n%s", buildErr, out)
	}
	return bin, cleanup, nil
}

// emit writes the report as indented JSON or a human summary.
func emit(w io.Writer, format string, rep pipelinegen.Report) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	case "pretty", "":
		return emitPretty(w, rep)
	default:
		return fmt.Errorf("unknown output format %q (want pretty or json)", format)
	}
}

func emitPretty(w io.Writer, rep pipelinegen.Report) error {
	if _, err := fmt.Fprintf(w, "generator: %s\n", rep.Generator); err != nil {
		return err
	}
	for _, r := range rep.Results {
		status := "PASS"
		if !r.Matched {
			status = "REGRESSION"
		} else if !r.Passed {
			status = "fail(expected)"
		}
		fmt.Fprintf(w, "  %-18s %-6s %-14s", r.Name, string(r.Expect), status)
		if r.GenError != "" {
			fmt.Fprintf(w, " gen-error: %s", firstLine(r.GenError))
		}
		for _, c := range r.Checks {
			if !c.OK {
				fmt.Fprintf(w, " [%s✗]", c.Name)
			}
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "matched %d/%d  pass %d/%d  pass-rate %.2f  (%dms)\n",
		rep.Matched, rep.Total, rep.Passed, rep.PassExpected, rep.PassRate, rep.TotalMS)
	if rep.Matched < rep.Total {
		fmt.Fprintln(w, "FAIL: a spec disagreed with its expectation")
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
