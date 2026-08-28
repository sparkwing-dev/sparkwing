package pipelinegen

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"
)

type CheckName string

const (
	CheckFormat CheckName = "format"

	CheckCompile CheckName = "compile"

	CheckVet CheckName = "vet"

	CheckExplain CheckName = "explain"

	CheckLint CheckName = "lint"
)

type CheckResult struct {
	Name   CheckName `json:"name"`
	OK     bool      `json:"ok"`
	Detail string    `json:"detail,omitempty"`
}

type Scorer interface {
	Score(ctx context.Context, spec Spec, source string) ([]CheckResult, error)
}

type ProjectScorer struct {
	Sparkwing string

	BaseDir string
}

func NewProjectScorer(sparkwingBin, baseDir string) *ProjectScorer {
	return &ProjectScorer{Sparkwing: sparkwingBin, BaseDir: baseDir}
}

func (s *ProjectScorer) Score(ctx context.Context, spec Spec, source string) ([]CheckResult, error) {
	tmp, err := os.MkdirTemp("", "pipelinegen-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	proj := filepath.Join(tmp, ".sparkwing")
	jobsDir := filepath.Join(proj, "jobs")
	if err := os.MkdirAll(jobsDir, 0o755); err != nil {
		return nil, err
	}
	if err := writeRebasedGoMod(filepath.Join(s.BaseDir, "go.mod"), filepath.Join(proj, "go.mod"), s.BaseDir); err != nil {
		return nil, fmt.Errorf("rebase base go.mod: %w", err)
	}
	for _, f := range []string{"go.sum", "main.go"} {
		if err := copyFile(filepath.Join(s.BaseDir, f), filepath.Join(proj, f)); err != nil {
			return nil, fmt.Errorf("copy base %s: %w", f, err)
		}
	}
	if err := os.WriteFile(filepath.Join(proj, "sparkwing.yaml"), []byte(pipelineYAML(spec)), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(jobsDir, "candidate.go"), []byte(source), 0o644); err != nil {
		return nil, err
	}

	checks := []CheckResult{
		runFormatCheck(ctx, jobsDir),
		runCheck(ctx, CheckCompile, proj, "go", "build", "./..."),
		runCheck(ctx, CheckVet, proj, "go", "vet", "./..."),
		runCheck(ctx, CheckExplain, tmp, s.Sparkwing, "pipeline", "explain", "--all", "-o", "json"),
		runCheck(ctx, CheckLint, tmp, s.Sparkwing, "pipeline", "lint", "--all", "-o", "json"),
	}
	return checks, nil
}

func pipelineYAML(spec Spec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "pipelines:\n  - name: %s\n    entrypoint: %s\n", spec.Name, spec.Entrypoint)
	if !spec.HasGuards() {
		return b.String()
	}
	b.WriteString("    guards:\n")
	if len(spec.GuardRequire) > 0 {
		fmt.Fprintf(&b, "      require: [%s]\n", strings.Join(spec.GuardRequire, ", "))
	}
	if len(spec.GuardReject) > 0 {
		fmt.Fprintf(&b, "      reject: [%s]\n", strings.Join(spec.GuardReject, ", "))
	}
	return b.String()
}

func runFormatCheck(ctx context.Context, dir string) CheckResult {
	out, err := exec.CommandContext(ctx, "gofmt", "-l", dir).CombinedOutput()
	if err != nil {
		return CheckResult{Name: CheckFormat, OK: false, Detail: truncate(strings.TrimSpace(string(out)), 600)}
	}
	if listed := strings.TrimSpace(string(out)); listed != "" {
		return CheckResult{Name: CheckFormat, OK: false, Detail: "needs gofmt: " + truncate(listed, 600)}
	}
	return CheckResult{Name: CheckFormat, OK: true}
}

func runCheck(ctx context.Context, name CheckName, dir, bin string, args ...string) CheckResult {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return CheckResult{Name: name, OK: true}
	}
	return CheckResult{Name: name, OK: false, Detail: truncate(strings.TrimSpace(string(out)), 600)}
}

func writeRebasedGoMod(baseGoMod, dst, baseDir string) error {
	raw, err := os.ReadFile(baseGoMod)
	if err != nil {
		return err
	}
	mf, err := modfile.Parse(baseGoMod, raw, nil)
	if err != nil {
		return err
	}
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return err
	}
	for _, r := range mf.Replace {
		if r.New.Version != "" || filepath.IsAbs(r.New.Path) {
			continue
		}
		abs := filepath.Clean(filepath.Join(absBase, r.New.Path))
		if err := mf.AddReplace(r.Old.Path, r.Old.Version, abs, ""); err != nil {
			return fmt.Errorf("rebase replace %s: %w", r.Old.Path, err)
		}
	}
	mf.Cleanup()
	out, err := mf.Format()
	if err != nil {
		return err
	}
	return os.WriteFile(dst, out, 0o644)
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + " ...(truncated)"
}
