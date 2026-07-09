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

// CheckName identifies one bar a generated pipeline must clear.
type CheckName string

const (
	// CheckFormat is `gofmt -l` of the generated source: it reports no
	// files, i.e. the source is already canonically formatted.
	CheckFormat CheckName = "format"
	// CheckCompile is `go build` of the generated source in a project.
	CheckCompile CheckName = "compile"
	// CheckVet is `go vet` of the generated project: the source is free of
	// the suspicious constructs vet reports.
	CheckVet CheckName = "vet"
	// CheckExplain is `sparkwing pipeline explain --all`: Plan() builds a
	// valid DAG without dispatching any job.
	CheckExplain CheckName = "explain"
	// CheckLint is `sparkwing pipeline lint --all`: the source is free of
	// idiomatic anti-patterns.
	CheckLint CheckName = "lint"
)

// CheckResult is the outcome of one check. Detail carries the truncated
// tool output when OK is false, so a reviewer can reproduce the failure.
type CheckResult struct {
	Name   CheckName `json:"name"`
	OK     bool      `json:"ok"`
	Detail string    `json:"detail,omitempty"`
}

// Scorer runs a generated pipeline through the
// gofmt+compile+vet+explain+lint bar.
type Scorer interface {
	Score(ctx context.Context, spec Spec, source string) ([]CheckResult, error)
}

// ProjectScorer scores a generation by materializing a single-pipeline
// .sparkwing project in a temp dir -- copying go.mod/go.sum/main.go from
// a discovered base project so the build resolves against the same
// pinned SDK -- then running the oracle bar against it. Each spec gets
// its own project so a generation that does not compile cannot poison
// the others.
type ProjectScorer struct {
	// Sparkwing is the path to the sparkwing binary used for the explain
	// and lint checks.
	Sparkwing string
	// BaseDir is a .sparkwing directory whose go.mod/go.sum/main.go are
	// copied to build the temp project.
	BaseDir string
}

// NewProjectScorer locates a base .sparkwing project by copying the
// build files from baseDir and using sparkwingBin for the CLI checks.
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
	yaml := fmt.Sprintf("pipelines:\n  - name: %s\n    entrypoint: %s\n", spec.Name, spec.Entrypoint)
	if err := os.WriteFile(filepath.Join(proj, "sparkwing.yaml"), []byte(yaml), 0o644); err != nil {
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

// runFormatCheck runs `gofmt -l` over dir. gofmt exits 0 even when files
// need formatting -- it lists them on stdout -- so the check is OK only
// when that list is empty, so a pipeline that is not gofmt-clean fails
// acceptance.
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

// runCheck runs name's command in dir, mapping a zero exit to OK and a
// non-zero exit (or spawn failure) to a failed check with truncated
// combined output as the detail.
func runCheck(ctx context.Context, name CheckName, dir, bin string, args ...string) CheckResult {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return CheckResult{Name: name, OK: true}
	}
	return CheckResult{Name: name, OK: false, Detail: truncate(strings.TrimSpace(string(out)), 600)}
}

// writeRebasedGoMod copies baseGoMod to dst, rewriting every local
// filesystem replace (a replace whose target has no version, e.g.
// `replace x => ..`) from a path relative to baseDir into an absolute
// path. The base .sparkwing project resolves the SDK with a relative
// replace anchored at its own directory; a verbatim copy into a temp
// project would resolve that path against the temp dir, where the SDK
// is absent, so the rebase is what lets the generated pipeline compile.
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
