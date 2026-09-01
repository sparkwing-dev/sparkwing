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

type Generator interface {
	Generate(ctx context.Context, spec Spec) (string, error)

	Label() string
}

type Reviser interface {
	Revise(ctx context.Context, spec Spec, prev, feedback string) (string, error)
}

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

type CommandGenerator struct {
	Argv []string
}

func commandPrompt(spec Spec) string {
	return fmt.Sprintf(
		"Register the pipeline under the name %q with an entrypoint struct named %s.\n\n%s",
		spec.Name, spec.Entrypoint, spec.Prompt,
	)
}

func (g CommandGenerator) Label() string {
	if len(g.Argv) == 0 {
		return "command"
	}
	return "command:" + strings.Join(g.Argv, " ")
}

func (g CommandGenerator) Generate(ctx context.Context, spec Spec) (string, error) {
	return g.run(ctx, commandPrompt(spec))
}

func (g CommandGenerator) Revise(ctx context.Context, spec Spec, prev, feedback string) (string, error) {
	prompt := fmt.Sprintf(
		"%s\n\n=== YOUR PREVIOUS ATTEMPT ===\n%s\n\n=== IT WAS REJECTED ===\n%s\n\n"+
			"Fix the problems above and output the corrected full Go source. Output ONLY the Go source.",
		commandPrompt(spec), prev, feedback,
	)
	return g.run(ctx, prompt)
}

func (g CommandGenerator) run(ctx context.Context, prompt string) (string, error) {
	if len(g.Argv) == 0 {
		return "", fmt.Errorf("generator command is empty")
	}
	cmd := exec.CommandContext(ctx, g.Argv[0], g.Argv[1:]...)
	cmd.Stdin = strings.NewReader(prompt)
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
