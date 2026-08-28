package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/scaffold"
)

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
	Skipped []string
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
		{filepath.Join(sparkwingDir, projectconfig.Filename), func() string { return renderInitPipelinesYAML() }},
		{filepath.Join(sparkwingDir, "README.md"), func() string { return renderInitReadme() }},
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
		fmt.Fprintf(os.Stderr, "init: note: could not update .gitignore: %v\n", err)
	}

	return rep, nil
}

func renderInitGoMod(moduleName string) string {
	goDirective := userGoModDirective()
	if goDirective == "" {
		goDirective = "1.26"
	}
	return fmt.Sprintf(`module %s

go %s

require github.com/sparkwing-dev/sparkwing %s
`, moduleName, goDirective, sdkRequirementVersion())
}

func sdkRequirementVersion() string {
	v := installedVersion()
	if isResolvableModuleVersion(v) {
		return v
	}
	return scaffold.FallbackSDKVersion
}

var pseudoVersionRE = regexp.MustCompile(`[-.]\d{14}-[0-9a-f]{12}(\+dirty)?$`)

func isResolvableModuleVersion(v string) bool {
	if v == "" || strings.HasPrefix(v, "(") {
		return false
	}
	if !strings.HasPrefix(v, "v") {
		return false
	}
	if i := strings.IndexByte(v, '+'); i >= 0 && v[i:] != "+incompatible" {
		return false
	}
	if strings.Contains(v, "-dev") {
		return false
	}
	if pseudoVersionRE.MatchString(v) {
		return false
	}
	return true
}

func renderInitMainGo(moduleName string) string {
	return fmt.Sprintf(`// Command %s is this repo's local pipeline runner.
// It re-exports runner.Main, which dispatches based on argv:
// `+"`sparkwing run <pipeline>`"+` invokes the pipeline; `+"`sparkwing pipeline ...`"+`
// is the agent/operator surface.
package main

import (
	"github.com/sparkwing-dev/sparkwing/pkg/runner"

	_ %q
)

func main() { runner.Main() }
`, moduleName, moduleName+"/jobs")
}

func renderInitReadme() string {
	return "# .sparkwing/\n" +
		"\n" +
		"This directory holds this repo's [sparkwing](https://sparkwing.dev) pipeline\n" +
		"definitions. Pipelines are Go programs registered in `sparkwing.yaml` and run\n" +
		"via `sparkwing run <name>`.\n" +
		"\n" +
		"Add a pipeline:\n" +
		"\n" +
		"```\n" +
		"sparkwing pipeline new --name <name>\n" +
		"```\n" +
		"\n" +
		"## Layout\n" +
		"\n" +
		"```\n" +
		".sparkwing/\n" +
		"  sparkwing.yaml      registry of every pipeline (name -> entrypoint)\n" +
		"  jobs/               Go package holding pipeline definitions; scaffold lands one .go file per pipeline\n" +
		"  main.go             thin entrypoint; delegates to runner.Main\n" +
		"  go.mod / go.sum     module + pinned SDK version\n" +
		"  sparkwing-pipeline  cached compiled binary (gitignored, regenerated)\n" +
		"```\n" +
		"\n" +
		"## Agents\n" +
		"\n" +
		"Run `sparkwing info --for-agent` for current, one-wake discovery context.\n" +
		"Do not copy runtime command catalogs into durable instruction files.\n"
}

func renderInitPipelinesYAML() string {
	return `# Registry of every pipeline this repo defines. Each entry
# below becomes an invocable target for ` + "`sparkwing run <name>`" + `.
#
# Add an entry by running:
#   sparkwing pipeline new --name <name> [--template <shape>]
#
# Shapes: minimal (default) | build-test-deploy | ci-pr-check |
# release | scheduled-report. An entry with no ` + "`on:`" + ` block runs only
# when invoked; see ` + "`sparkwing docs read --topic pipelines`" + ` for the
# trigger schema.
pipelines:
`
}

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
	b.WriteString("\n# sparkwing: cached pipeline binary, regenerated on each `sparkwing run` invocation\n")
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

func tidySkeleton(sparkwingDir string) tidyStatus {
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
	fmt.Printf("  2. sparkwing run release                   %s\n", color.Dim("# run it; replace the placeholder step with real logic"))
	fmt.Printf("  %s\n", color.Dim("for a build/test/deploy DAG: sparkwing pipeline new --name release --template build-test-deploy"))
	fmt.Println()
	fmt.Printf("  %s\n", color.Dim("dashboard:    sparkwing dashboard start"))
	fmt.Printf("  %s\n", color.Dim("docs:         sparkwing docs list  (or https://sparkwing.dev/docs)"))
	fmt.Printf("  %s\n", color.Dim("AI agents:    sparkwing info --for-agent  (current one-wake context)"))
}
