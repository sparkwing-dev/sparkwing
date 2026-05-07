// Bootstrap helpers for `sparkwing pipeline new` first-run scaffolding.
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/pkg/color"
)

// fallbackSDKVersion is pinned into a fresh .sparkwing/go.mod when the
// running CLI's version can't be detected. **Bump on each release.**
// Must be ≥ v1.3.0 (pre-1.3 used the old module path).
const fallbackSDKVersion = "v1.3.1"

// bootstrapDotSparkwingOpts writes the .sparkwing/ skeleton. `go mod
// tidy` is deferred to the caller because tidy fails on the empty
// jobs/ package until the first scaffolded file lands.
// terse=true suppresses the "next steps" block (the scaffolder owns it).
func bootstrapDotSparkwingOpts(cwd, sparkwingDir string, terse bool) error {
	moduleName := filepath.Base(cwd) + "-pipelines"
	existed := dirExists(sparkwingDir)
	report, err := writeSkeleton(sparkwingDir, moduleName, false)
	if err != nil {
		return err
	}
	printInitReport(cwd, moduleName, existed, report, tidyStatus{Skipped: true}, terse)
	return nil
}

type initFileReport struct {
	Created []string
	Existed []string
	Skipped []string // existed without --force
}

func writeSkeleton(sparkwingDir, moduleName string, force bool) (initFileReport, error) {
	rep := initFileReport{}

	if err := os.MkdirAll(sparkwingDir, 0o755); err != nil {
		return rep, fmt.Errorf("init: create %s: %w", sparkwingDir, err)
	}
	for _, sub := range []string{"jobs"} {
		if err := os.MkdirAll(filepath.Join(sparkwingDir, sub), 0o755); err != nil {
			return rep, fmt.Errorf("init: create %s/%s: %w", sparkwingDir, sub, err)
		}
	}

	files := []struct {
		Path    string
		Content func() string
	}{
		{filepath.Join(sparkwingDir, "go.mod"), func() string { return renderInitGoMod(moduleName) }},
		{filepath.Join(sparkwingDir, "main.go"), func() string { return renderInitMainGo(moduleName) }},
		{filepath.Join(sparkwingDir, "pipelines.yaml"), func() string { return renderInitPipelinesYAML() }},
	}
	for _, f := range files {
		rel, _ := filepath.Rel(filepath.Dir(sparkwingDir), f.Path)
		if _, err := os.Stat(f.Path); err == nil {
			if !force {
				rep.Existed = append(rep.Existed, rel)
				continue
			}
			rep.Skipped = append(rep.Skipped, rel)
			continue
		}
		if err := os.WriteFile(f.Path, []byte(f.Content()), 0o644); err != nil {
			return rep, fmt.Errorf("init: write %s: %w", f.Path, err)
		}
		rep.Created = append(rep.Created, rel)
	}

	if err := ensureGitignoreEntry(filepath.Dir(sparkwingDir), ".sparkwing/sparkwing-pipeline"); err != nil {
		// Non-fatal: not every project tracks .gitignore.
		fmt.Fprintf(os.Stderr, "init: note: could not update .gitignore: %v\n", err)
	}

	return rep, nil
}

func renderInitGoMod(moduleName string) string {
	goDirective := userGoModDirective()
	if goDirective == "" {
		// SDK ≥ v1.3.0 requires Go 1.26 (transitive k8s.io/client-go bump).
		goDirective = "1.26"
	}
	return fmt.Sprintf(`module %s

go %s

require github.com/sparkwing-dev/sparkwing/v2 %s
`, moduleName, goDirective, sdkRequirementVersion())
}

// sdkRequirementVersion: tidy resolves to latest at first compile, so
// stale fallbacks are non-load-bearing.
func sdkRequirementVersion() string {
	return fallbackSDKVersion
}

func renderInitMainGo(moduleName string) string {
	return fmt.Sprintf(`// Command %s is this repo's local pipeline runner.
// It re-exports orchestrator.Main, which dispatches based on argv:
// `+"`wing <pipeline>`"+` invokes the pipeline; `+"`sparkwing pipeline ...`"+`
// is the agent/operator surface.
package main

import (
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator"

	_ %q
)

func main() { orchestrator.Main() }
`, moduleName, moduleName+"/jobs")
}

func renderInitPipelinesYAML() string {
	return `# Registry of every pipeline this repo defines. Each entry
# below becomes an invocable target for ` + "`sparkwing run <name>`" + `
# (or the human shortcut ` + "`wing <name>`" + `).
#
# Add an entry by running:
#   sparkwing pipeline new --name <name>
#
# (Default template is minimal; pass --template build-test-deploy
# for a build/test/deploy DAG.)
pipelines:
`
}

// ensureGitignoreEntry idempotently appends a line to .gitignore.
func ensureGitignoreEntry(repoRoot, entry string) error {
	path := filepath.Join(repoRoot, ".gitignore")
	body, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		body = nil
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(line) == entry {
			return nil
		}
	}
	var b strings.Builder
	if len(body) > 0 {
		b.Write(body)
		if !strings.HasSuffix(string(body), "\n") {
			b.WriteByte('\n')
		}
	}
	b.WriteString("\n# sparkwing: cached pipeline binary, regenerated on each `wing` invocation\n")
	b.WriteString(entry)
	b.WriteByte('\n')
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

type tidyStatus struct {
	Skipped bool
	OK      bool
	Note    string
	Err     string
}

// tidySkeleton runs `go mod tidy` so go.sum is populated before the
// first `wing <name>`. Captures Go's noisy stdout/stderr; dumps only on failure.
func tidySkeleton(sparkwingDir string, createdAny bool) tidyStatus {
	_ = createdAny
	if !goOnPath() {
		return tidyStatus{Skipped: true}
	}
	fmt.Println()
	cmd := exec.Command("go", "mod", "tidy")
	cmd.Dir = sparkwingDir
	var captured bytes.Buffer
	cmd.Stdout = &captured
	cmd.Stderr = &captured

	stop := startSpinner("resolving dependencies (`go mod tidy`)")
	err := cmd.Run()
	stop()

	if err != nil {
		return tidyStatus{OK: false, Note: "go mod tidy failed", Err: strings.TrimSpace(captured.String())}
	}
	return tidyStatus{OK: true, Note: "resolved dependencies (go mod tidy)"}
}

// startSpinner ticks a Braille spinner until cancel(). Non-TTY = no-op.
func startSpinner(label string) func() {
	if !color.IsInteractiveStdout() {
		fmt.Fprintln(os.Stderr, color.Dim("==> "+label+" ..."))
		return func() {}
	}
	frames := []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go runSpinner(os.Stderr, frames, label, done, stopped)
	return func() {
		close(done)
		<-stopped
	}
}

func runSpinner(w io.Writer, frames []rune, label string, done <-chan struct{}, stopped chan<- struct{}) {
	defer close(stopped)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	i := 0
	render := func() {
		fmt.Fprintf(w, "\r%s %s ", color.Cyan(string(frames[i%len(frames)])), label)
	}
	render()
	for {
		select {
		case <-done:
			fmt.Fprintf(w, "\r%s\r", strings.Repeat(" ", len(label)+8))
			return
		case <-tick.C:
			i++
			render()
		}
	}
}

func printInitReport(cwd, moduleName string, existedBefore bool, rep initFileReport, tidy tidyStatus, terse bool) {
	if existedBefore {
		fmt.Printf("%s .sparkwing already in place (module %s)\n", color.Cyan("==>"), moduleName)
	} else {
		fmt.Printf("%s bootstrapping .sparkwing\n", color.Cyan("==>"))
	}

	for _, p := range rep.Created {
		fmt.Printf("  %s %s\n", color.Green("+"), p)
	}
	for _, p := range rep.Existed {
		fmt.Printf("  %s %s\n", color.Dim("="), color.Dim(p))
	}
	for _, p := range rep.Skipped {
		fmt.Printf("  %s %s %s\n", color.Yellow("!"), p, color.Dim("(kept; pass --force to overwrite)"))
	}
	switch {
	case tidy.Skipped:
		// nothing to print
	case tidy.OK:
		fmt.Printf("  %s resolved dependencies (go mod tidy)\n", color.Green("+"))
	default:
		fmt.Printf("  %s go mod tidy %s\n", color.Red("x"), color.Dim("(see error below)"))
		if tidy.Err != "" {
			for _, line := range strings.Split(tidy.Err, "\n") {
				fmt.Printf("      %s\n", color.Dim(line))
			}
		}
	}

	if !goOnPath() {
		fmt.Println()
		fmt.Println("toolchain: Go is NOT on PATH")
		fmt.Printf("  %s\n", goInstallHintForce())
	}

	if terse {
		return
	}

	fmt.Println()
	fmt.Println("next steps:")
	fmt.Printf("  1. sparkwing pipeline new --name release   %s\n", color.Dim("# scaffold a single-node pipeline (default --template minimal)"))
	fmt.Printf("  2. sparkwing run release                   %s\n", color.Dim("# run it; replace the Log(\"TODO\") with real logic"))
	fmt.Printf("  %s\n", color.Dim("for a build/test/deploy DAG: sparkwing pipeline new --name release --template build-test-deploy"))
	fmt.Println()
	fmt.Printf("  %s\n", color.Dim("dashboard:    sparkwing dashboard start"))
	fmt.Printf("  %s\n", color.Dim("docs:         sparkwing docs list  (or https://sparkwing.dev/docs)"))
	fmt.Printf("  %s\n", color.Dim("AI agents:    sparkwing info --for-agent  (paste into CLAUDE.md / AGENTS.md)"))
}
