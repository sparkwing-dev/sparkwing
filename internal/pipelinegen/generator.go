package pipelinegen

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os/exec"
	"path"
	"strings"
)

// Generator turns a spec into pipeline Go source. The default is
// fixture-backed (deterministic); a CommandGenerator drives a live
// model so the same scorer measures real generation.
type Generator interface {
	// Generate returns the pipeline source for spec, in package jobs,
	// registering spec.Name with entrypoint spec.Entrypoint.
	Generate(ctx context.Context, spec Spec) (string, error)
	// Label names the generator for the report header.
	Label() string
}

// FixtureGenerator returns the source each spec ships in its corpus
// directory (candidate.go). A run over fixtures is fully reproducible:
// it scores fixed source, which makes the corpus a regression gate on
// the linter, explain, and the template catalog the fixtures imitate.
type FixtureGenerator struct {
	FS   fs.FS
	Root string
}

func (g FixtureGenerator) Label() string { return "fixture" }

func (g FixtureGenerator) Generate(_ context.Context, spec Spec) (string, error) {
	raw, err := fs.ReadFile(g.FS, path.Join(g.Root, spec.Name, "candidate.go"))
	if err != nil {
		return "", fmt.Errorf("fixture %q: %w", spec.Name, err)
	}
	return string(raw), nil
}

// CommandGenerator shells an external generator: it runs Argv with the
// spec's prompt on stdin and takes the pipeline source from stdout. This
// is the seam a real AI generator plugs into without touching the
// scorer -- the harness stays the measuring instrument, the model stays
// swappable.
type CommandGenerator struct {
	Argv []string
}

func (g CommandGenerator) Label() string {
	if len(g.Argv) == 0 {
		return "command"
	}
	return "command:" + strings.Join(g.Argv, " ")
}

func (g CommandGenerator) Generate(ctx context.Context, spec Spec) (string, error) {
	if len(g.Argv) == 0 {
		return "", fmt.Errorf("generator command is empty")
	}
	cmd := exec.CommandContext(ctx, g.Argv[0], g.Argv[1:]...)
	cmd.Stdin = strings.NewReader(spec.Prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("generator: %s", detail)
	}
	src := stdout.String()
	if strings.TrimSpace(src) == "" {
		return "", fmt.Errorf("generator produced no output")
	}
	return src, nil
}
