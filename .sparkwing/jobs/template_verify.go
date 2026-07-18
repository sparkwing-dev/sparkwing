package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	templates "github.com/sparkwing-dev/sparks-core/templates"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// verifyTemplates is the registry snapshot the pipeline fans out over.
// It is read once at package load (the registry is an embedded FS, so
// the read is in-memory and deterministic) which keeps Plan() pure: the
// plan body only ranges over this slice, exactly as the build pipeline
// ranges over its compiled-in binary list.
var verifyTemplates, verifyTemplatesErr = templates.List()

// TemplateVerifySummary is the gate node's typed output: proof that
// every registered template passed. Cross-pipeline callers (the release
// gate) receive it via sparkwing.RunAndAwait.
type TemplateVerifySummary struct {
	Total int `json:"total"`
}

// verifyEnv is what build-cli hands each template job: the path to the
// sparkwing CLI built from the working tree, plus the local sparks-core
// module checkout (module path -> directory) discovered on this machine.
// Templates are verified against that in-development checkout -- the same
// sparks-core the repo is co-releasing -- rather than whatever tags the
// module proxy happens to serve.
type verifyEnv struct {
	CLI        string            `json:"cli"`
	SparksCore map[string]string `json:"sparks_core"`
}

// TemplateVerify proves that every sparks-core template is a working
// pipeline: it builds the sparkwing CLI from the working tree, then for
// each registered template scaffolds it into a throwaway repo with the
// template's sample parameters and checks it compiles, lints, and
// explains. Runnable-tier templates additionally run to green (a
// docker-fixture template is skipped when no Docker daemon is present).
type TemplateVerify struct{ sparkwing.Base }

func (TemplateVerify) ShortHelp() string {
	return "Scaffold, compile, lint, explain (and run) every sparks-core template"
}

func (TemplateVerify) Help() string {
	return "Builds the sparkwing CLI from the working tree, then fans out one job per sparks-core registry template. Each job scaffolds the template into a throwaway repo using the manifest's verify_params, then runs `go build ./...`, `sparkwing pipeline lint`, and `sparkwing pipeline explain`. Templates that import sparks-core blocks are built against the local sparks-core checkout (discovered via SPARKWING_SPARKS_CORE_DIR, the repo go.work, or a sibling ../sparks-core) so a template can be verified against unreleased library APIs it co-develops with. Templates tagged verify: runnable also run end-to-end against a synthesized fixture (a go module or a Dockerfile); a docker-fixture template's run is skipped when no Docker daemon is available. The pipeline is green only when every template passes, which is why the release pipeline gates on it."
}

func (TemplateVerify) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Verify the whole template registry", Command: "sparkwing run template-verify"},
		{Comment: "Render the fan-out DAG without running", Command: "sparkwing pipeline explain --name template-verify"},
	}
}

func (TemplateVerify) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	if verifyTemplatesErr != nil {
		return fmt.Errorf("template-verify: load registry: %w", verifyTemplatesErr)
	}
	plan.Resources(sparkwing.Cores(2), sparkwing.MemoryGB(4))

	build := sparkwing.Job(plan, "build-cli", &buildVerifyCLIJob{})
	envRef := sparkwing.RefTo[verifyEnv](build)

	deps := make([]sparkwing.Dep, 0, len(verifyTemplates))
	for _, tmpl := range verifyTemplates {
		m := tmpl.Manifest
		node := sparkwing.Job(plan, "verify-"+m.Name, verifyTemplateFn(m, envRef)).Needs(build)
		deps = append(deps, node)
	}

	sparkwing.Job(plan, "summary", &templateVerifyGate{}).Needs(deps...).Inline()
	return nil
}

// buildVerifyCLIJob builds the sparkwing CLI from the working tree into
// a throwaway directory and discovers the local sparks-core checkout,
// handing both to every template job.
type buildVerifyCLIJob struct {
	sparkwing.Base
	sparkwing.Produces[verifyEnv]
}

func (j *buildVerifyCLIJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", j.run), nil
}

func (j *buildVerifyCLIJob) run(ctx context.Context) (verifyEnv, error) {
	root := sparkwing.WorkDir()
	if root == "" {
		return verifyEnv{}, errors.New("template-verify: sparkwing.WorkDir() is empty")
	}
	dir, err := os.MkdirTemp("", "sparkwing-template-verify-cli-*")
	if err != nil {
		return verifyEnv{}, fmt.Errorf("template-verify: temp dir: %w", err)
	}
	bin := filepath.Join(dir, "sparkwing")
	if _, err := sparkwing.Exec(ctx, "go", "build", "-o", bin, "./cmd/sparkwing").Dir(root).Run(); err != nil {
		return verifyEnv{}, fmt.Errorf("template-verify: build CLI: %w", err)
	}
	core := discoverSparksCore(root)
	if len(core) > 0 {
		sparkwing.Annotate(ctx, fmt.Sprintf("built CLI; sparks-core checkout with %d modules", len(core)))
	} else {
		sparkwing.Annotate(ctx, "built CLI; no local sparks-core checkout (using published modules)")
	}
	return verifyEnv{CLI: bin, SparksCore: core}, nil
}

// templateVerifyGate is the convergence node every template job feeds.
// Its success is the invariant the release gate awaits: it can only run
// once all template jobs passed.
type templateVerifyGate struct {
	sparkwing.Base
	sparkwing.Produces[TemplateVerifySummary]
}

func (j *templateVerifyGate) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(ctx context.Context) (TemplateVerifySummary, error) {
		s := TemplateVerifySummary{Total: len(verifyTemplates)}
		sparkwing.Annotate(ctx, fmt.Sprintf("verified %d templates", s.Total))
		return s, nil
	}), nil
}

// verifyTemplateFn returns the job body that verifies one template: it
// scaffolds the template with its sample parameters into a throwaway
// repo, compiles the generated pipeline, lints and explains it, and --
// for a runnable/dry-runnable tier -- runs it against a synthesized
// fixture. Any failing check fails the job (and thus the pipeline).
func verifyTemplateFn(m templates.Manifest, envRef sparkwing.Ref[verifyEnv]) func(context.Context) error {
	return func(ctx context.Context) error {
		env := envRef.Get(ctx)
		bin := env.CLI
		scratch, err := os.MkdirTemp("", "sparkwing-tv-"+m.Name+"-*")
		if err != nil {
			return fmt.Errorf("%s: temp dir: %w", m.Name, err)
		}
		defer func() { _ = os.RemoveAll(scratch) }()

		newArgs := []string{"pipeline", "new", "-C", scratch, "--name", m.Name, "--template", m.Name}
		for _, p := range sortedParamFlags(m.VerifyParams) {
			newArgs = append(newArgs, "--param", p)
		}
		if _, err := sparkwing.Exec(ctx, bin, newArgs...).Run(); err != nil {
			return fmt.Errorf("%s: scaffold: %w", m.Name, err)
		}

		dotSparkwing := filepath.Join(scratch, ".sparkwing")
		if err := pinLocalSparksCore(ctx, dotSparkwing, env.SparksCore); err != nil {
			return fmt.Errorf("%s: pin sparks-core: %w", m.Name, err)
		}
		if _, err := sparkwing.Exec(ctx, "go", "build", "./...").Dir(dotSparkwing).Env("GOWORK", "off").Run(); err != nil {
			return fmt.Errorf("%s: go build: %w", m.Name, err)
		}
		if _, err := sparkwing.Exec(ctx, bin, "pipeline", "lint", "-C", scratch, "--all").Run(); err != nil {
			return fmt.Errorf("%s: lint: %w", m.Name, err)
		}
		if _, err := sparkwing.Exec(ctx, bin, "pipeline", "explain", "--name", m.Name).Dir(scratch).Run(); err != nil {
			return fmt.Errorf("%s: explain: %w", m.Name, err)
		}

		switch m.Tier() {
		case templates.VerifyCompileOnly:
			sparkwing.Annotate(ctx, fmt.Sprintf("%s: compiled + linted + explained (compile-only)", m.Name))
			return nil
		case templates.VerifyRunnable, templates.VerifyDryRunnable:
			if m.Fixture() == templates.FixtureDocker && !dockerAvailable(ctx) {
				sparkwing.Annotate(ctx, fmt.Sprintf("%s: compiled + linted + explained; run skipped (no Docker)", m.Name))
				return nil
			}
			if err := seedFixture(scratch, m.Fixture()); err != nil {
				return fmt.Errorf("%s: fixture: %w", m.Name, err)
			}
			if _, err := sparkwing.Exec(ctx, bin, "run", m.Name).Dir(scratch).Run(); err != nil {
				return fmt.Errorf("%s: run: %w", m.Name, err)
			}
			sparkwing.Annotate(ctx, fmt.Sprintf("%s: compiled + linted + explained + ran green", m.Name))
			return nil
		default:
			return fmt.Errorf("%s: unknown verify tier %q", m.Name, m.Tier())
		}
	}
}

// sortedParamFlags renders a verify_params map as sorted "k=v" strings
// so the scaffold command is deterministic.
func sortedParamFlags(params map[string]string) []string {
	out := make([]string, 0, len(params))
	for k, v := range params {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// pinLocalSparksCore rewrites the scratch .sparkwing/go.mod to resolve
// every sparks-core module from the local checkout via a filesystem
// replace, then re-tidies. This lets a template compile against
// unreleased sparks-core APIs it co-develops with, instead of the older
// tags the module proxy serves. A no-op when no checkout was found.
func pinLocalSparksCore(ctx context.Context, dotSparkwing string, core map[string]string) error {
	if len(core) == 0 {
		return nil
	}
	path := filepath.Join(dotSparkwing, "go.mod")
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.Write(body)
	if len(body) > 0 && body[len(body)-1] != '\n' {
		b.WriteByte('\n')
	}
	for _, mp := range sortedKeys(core) {
		fmt.Fprintf(&b, "\nreplace %s => %s\n", mp, core[mp])
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return err
	}
	if _, err := sparkwing.Exec(ctx, "go", "mod", "tidy").Dir(dotSparkwing).Env("GOWORK", "off").Run(); err != nil {
		return fmt.Errorf("go mod tidy: %w", err)
	}
	return nil
}

var goModModuleRe = regexp.MustCompile(`(?m)^module[\t ]+(\S+)`)

// discoverSparksCore returns the sparks-core module checkout as a map of
// module path to local directory. It looks for the checkout root in
// SPARKWING_SPARKS_CORE_DIR, then in the repo's go.work, then in a
// sibling ../sparks-core, and enumerates every sub-module directory.
// Returns nil when no checkout is found.
func discoverSparksCore(repoRoot string) map[string]string {
	root := sparksCoreRoot(repoRoot)
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err != nil {
			continue
		}
		match := goModModuleRe.FindSubmatch(raw)
		if match == nil {
			continue
		}
		mp := string(match[1])
		if strings.HasPrefix(mp, "github.com/sparkwing-dev/sparks-core/") {
			out[mp] = dir
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sparksCoreRoot resolves the directory holding the sparks-core
// sub-module checkouts, or "" when none is discoverable.
func sparksCoreRoot(repoRoot string) string {
	if env := strings.TrimSpace(os.Getenv("SPARKWING_SPARKS_CORE_DIR")); env != "" {
		if isDir(env) {
			return env
		}
	}
	if r := sparksCoreRootFromGoWork(repoRoot); r != "" {
		return r
	}
	sibling := filepath.Join(filepath.Dir(repoRoot), "sparks-core")
	if isDir(filepath.Join(sibling, "templates")) {
		return sibling
	}
	return ""
}

// sparksCoreRootFromGoWork scans repoRoot/go.work for a `use` entry
// pointing at the sparks-core templates module and returns its parent
// directory (the checkout root). Relative entries resolve against
// repoRoot.
func sparksCoreRootFromGoWork(repoRoot string) string {
	raw, err := os.ReadFile(filepath.Join(repoRoot, "go.work"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "use")
		line = strings.Trim(strings.TrimSpace(line), "()")
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "sparks-core") {
			continue
		}
		if filepath.Base(line) != "templates" {
			continue
		}
		p := line
		if !filepath.IsAbs(p) {
			p = filepath.Join(repoRoot, p)
		}
		root := filepath.Dir(p)
		if isDir(root) {
			return root
		}
	}
	return ""
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// dockerAvailable reports whether a Docker daemon is reachable. A
// docker-fixture template's run is skipped (not failed) when it isn't,
// so the gate stays green on machines without Docker.
func dockerAvailable(ctx context.Context) bool {
	_, err := sparkwing.Exec(ctx, "docker", "info").Capture()
	return err == nil
}

// seedFixture writes the scratch-repo scaffolding a runnable template
// needs at the repo root before it runs: FixtureNone writes nothing,
// FixtureGoModule a buildable go module, FixtureDocker that module plus
// a Dockerfile and an integration test package.
func seedFixture(root, fixture string) error {
	switch fixture {
	case templates.FixtureNone, "":
		return nil
	case templates.FixtureGoModule:
		return seedGoModule(root)
	case templates.FixtureDocker:
		if err := seedGoModule(root); err != nil {
			return err
		}
		return seedDocker(root)
	default:
		return fmt.Errorf("unknown fixture %q", fixture)
	}
}

func seedGoModule(root string) error {
	files := map[string]string{
		"go.mod":       "module verifyfixture\n\ngo 1.26\n",
		"main.go":      "package main\n\nfunc main() {}\n",
		"main_test.go": "package main\n\nimport \"testing\"\n\nfunc TestFixture(t *testing.T) {}\n",
		filepath.Join("integration", "integration_test.go"): "package integration\n\nimport \"testing\"\n\nfunc TestIntegration(t *testing.T) {}\n",
	}
	return writeFixtureFiles(root, files)
}

func seedDocker(root string) error {
	files := map[string]string{
		"Dockerfile":    "FROM alpine:3.20\nCMD [\"true\"]\n",
		".dockerignore": ".sparkwing\ndist\n",
	}
	return writeFixtureFiles(root, files)
}

func writeFixtureFiles(root string, files map[string]string) error {
	for rel, body := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func init() {
	sparkwing.Register("template-verify", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &TemplateVerify{} })
}
