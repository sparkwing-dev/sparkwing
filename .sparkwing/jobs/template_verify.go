package jobs

import (
	"bytes"
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
	"sync"
	"syscall"
	"time"

	templates "github.com/sparkwing-dev/sparks-core/templates"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var verifyTemplates, verifyTemplatesErr = templates.List()

var templateVerifyGroup = sparkwing.NewConcurrencyGroup("template-verify", sparkwing.ConcurrencyLimit{
	Capacity: 4,
	Scope:    sparkwing.ScopeRun,
	OnLimit:  sparkwing.Queue,
})

const templateVerifyMinFreeDisk = uint64(10 << 30)

type TemplateVerifySummary struct {
	Total int `json:"total"`
}

type verifyEnv struct {
	CLI string `json:"cli"`

	Root       string            `json:"root"`
	StateHome  string            `json:"state_home"`
	SparksCore map[string]string `json:"sparks_core"`

	GoEnv map[string]string `json:"go_env"`

	Proof proofEnv `json:"proof"`
}

// TemplateVerifyArgs are the flags `sparkwing run template-verify` accepts.
type TemplateVerifyArgs struct {
	Exhaustive bool `flag:"exhaustive" desc:"Verify every template even when a recorded proof covers its inputs. The release pipeline always sets this."`
}

var (
	templateVerifyLockOnce sync.Once
	templateVerifyLockErr  error
	templateVerifyLockFile *os.File
)

type TemplateVerify struct{ sparkwing.Base }

func (TemplateVerify) ShortHelp() string {
	return "Scaffold, compile, lint, explain (and run) every sparks-core template"
}

func (TemplateVerify) Help() string {
	return "Builds the sparkwing CLI from the working tree, then fans out one job per sparks-core registry template. Each job scaffolds the template into a throwaway repo using the manifest's verify_params, then runs `go build ./...`, `sparkwing pipeline lint`, and `sparkwing pipeline explain`. Templates that import sparks-core blocks are built against the local sparks-core checkout (discovered via SPARKWING_SPARKS_CORE_DIR, the repo go.work, or a sibling ../sparks-core) so a template can be verified against unreleased library APIs it co-develops with, and every scaffold's sparkwing SDK is replaced with the working tree so the release being cut is what gets verified (and so a runs-store schema bump cannot strand a released-SDK scaffold on the tree-migrated verify state home). Templates tagged verify: runnable also run end-to-end against a synthesized fixture (a go module, a Dockerfile, a Node package, a Python package, or an ephemeral Postgres whose DSN is injected as a masked secret for the run); verify: dry-runnable templates run the same way with SPARKWING_DRY_RUN=1 exported so cloud mutations echo instead of executing. When a fixture's toolchain or any command in the manifest's verify_tools is missing on the host (Docker daemon, node/npm, python3, migrate, pg_dump, ...) the run step is skipped, not failed, so the gate stays green. The pipeline is green only when every template passes, which is why the release pipeline gates on it. A template whose proof inputs are unchanged since a recorded pass is reused instead of re-verified: the digest covers the template's registry files, its verification manifest fields, the exact working state of the sparkwing and sparks-core checkouts (which is what pins the SDK, the CLI, and this verifier), the Go toolchain, and the identity of every host tool the template needs. Reuse is refused whenever any of those cannot be established, including when no local sparks-core checkout pins the module versions a scaffold resolves. Pass --exhaustive to verify every template regardless; the release pipeline always does."
}

func (TemplateVerify) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Verify the whole template registry", Command: "sparkwing run template-verify"},
		{Comment: "Render the fan-out DAG without running", Command: "sparkwing pipeline explain --name template-verify"},
		{Comment: "Re-verify every template, ignoring recorded proofs", Command: "sparkwing run template-verify --exhaustive"},
	}
}

func (TemplateVerify) Plan(_ context.Context, plan *sparkwing.Plan, in TemplateVerifyArgs, _ sparkwing.RunContext) error {
	if verifyTemplatesErr != nil {
		return fmt.Errorf("template-verify: load registry: %w", verifyTemplatesErr)
	}

	build := sparkwing.Job(plan, "build-cli", &buildVerifyCLIJob{Exhaustive: in.Exhaustive})
	envRef := sparkwing.RefTo[verifyEnv](build)

	deps := make([]sparkwing.Dep, 0, len(verifyTemplates))
	for _, tmpl := range verifyTemplates {
		m := tmpl.Manifest
		node := sparkwing.Job(plan, "verify-"+m.Name, verifyTemplateFn(m, envRef)).Needs(build).Concurrency(templateVerifyGroup)
		deps = append(deps, node)
	}

	sparkwing.Job(plan, "summary", &templateVerifyGate{}).Needs(deps...).Inline()
	return nil
}

type buildVerifyCLIJob struct {
	sparkwing.Base
	sparkwing.Produces[verifyEnv]

	Exhaustive bool
}

func (j *buildVerifyCLIJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", j.run), nil
}

func (j *buildVerifyCLIJob) run(ctx context.Context) (verifyEnv, error) {
	root := sparkwing.WorkDir()
	if root == "" {
		return verifyEnv{}, errors.New("template-verify: sparkwing.WorkDir() is empty")
	}
	if err := acquireTemplateVerifyLock(os.TempDir()); err != nil {
		return verifyEnv{}, fmt.Errorf("template-verify: acquire scratch ownership: %w", err)
	}
	if err := cleanupTemplateScratch(os.TempDir()); err != nil {
		return verifyEnv{}, fmt.Errorf("template-verify: reclaim stale scratch: %w", err)
	}
	free, err := availableTemplateVerifyDisk(os.TempDir())
	if err != nil {
		return verifyEnv{}, fmt.Errorf("template-verify: measure scratch capacity: %w", err)
	}
	if err := requireTemplateVerifyDisk(free); err != nil {
		return verifyEnv{}, err
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
	proof := resolveProofEnv(ctx, root, core, j.Exhaustive)
	if proof.Reusable {
		sparkwing.Annotate(ctx, "proof inputs digested; templates whose inputs are unchanged reuse a recorded proof")
	} else {
		sparkwing.Info(ctx, "verifying every template: %s", proof.Reason)
	}
	return verifyEnv{
		CLI:        bin,
		Root:       root,
		StateHome:  filepath.Join(dir, "state"),
		SparksCore: core,
		GoEnv:      readGoEnv(ctx),
		Proof:      proof,
	}, nil
}

func acquireTemplateVerifyLock(root string) error {
	templateVerifyLockOnce.Do(func() {
		path := filepath.Join(root, "sparkwing-template-verify.lock")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			templateVerifyLockErr = err
			return
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			_ = f.Close()
			templateVerifyLockErr = err
			return
		}
		templateVerifyLockFile = f
	})
	return templateVerifyLockErr
}

func cleanupTemplateScratch(root string) error {
	for _, pattern := range []string{"sparkwing-tv-*", "sparkwing-template-verify-cli-*"} {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			return err
		}
		for _, path := range matches {
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("remove %s: %w", filepath.Base(path), err)
			}
		}
	}
	return nil
}

func availableTemplateVerifyDisk(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}

func requireTemplateVerifyDisk(free uint64) error {
	if free < templateVerifyMinFreeDisk {
		return fmt.Errorf("template-verify: scratch filesystem has %.1f GiB free; at least 10 GiB is required before compiling templates",
			float64(free)/(1<<30))
	}
	return nil
}

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

func verifyTemplateFn(m templates.Manifest, envRef sparkwing.Ref[verifyEnv]) func(context.Context) error {
	return func(ctx context.Context) error {
		env := envRef.Get(ctx)
		digest, dir, reuse := reusableProof(ctx, env.Proof, m)
		if reuse {
			sparkwing.Annotate(ctx, fmt.Sprintf("%s: reused a recorded proof; every input digest matched", m.Name))
			return nil
		}
		if err := verifyTemplate(ctx, m, env); err != nil {
			return err
		}
		if digest == "" {
			return nil
		}
		if err := recordProof(dir, digest, m.Name); err != nil {
			sparkwing.Info(ctx, "%s: proof not recorded, so the next run verifies again: %v", m.Name, err)
		}
		return nil
	}
}

// safety: every failure to establish an input returns no reuse and no digest,
// so a template whose inputs cannot be pinned down is verified again and its
// pass is not recorded against a digest that does not describe it.
func reusableProof(ctx context.Context, env proofEnv, m templates.Manifest) (digest, dir string, reuse bool) {
	if !env.Reusable {
		return "", "", false
	}
	dir, err := proofDir()
	if err != nil {
		return "", "", false
	}
	digest, err = templateProofDigest(ctx, env, m)
	if err != nil {
		sparkwing.Info(ctx, "%s: verifying again, no digest: %v", m.Name, err)
		return "", "", false
	}
	return digest, dir, proofRecorded(dir, digest)
}

func verifyTemplate(ctx context.Context, m templates.Manifest, env verifyEnv) error {
	bin := env.CLI
	scratch, err := os.MkdirTemp("", "sparkwing-tv-"+m.Name+"-*")
	if err != nil {
		return fmt.Errorf("%s: temp dir: %w", m.Name, err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	newArgs := []string{"examples", "scaffold", "-C", scratch, "--name", m.Name}
	for _, p := range sortedParamFlags(m.VerifyParams) {
		newArgs = append(newArgs, "--param", p)
	}
	if _, err := sparkwing.Exec(ctx, bin, newArgs...).Run(); err != nil {
		return fmt.Errorf("%s: scaffold: %w", m.Name, err)
	}

	dotSparkwing := filepath.Join(scratch, ".sparkwing")
	if err := normalizeVerifyModulePath(dotSparkwing, m.Name); err != nil {
		return fmt.Errorf("%s: normalize module path: %w", m.Name, err)
	}
	if err := pinLocalSparksCore(ctx, dotSparkwing, env.SparksCore); err != nil {
		return fmt.Errorf("%s: pin sparks-core: %w", m.Name, err)
	}
	if err := pinLocalSparkwingSDK(ctx, dotSparkwing, env.Root); err != nil {
		return fmt.Errorf("%s: pin sparkwing SDK: %w", m.Name, err)
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
		runCmd := sparkwing.Exec(ctx, bin, "run", m.Name).
			Dir(scratch)
		for name, value := range templateRunAdmissionEnv(env.StateHome) {
			runCmd = runCmd.Env(name, value)
		}
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

func templateRunAdmissionEnv(stateHome string) map[string]string {
	return map[string]string{
		"SPARKWING_HOME":            stateHome,
		wingwire.LeaseTokenEnv:      "",
		wingwire.ChildLeaseTokenEnv: "",
	}
}

func normalizeVerifyModulePath(dotSparkwing, templateName string) error {
	path := filepath.Join(dotSparkwing, "go.mod")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(raw), "\n")
	found := false
	oldModule := ""
	newModule := "example.com/sparkwing/verify/" + templateName + "/pipelines"
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "module ") {
			oldModule = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "module "))
			lines[i] = "module " + newModule
			found = true
			break
		}
	}
	if !found {
		return errors.New("go.mod has no module directive")
	}
	// #nosec G703 -- a pipeline job writing under the repository it runs in
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		return err
	}
	return filepath.Walk(dotSparkwing, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Ext(path) != ".go" {
			return err
		}
		// #nosec G122 -- a TOCTOU swap here needs write access to the checkout this build-time job already trusts
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		updated := bytes.ReplaceAll(data, []byte(oldModule+"/"), []byte(newModule+"/"))
		if bytes.Equal(data, updated) {
			return nil
		}
		// #nosec G122,G703 -- a TOCTOU swap needs write access to the checkout this build-time job already trusts
		return os.WriteFile(path, updated, info.Mode())
	})
}

func sortedParamFlags(params map[string]string) []string {
	out := make([]string, 0, len(params))
	for k, v := range params {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

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

func pinLocalSparkwingSDK(ctx context.Context, dotSparkwing, root string) error {
	if root == "" {
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
	fmt.Fprintf(&b, "\nreplace github.com/sparkwing-dev/sparkwing => %s\n", root)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return err
	}
	if _, err := sparkwing.Exec(ctx, "go", "mod", "tidy").Dir(dotSparkwing).Env("GOWORK", "off").Run(); err != nil {
		return fmt.Errorf("go mod tidy: %w", err)
	}
	return nil
}

var goModModuleRe = regexp.MustCompile(`(?m)^module[\t ]+(\S+)`)

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
	// #nosec G703 -- a pipeline job stating paths under the repository it runs in
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

func dockerAvailable(ctx context.Context) bool {
	_, err := sparkwing.Exec(ctx, "docker", "info").Capture()
	return err == nil
}

func gitInitFixture(ctx context.Context, root string) error {
	ident := []string{"-c", "user.email=verify@sparkwing.invalid", "-c", "user.name=template-verify"}
	for _, args := range [][]string{
		{"init", "--quiet"},
		append(append([]string{}, ident...), "add", "-A"),
		append(append([]string{}, ident...), "commit", "--allow-empty", "--quiet", "-m", "fixture"),
	} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %v: %w: %s", args, err, out)
		}
	}
	return nil
}

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

func seedGoModule(root string) error {
	files := map[string]string{
		"go.mod":       "module verifyfixture\n\ngo 1.26\n",
		"main.go":      "package main\n\nfunc main() {}\n\n// Sum returns the total of nums.\nfunc Sum(nums ...int) int {\n\ttotal := 0\n\tfor _, n := range nums {\n\t\ttotal += n\n\t}\n\treturn total\n}\n",
		"main_test.go": "package main\n\nimport \"testing\"\n\nfunc TestSum(t *testing.T) {\n\tif got := Sum(1, 2, 3); got != 6 {\n\t\tt.Fatalf(\"Sum = %d, want 6\", got)\n\t}\n}\n",
		filepath.Join("integration", "integration_test.go"): "package integration\n\nimport \"testing\"\n\nfunc TestIntegration(t *testing.T) {}\n",
	}
	return writeFixtureFiles(root, files)
}

func provisionFixture(ctx context.Context, scratch string, m templates.Manifest, env verifyEnv) (func(), map[string]string, error) {
	noop := func() {}
	if m.Fixture() != templates.FixturePostgres {
		if err := seedFixture(scratch, m.Fixture()); err != nil {
			return noop, nil, err
		}
		return noop, nil, gitInitFixture(ctx, scratch)
	}
	if err := seedGoModule(scratch); err != nil {
		return noop, nil, err
	}
	migrationsDir := valueOr(m.VerifyParams["migrations-dir"], "db/migrations")
	if err := seedMigrations(scratch, migrationsDir); err != nil {
		return noop, nil, err
	}
	if err := gitInitFixture(ctx, scratch); err != nil {
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

func seedMigrations(root, dir string) error {
	files := map[string]string{
		filepath.Join(dir, "0001_init.up.sql"):   "CREATE TABLE verify_fixture (id integer PRIMARY KEY);\n",
		filepath.Join(dir, "0001_init.down.sql"): "DROP TABLE verify_fixture;\n",
	}
	return writeFixtureFiles(root, files)
}

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
	sparkwing.Register[TemplateVerifyArgs]("template-verify", func() sparkwing.Pipeline[TemplateVerifyArgs] { return &TemplateVerify{} })
}
