package jobs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

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
	// GoEnv carries GOCACHE / GOMODCACHE / GOPATH so a job that runs the
	// inner pipeline under an overridden HOME (the postgres fixture, to
	// point secret resolution at a scratch dotenv) keeps the host's warm
	// build and module caches instead of a cold HOME default.
	GoEnv map[string]string `json:"go_env"`
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
	return "Builds the sparkwing CLI from the working tree, then fans out one job per sparks-core registry template. Each job scaffolds the template into a throwaway repo using the manifest's verify_params, then runs `go build ./...`, `sparkwing pipeline lint`, and `sparkwing pipeline explain`. Templates that import sparks-core blocks are built against the local sparks-core checkout (discovered via SPARKWING_SPARKS_CORE_DIR, the repo go.work, or a sibling ../sparks-core) so a template can be verified against unreleased library APIs it co-develops with. Templates tagged verify: runnable also run end-to-end against a synthesized fixture (a go module, a Dockerfile, a Node package, a Python package, or an ephemeral Postgres whose DSN is injected as a masked secret for the run); verify: dry-runnable templates run the same way with SPARKWING_DRY_RUN=1 exported so cloud mutations echo instead of executing. When a fixture's toolchain or any command in the manifest's verify_tools is missing on the host (Docker daemon, node/npm, python3, migrate, pg_dump, ...) the run step is skipped, not failed, so the gate stays green. The pipeline is green only when every template passes, which is why the release pipeline gates on it."
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
	return verifyEnv{CLI: bin, SparksCore: core, GoEnv: readGoEnv(ctx)}, nil
}

// readGoEnv captures the host's GOCACHE / GOMODCACHE / GOPATH so a job
// running the inner pipeline under an overridden HOME still resolves the
// warm caches. Best-effort: a lookup failure yields an empty map and the
// inner run falls back to the (overridden) HOME defaults.
func readGoEnv(ctx context.Context) map[string]string {
	keys := []string{"GOCACHE", "GOMODCACHE", "GOPATH"}
	res, err := sparkwing.Exec(ctx, "go", append([]string{"env"}, keys...)...).Capture()
	if err != nil {
		return map[string]string{}
	}
	lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
	out := map[string]string{}
	for i, k := range keys {
		if i < len(lines) {
			if v := strings.TrimSpace(lines[i]); v != "" {
				out[k] = v
			}
		}
	}
	return out
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
			if ready, missing := runToolchainReady(ctx, m); !ready {
				sparkwing.Info(ctx, "%s: run SKIPPED -- %s not available on host; keeping gate green (compiled + linted + explained only)", m.Name, missing)
				sparkwing.Annotate(ctx, fmt.Sprintf("%s: compiled + linted + explained; run skipped (%s unavailable)", m.Name, missing))
				return nil
			}
			cleanup, runEnv, err := provisionFixture(ctx, scratch, m, env)
			if err != nil {
				return fmt.Errorf("%s: fixture: %w", m.Name, err)
			}
			defer cleanup()
			runCmd := sparkwing.Exec(ctx, bin, "run", m.Name).Dir(scratch)
			mode := "ran green"
			if m.Tier() == templates.VerifyDryRunnable {
				runCmd = runCmd.Env("SPARKWING_DRY_RUN", "1")
				mode = "ran green (dry-run)"
			}
			for _, k := range sortedKeys(runEnv) {
				runCmd = runCmd.Env(k, runEnv[k])
			}
			if _, err := runCmd.Run(); err != nil {
				return fmt.Errorf("%s: run: %w", m.Name, err)
			}
			sparkwing.Annotate(ctx, fmt.Sprintf("%s: compiled + linted + explained + %s", m.Name, mode))
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

// runToolchainReady reports whether the host has everything a
// runnable/dry-runnable run needs -- the fixture's own toolchain plus
// every command in the manifest's verify_tools -- naming the first
// missing one otherwise. When anything is absent the run step is skipped
// (not failed), so the gate stays green on a host that lacks a tool. It
// runs before any fixture provisioning, so a missing tool never leaves a
// half-started container behind.
func runToolchainReady(ctx context.Context, m templates.Manifest) (bool, string) {
	if ok, missing := fixtureToolchainReady(ctx, m.Fixture()); !ok {
		return false, missing
	}
	for _, tool := range m.VerifyTools {
		if tool == "docker" {
			if !dockerAvailable(ctx) {
				return false, "docker daemon"
			}
			continue
		}
		if _, err := exec.LookPath(tool); err != nil {
			return false, tool
		}
	}
	return true, ""
}

// fixtureToolchainReady reports whether the host has the toolchain a
// fixture's run needs, naming the missing tool otherwise. The interpreter
// each fixture synthesizes against is the contract: the node fixture
// drives npm, the python fixture stdlib python3, and the docker and
// postgres fixtures the Docker daemon (postgres provisions an ephemeral
// container).
func fixtureToolchainReady(ctx context.Context, fixture string) (bool, string) {
	switch fixture {
	case templates.FixtureDocker, templates.FixturePostgres:
		if !dockerAvailable(ctx) {
			return false, "docker daemon"
		}
	case templates.FixtureNodeModule:
		for _, tool := range []string{"node", "npm"} {
			if _, err := exec.LookPath(tool); err != nil {
				return false, tool
			}
		}
	case templates.FixturePythonModule:
		if _, err := exec.LookPath("python3"); err != nil {
			return false, "python3"
		}
	}
	return true, ""
}

// dockerAvailable reports whether a Docker daemon is reachable.
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
	case templates.FixtureNodeModule:
		return seedNodeModule(root)
	case templates.FixturePythonModule:
		return seedPythonModule(root)
	default:
		return fmt.Errorf("unknown fixture %q", fixture)
	}
}

// seedGoModule writes a buildable go module at the repo root: a package
// with a fully covered function plus its test, so a coverage-gated
// template's `go test -coverprofile` reports nonzero total statements and
// 100% coverage (clearing any threshold), and an integration test
// package for templates whose default command runs `go test ./integration/...`.
func seedGoModule(root string) error {
	files := map[string]string{
		"go.mod":       "module verifyfixture\n\ngo 1.26\n",
		"main.go":      "package main\n\nfunc main() {}\n\n// Sum returns the total of nums.\nfunc Sum(nums ...int) int {\n\ttotal := 0\n\tfor _, n := range nums {\n\t\ttotal += n\n\t}\n\treturn total\n}\n",
		"main_test.go": "package main\n\nimport \"testing\"\n\nfunc TestSum(t *testing.T) {\n\tif got := Sum(1, 2, 3); got != 6 {\n\t\tt.Fatalf(\"Sum = %d, want 6\", got)\n\t}\n}\n",
		filepath.Join("integration", "integration_test.go"): "package integration\n\nimport \"testing\"\n\nfunc TestIntegration(t *testing.T) {}\n",
	}
	return writeFixtureFiles(root, files)
}

// provisionFixture writes a fixture's files and, for the postgres
// fixture, starts an ephemeral database and injects its DSN as a masked
// secret for the inner run. It returns a cleanup to run after the node
// (a no-op except for postgres, which tears the container down) and any
// extra environment the inner `sparkwing run` needs (the HOME override
// that points secret resolution at the scratch dotenv, plus the host's
// Go caches so that override stays fast).
func provisionFixture(ctx context.Context, scratch string, m templates.Manifest, env verifyEnv) (func(), map[string]string, error) {
	noop := func() {}
	if m.Fixture() != templates.FixturePostgres {
		return noop, nil, seedFixture(scratch, m.Fixture())
	}
	if err := seedGoModule(scratch); err != nil {
		return noop, nil, err
	}
	migrationsDir := valueOr(m.VerifyParams["migrations-dir"], "db/migrations")
	if err := seedMigrations(scratch, migrationsDir); err != nil {
		return noop, nil, err
	}
	dsn, cleanup, err := startEphemeralPostgres(ctx)
	if err != nil {
		return noop, nil, err
	}
	secretName := valueOr(m.VerifyParams["dsn-secret"], "DATABASE_URL")
	home := filepath.Join(scratch, "home")
	if err := writeMaskedSecret(home, secretName, dsn); err != nil {
		cleanup()
		return noop, nil, err
	}
	runEnv := map[string]string{"HOME": home}
	for k, v := range env.GoEnv {
		runEnv[k] = v
	}
	return cleanup, runEnv, nil
}

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// seedMigrations writes a trivial reversible golang-migrate pair into dir
// (relative to root), matching the NNNN_name.up.sql / .down.sql naming.
func seedMigrations(root, dir string) error {
	files := map[string]string{
		filepath.Join(dir, "0001_init.up.sql"):   "CREATE TABLE verify_fixture (id integer PRIMARY KEY);\n",
		filepath.Join(dir, "0001_init.down.sql"): "DROP TABLE verify_fixture;\n",
	}
	return writeFixtureFiles(root, files)
}

// startEphemeralPostgres runs a throwaway postgres container on a free
// host port, waits for it to accept connections, and returns its DSN plus
// a cleanup that removes the container. Callers must invoke cleanup.
func startEphemeralPostgres(ctx context.Context) (string, func(), error) {
	port, err := freeTCPPort()
	if err != nil {
		return "", nil, fmt.Errorf("pick free port: %w", err)
	}
	name := fmt.Sprintf("sparkwing-tv-pg-%d", time.Now().UnixNano())
	if _, err := sparkwing.Exec(ctx, "docker", "run", "-d", "--name", name,
		"-e", "POSTGRES_PASSWORD=postgres",
		"-p", fmt.Sprintf("127.0.0.1:%d:5432", port),
		"postgres:16-alpine").Capture(); err != nil {
		return "", nil, fmt.Errorf("start postgres: %w", err)
	}
	cleanup := func() { _, _ = sparkwing.Exec(context.Background(), "docker", "rm", "-f", name).Capture() }
	if err := waitPostgresReady(ctx, name); err != nil {
		cleanup()
		return "", nil, err
	}
	dsn := fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", port)
	return dsn, cleanup, nil
}

// waitPostgresReady polls pg_isready inside the container until it accepts
// connections or a deadline passes.
func waitPostgresReady(ctx context.Context, container string) error {
	deadline := time.Now().Add(90 * time.Second)
	for {
		if _, err := sparkwing.Exec(ctx, "docker", "exec", container, "pg_isready", "-U", "postgres").Capture(); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("postgres container %s not ready within 90s", container)
		}
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func freeTCPPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// writeMaskedSecret writes a single KEY=VALUE into a scratch masked
// dotenv under home, the file the CLI's secret resolver reads when the
// inner run is invoked with HOME set to home. Keeping it in a scratch
// home leaves the operator's real ~/.config/sparkwing/secrets.env alone.
func writeMaskedSecret(home, name, value string) error {
	dir := filepath.Join(home, ".config", "sparkwing")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "secrets.env"), []byte(name+"="+value+"\n"), 0o600)
}

func seedDocker(root string) error {
	files := map[string]string{
		"Dockerfile":    "FROM alpine:3.20\nCMD [\"true\"]\n",
		".dockerignore": ".sparkwing\ndist\n",
	}
	return writeFixtureFiles(root, files)
}

// seedNodeModule writes a dependency-free Node project at the repo root:
// a package.json whose install / lint / typecheck / build / test scripts
// all pass with only the Node runtime (no npm install, no network -- the
// lockfile has zero dependencies and the test runs the stdlib test
// runner), plus a passing test file.
func seedNodeModule(root string) error {
	files := map[string]string{
		"package.json": `{
  "name": "verify-fixture",
  "version": "0.0.0",
  "private": true,
  "scripts": {
    "build": "node -e \"process.exit(0)\"",
    "lint": "node -e \"process.exit(0)\"",
    "typecheck": "node -e \"process.exit(0)\"",
    "test": "node --test"
  }
}
`,
		"package-lock.json": `{
  "name": "verify-fixture",
  "version": "0.0.0",
  "lockfileVersion": 3,
  "requires": true,
  "packages": {
    "": { "name": "verify-fixture", "version": "0.0.0" }
  }
}
`,
		filepath.Join("test", "smoke.test.js"): "const { test } = require(\"node:test\");\n\ntest(\"fixture smoke\", () => {});\n",
	}
	return writeFixtureFiles(root, files)
}

// seedPythonModule writes a dependency-free Python project at the repo
// root: a pyproject.toml, a trivial importable package, and a passing
// stdlib unittest that `python3 -m unittest discover` finds -- no pip,
// uv, pytest, or network required.
func seedPythonModule(root string) error {
	files := map[string]string{
		"pyproject.toml": "[project]\nname = \"verify-fixture\"\nversion = \"0.0.0\"\n",
		filepath.Join("verify_fixture", "__init__.py"): "",
		"test_smoke.py": "import unittest\n\n\nclass SmokeTest(unittest.TestCase):\n    def test_ok(self) -> None:\n        self.assertTrue(True)\n",
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
