package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/mod/module"

	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/scaffold"
)

func bootstrapDotSparkwing(cwd, sparkwingDir string) error {
	moduleName := scaffoldModuleName(filepath.Base(cwd))
	_, err := writeSkeleton(sparkwingDir, moduleName, false)
	if err != nil {
		return err
	}
	return nil
}

var moduleUnsafeRE = regexp.MustCompile(`[^A-Za-z0-9._~-]+`)

func scaffoldModuleName(dir string) string {
	base := strings.Trim(moduleUnsafeRE.ReplaceAllString(dir, "-"), "-.")
	if base == "" {
		base = "sparkwing"
	}
	name := base + "-pipelines"
	if module.CheckImportPath(name) == nil {
		return name
	}
	name = strings.ReplaceAll(base, ".", "-") + "-pipelines"
	if module.CheckImportPath(name) == nil {
		return name
	}
	return "sparkwing-pipelines"
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
	return fmt.Sprintf(`// Command %s serves this repo's pipeline definitions to the Sparkwing host.
package main

import (
	"github.com/sparkwing-dev/sparkwing/sparkwing"

	_ %q
)

func main() { sparkwing.Main() }
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
		"sparkwing pipeline new --pipeline <name>\n" +
		"```\n" +
		"\n" +
		"## Layout\n" +
		"\n" +
		"```\n" +
		".sparkwing/\n" +
		"  .gitignore          ignores the cached pipeline binary\n" +
		"  sparkwing.yaml      registry of every pipeline (name -> entrypoint)\n" +
		"  jobs/               Go package holding pipeline definitions; scaffold lands one .go file per pipeline\n" +
		"  main.go             thin entrypoint; delegates to sparkwing.Main\n" +
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
#   sparkwing pipeline new --pipeline <name> [--template <shape>]
#
# Shapes: minimal (default) | build-test-deploy | ci-pr-check |
# release | scheduled-report. An entry with no ` + "`on:`" + ` block runs only
# when invoked; see ` + "`sparkwing docs read --topic pipelines`" + ` for the
# trigger schema.
pipelines: []
`
}

func ensureGitignoreEntry(sparkwingDir, entry string) (err error) {
	root, err := os.OpenRoot(sparkwingDir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	f, err := root.OpenFile(".gitignore", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(line) == entry {
			return nil
		}
	}
	var b strings.Builder
	if len(body) > 0 {
		if !strings.HasSuffix(string(body), "\n") {
			b.WriteByte('\n')
		}
	}
	b.WriteString(entry)
	b.WriteByte('\n')
	_, err = f.WriteString(b.String())
	return err
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func tidySkeleton(ctx context.Context, sparkwingDir string) (bool, error) {
	if !goOnPath() {
		return false, nil
	}
	ctx = gotoolchain.WithSession(ctx, nil, nil)
	env, err := gotoolchain.BuildEnv(ctx, sparkwingDir, nil, "")
	if err != nil {
		return true, err
	}
	cmd := procenv.ControlCommandContext(ctx, env, "go", "mod", "tidy")
	cmd.Dir = sparkwingDir
	var captured bytes.Buffer
	cmd.Stdout = &captured
	cmd.Stderr = &captured

	stop := startSpinner("resolving dependencies (`go mod tidy`)")
	err = cmd.Run()
	stop()

	if err != nil {
		if explanation := gotoolchain.ExplainOutput(ctx, captured.String(), env); explanation != nil {
			return true, explanation
		}
		return true, fmt.Errorf("go mod tidy in %s failed; the pipeline files are written, so fix the error and rerun it there: %w\n%s",
			sparkwingDir, err, strings.TrimSpace(captured.String()))
	}
	return true, nil
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
