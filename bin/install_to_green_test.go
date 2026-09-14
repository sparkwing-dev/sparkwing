package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// safety: the installer verifies this stub like any signed asset, so its
// version verb must answer in the release format the installer parses.
const installToGreenStub = `#!/usr/bin/env bash
set -euo pipefail
entries=0
if [[ -d "${GOMODCACHE:-}" ]]; then
  entries="$(find "$GOMODCACHE" -mindepth 1 -maxdepth 1 | wc -l | tr -d '[:space:]')"
fi
printf '%s\t%s\t%s\t%s\t%s\n' "$1" "${HOME:-}" "${GOMODCACHE:-}" "${SPARKWING_HOME:-}" "$entries" >>"$INSTALL_TO_GREEN_TRACE"
case "$1 ${2:-}" in
  "version -o")
    case "${3:-}" in
      json) printf '{"installed":"v9.9.9"}\n' ;;
      *) printf 'v9.9.9\n' ;;
    esac
    ;;
  "info --first-time") printf 'first time\n' ;;
  "pipeline new")
    if [[ "${INSTALL_TO_GREEN_STUB_SCAFFOLD:-ok}" == offline ]]; then
      printf 'go: github.com/sparkwing-dev/sparkwing@v9.9.9: dial tcp: lookup proxy.golang.org: no such host\n' >&2
      exit 1
    fi
    printf 'ok\n'
    ;;
  "pipeline explain") printf 'ok\n' ;;
  "daemon stop") printf 'stopped\n' ;;
  "run demo")
    case "${INSTALL_TO_GREEN_STUB_RUN:-green}" in
      exit-nonzero) printf 'boom\n' >&2; exit 1 ;;
      silent) printf '{"event":"node_end"}\n' ;;
      split)
        printf '{"event":"run_finish"}\n'
        printf '{"event":"node_end","attrs":{"status":"success"}}\n'
        ;;
      *) printf '{"event":"run_finish","attrs":{"status":"success"}}\n' ;;
    esac
    ;;
  *) printf 'unexpected: %s\n' "$*" >&2; exit 64 ;;
esac
`

type installToGreenReport struct {
	TotalSeconds float64 `json:"total_seconds"`
	Phases       struct {
		Install  float64 `json:"install"`
		Scaffold float64 `json:"scaffold"`
		Compile  float64 `json:"compile"`
		Run      float64 `json:"run"`
	} `json:"phases"`
	DominantPhase  string   `json:"dominant_phase"`
	Mode           string   `json:"mode"`
	Template       string   `json:"template"`
	Version        string   `json:"version"`
	SDKSource      string   `json:"sdk_source"`
	Green          bool     `json:"green"`
	StartedAt      string   `json:"started_at"`
	Cores          int      `json:"cores"`
	LoadStart      *float64 `json:"load_start"`
	LoadEnd        *float64 `json:"load_end"`
	GoProxy        string   `json:"goproxy"`
	InheritedGoEnv []string `json:"inherited_go_env"`
	TargetSeconds  int      `json:"target_seconds"`
	TargetMet      bool     `json:"target_met"`
}

type installToGreenResult struct {
	stdout   string
	stderr   string
	err      error
	exitCode int
	trace    string
	tmpDir   string
}

type installToGreenOptions struct {
	stubRun      string
	stubScaffold string
	env          []string
	args         []string
}

func installToGreenRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func runInstallToGreen(t *testing.T, options installToGreenOptions) installToGreenResult {
	t.Helper()
	for _, tool := range []string{"openssl", "curl", "git", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH, so the harness cannot run: %v", tool, err)
		}
	}
	root := installToGreenRoot(t)
	fixture := t.TempDir()
	stub := filepath.Join(fixture, "sparkwing-stub")
	if err := os.WriteFile(stub, []byte(installToGreenStub), 0o755); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(fixture, "trace")
	if err := os.WriteFile(trace, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(fixture, "scratch")
	if err := os.Mkdir(scratch, 0o755); err != nil {
		t.Fatal(err)
	}

	argv := append([]string{filepath.Join(root, "bin", "install-to-green.sh"), "--binary", stub}, options.args...)
	cmd := exec.Command("bash", argv...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"INSTALL_TO_GREEN_TRACE="+trace,
		"INSTALL_TO_GREEN_STUB_RUN="+options.stubRun,
		"INSTALL_TO_GREEN_STUB_SCAFFOLD="+options.stubScaffold,
		"TMPDIR="+scratch,
	)
	cmd.Env = append(cmd.Env, options.env...)
	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	runErr := cmd.Run()

	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			t.Fatalf("harness could not be started: %v", runErr)
		}
		exitCode = exitErr.ExitCode()
	}

	recorded, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	return installToGreenResult{
		stdout:   out.String(),
		stderr:   errOut.String(),
		err:      runErr,
		exitCode: exitCode,
		trace:    string(recorded),
		tmpDir:   scratch,
	}
}

func measureGreen(t *testing.T, args ...string) (installToGreenResult, installToGreenReport) {
	t.Helper()
	result := runInstallToGreen(t, installToGreenOptions{stubRun: "green", args: append([]string{"--output", "json"}, args...)})
	if result.err != nil {
		t.Fatalf("harness failed on a green demo path: %v\nstdout: %s\nstderr: %s", result.err, result.stdout, result.stderr)
	}
	var report installToGreenReport
	if err := json.Unmarshal([]byte(result.stdout), &report); err != nil {
		t.Fatalf("harness did not print one JSON report: %v\ngot: %s", err, result.stdout)
	}
	return result, report
}

func TestInstallToGreenReportsEveryPhaseOfTheDemoPath(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	_, report := measureGreen(t)
	if !report.Green {
		t.Error("a successful run_finish record was not reported as green")
	}
	if report.Mode != "binary" || report.Template != "minimal" || report.Version != "v9.9.9" {
		t.Errorf("report does not name what it measured: mode=%q template=%q version=%q", report.Mode, report.Template, report.Version)
	}
	if !strings.Contains(report.SDKSource, "v9.9.9") {
		t.Errorf("a staged binary must report the released module it scaffolds against, got %q", report.SDKSource)
	}
	phases := map[string]float64{
		"install":  report.Phases.Install,
		"scaffold": report.Phases.Scaffold,
		"compile":  report.Phases.Compile,
		"run":      report.Phases.Run,
	}
	var summed float64
	for name, seconds := range phases {
		if seconds < 0 {
			t.Errorf("phase %s reported %v seconds", name, seconds)
		}
		summed += seconds
	}
	if difference := report.TotalSeconds - summed; difference > 0.0005 || difference < -0.0005 {
		t.Errorf("total %v is not the sum of the printed phases %v", report.TotalSeconds, summed)
	}
	if _, ok := phases[report.DominantPhase]; !ok {
		t.Errorf("dominant phase %q is not one of the measured phases", report.DominantPhase)
	}
}

func TestInstallToGreenRecordsTheContextTheNumberNeeds(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	_, report := measureGreen(t)
	if report.Cores <= 0 {
		t.Errorf("report claims %d cores", report.Cores)
	}
	if report.StartedAt == "" {
		t.Error("report carries no start timestamp")
	} else if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`).MatchString(report.StartedAt) {
		t.Errorf("start timestamp %q is not UTC RFC 3339", report.StartedAt)
	}
	if report.GoProxy == "" {
		t.Error("report does not say which module proxy resolved the scaffold")
	}
	if report.LoadStart == nil || report.LoadEnd == nil {
		t.Errorf("report carries no load average: start=%v end=%v", report.LoadStart, report.LoadEnd)
	}
}

func TestInstallToGreenPinsModuleResolutionAndNamesWhatItReset(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.5s of real work; the fast class runs under -short")
	}
	_, reset := measureGreen(t)
	if reset.GoProxy != "https://proxy.golang.org,direct" {
		t.Errorf("a reset run resolved through %q, not the public default", reset.GoProxy)
	}

	result := runInstallToGreen(t, installToGreenOptions{
		stubRun: "green",
		args:    []string{"--output", "json"},
		env:     []string{"GOFLAGS=-mod=mod", "GOPROXY=off"},
	})
	if result.err != nil {
		t.Fatalf("harness failed: %v\nstderr: %s", result.err, result.stderr)
	}
	var report installToGreenReport
	if err := json.Unmarshal([]byte(result.stdout), &report); err != nil {
		t.Fatalf("harness did not print one JSON report: %v\ngot: %s", err, result.stdout)
	}
	if report.GoProxy != "https://proxy.golang.org,direct" {
		t.Errorf("an inherited GOPROXY survived the reset: %q", report.GoProxy)
	}
	inherited := strings.Join(report.InheritedGoEnv, ",")
	for _, want := range []string{"GOPROXY", "GOFLAGS"} {
		if !strings.Contains(inherited, want) {
			t.Errorf("report does not say %s was inherited and reset: %v", want, report.InheritedGoEnv)
		}
	}
}

func TestInstallToGreenMeasuresAgainstACleanHomeAndAnEmptyModuleCache(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	result, _ := measureGreen(t)

	callerHome := os.Getenv("HOME")
	var scaffolds int
	for _, line := range strings.Split(strings.TrimSpace(result.trace), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 5 {
			t.Fatalf("stub recorded an unreadable line: %q", line)
		}
		verb, home, modCache, sparkwingHome, entries := fields[0], fields[1], fields[2], fields[3], fields[4]
		if callerHome != "" && home == callerHome {
			t.Errorf("%s ran against the caller's HOME %q", verb, home)
		}
		if !strings.HasPrefix(sparkwingHome, home) {
			t.Errorf("%s ran against sparkwing home %q, which is outside the demo HOME %q", verb, sparkwingHome, home)
		}
		if !strings.HasPrefix(modCache, home) {
			t.Errorf("%s ran against module cache %q, which is outside the demo HOME %q", verb, modCache, home)
		}
		if verb == "pipeline" {
			scaffolds++
			if entries != "0" {
				t.Errorf("the module cache held %s entries before the pipeline was compiled", entries)
			}
		}
	}
	if scaffolds == 0 {
		t.Error("the harness never reached the scaffold or compile phase")
	}
}

func TestInstallToGreenLeavesTheCallersGitBindingAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not on PATH: %v", err)
	}
	operatorRepo := operatorRepository(t)
	commitsBefore := gitCommitCount(t, operatorRepo)

	// safety: this is the environment a git hook, `git rebase --exec` and
	// `git bisect run` hand down; a harness that keeps it commits into the
	// operator's repository instead of its own scratch one.
	result := runInstallToGreen(t, installToGreenOptions{
		stubRun: "green",
		args:    []string{"--output", "json"},
		env: []string{
			"GIT_DIR=" + filepath.Join(operatorRepo, ".git"),
			"GIT_WORK_TREE=" + operatorRepo,
			"GIT_INDEX_FILE=" + filepath.Join(operatorRepo, ".git", "index"),
		},
	})
	if result.err != nil {
		t.Fatalf("harness failed under a bound git environment: %v\nstderr: %s", result.err, result.stderr)
	}
	if after := gitCommitCount(t, operatorRepo); after != commitsBefore {
		t.Errorf("the harness committed into the caller's repository: %s commits before, %s after", commitsBefore, after)
	}
}

func operatorRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{
			"-c", "user.name=test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false",
			"-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-qm", "operator work",
		},
	} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = environWithoutGitBindings()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, out)
		}
	}
	return repo
}

func gitCommitCount(t *testing.T, repo string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "rev-list", "--count", "HEAD")
	cmd.Env = environWithoutGitBindings()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("count commits in %s: %v", repo, err)
	}
	return strings.TrimSpace(string(out))
}

func environWithoutGitBindings() []string {
	var kept []string
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GIT_") {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

func gitBindingNames(t *testing.T) []string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(installToGreenRoot(t), "pkg", "gitenv", "gitenv.go"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)var bindingVars = \[\]string\{(.*?)\n\}`).FindSubmatch(source)
	if block == nil {
		t.Fatal("pkg/gitenv no longer declares bindingVars as a slice literal")
	}
	var names []string
	for _, match := range regexp.MustCompile(`"([A-Z_]+)"`).FindAllStringSubmatch(string(block[1]), -1) {
		names = append(names, match[1])
	}
	if len(names) == 0 {
		t.Fatal("pkg/gitenv bindingVars listed no variables")
	}
	return names
}

func gitBindingValue(name, repo string) string {
	switch name {
	case "GIT_WORK_TREE", "GIT_PREFIX":
		return repo
	case "GIT_INDEX_FILE":
		return filepath.Join(repo, ".git", "index")
	case "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_QUARANTINE_PATH":
		return filepath.Join(repo, ".git", "objects")
	case "GIT_NAMESPACE":
		return "probe"
	default:
		return filepath.Join(repo, ".git")
	}
}

func TestInstallToGreenResetsEveryGitBindingVariable(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 2.1s of real work; the fast class runs under -short")
	}
	for _, name := range gitBindingNames(t) {
		t.Run(name, func(t *testing.T) {
			operatorRepo := operatorRepository(t)
			commitsBefore := gitCommitCount(t, operatorRepo)
			result := runInstallToGreen(t, installToGreenOptions{
				stubRun: "green",
				args:    []string{"--output", "json"},
				env:     []string{name + "=" + gitBindingValue(name, operatorRepo)},
			})
			if result.err != nil {
				t.Fatalf("harness failed with %s bound: %v\nstderr: %s", name, result.err, result.stderr)
			}
			if after := gitCommitCount(t, operatorRepo); after != commitsBefore {
				t.Errorf("with %s bound the harness committed into the caller's repository: %s commits before, %s after",
					name, commitsBefore, after)
			}
		})
	}
}

func TestInstallToGreenRefusesToCallAnUnprovenRunGreen(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.7s of real work; the fast class runs under -short")
	}
	for _, stubRun := range []string{"silent", "split"} {
		result := runInstallToGreen(t, installToGreenOptions{stubRun: stubRun, args: []string{"--output", "json"}})
		if result.err == nil {
			t.Errorf("%s: harness called a run green with no success record: %s", stubRun, result.stdout)
		}
		if !strings.Contains(result.stderr, "without recording a green run") {
			t.Errorf("%s: refusal does not say the run was never recorded green: %s", stubRun, result.stderr)
		}
	}

	failed := runInstallToGreen(t, installToGreenOptions{stubRun: "exit-nonzero", args: []string{"--output", "json"}})
	if failed.err == nil {
		t.Errorf("harness reported a number for a failing run: %s", failed.stdout)
	}
	if failed.exitCode != 1 {
		t.Errorf("a failing run exited %d, want 1", failed.exitCode)
	}
	if !strings.Contains(failed.stderr, "the run phase failed") {
		t.Errorf("refusal does not name the failing phase: %s", failed.stderr)
	}
}

func TestInstallToGreenSeparatesAnUnreachableProxyFromASlowDemoPath(t *testing.T) {
	result := runInstallToGreen(t, installToGreenOptions{
		stubRun:      "green",
		stubScaffold: "offline",
		args:         []string{"--output", "json"},
	})
	if result.err == nil {
		t.Fatalf("harness reported a number without reaching the proxy: %s", result.stdout)
	}
	if result.exitCode != 75 {
		t.Errorf("an unreachable proxy exited %d, want 75 so a caller can record a skipped measurement", result.exitCode)
	}
	if !strings.Contains(result.stderr, "nothing was measured") {
		t.Errorf("the refusal does not say nothing was measured: %s", result.stderr)
	}
	var skipped struct {
		Measured  bool   `json:"measured"`
		Green     bool   `json:"green"`
		Phase     string `json:"phase"`
		StartedAt string `json:"started_at"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.stdout)), &skipped); err != nil {
		t.Fatalf("a skipped measurement printed no record a caller can log: %v\ngot: %s", err, result.stdout)
	}
	if skipped.Measured || skipped.Green {
		t.Errorf("the skipped record claims a measurement: %+v", skipped)
	}
	if skipped.Phase != "scaffold" {
		t.Errorf("the skipped record names phase %q, want the phase that could not resolve", skipped.Phase)
	}
	if skipped.StartedAt == "" || skipped.Reason == "" {
		t.Errorf("the skipped record carries no timestamp or reason: %+v", skipped)
	}
}

func TestInstallToGreenFailsAnExplicitTargetAndNamesTheDominantPhase(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	result := runInstallToGreen(t, installToGreenOptions{stubRun: "green", args: []string{"--output", "json", "--target-seconds", "0"}})
	if result.err == nil {
		t.Fatalf("harness met a zero-second target: %s", result.stdout)
	}
	var report installToGreenReport
	if err := json.Unmarshal([]byte(result.stdout), &report); err != nil {
		t.Fatalf("harness did not print its report before failing the target: %v\ngot: %s", err, result.stdout)
	}
	if report.TargetSeconds != 0 || report.TargetMet {
		t.Errorf("report claims the target was met: target=%d met=%v", report.TargetSeconds, report.TargetMet)
	}
	if !strings.Contains(result.stderr, report.DominantPhase) {
		t.Errorf("the over-target message does not name the dominant phase %q: %s", report.DominantPhase, result.stderr)
	}
}

func TestInstallToGreenPointsACandidateBuildAtTheWorktreeSDK(t *testing.T) {
	root := installToGreenRoot(t)
	harness, err := os.ReadFile(filepath.Join(root, "bin", "install-to-green.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(harness), `mod edit -replace "github.com/sparkwing-dev/sparkwing=$ROOT"`) {
		t.Error("a candidate build must scaffold against this worktree's SDK, not the released module its tag names")
	}
	if !strings.Contains(string(harness), `mod tidy`) {
		t.Error("a candidate build must resolve the scaffolded module after the replace, or an added dependency fails the compile")
	}
	if !strings.Contains(string(harness), `scaffold_proxy=("GOPROXY=off")`) {
		t.Error("a candidate build must not pay to download the published SDK its compile discards")
	}
	if !strings.Contains(string(harness), `grep -E '^v0\.[0-9]+\.[0-9]+$'`) {
		t.Error("candidate tag selection must exclude prerelease and local candidate tags")
	}
}

func TestInstallToGreenRemovesItsScratchTree(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	result, _ := measureGreen(t)
	left, err := os.ReadDir(result.tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("harness left %d entries behind in its temporary directory", len(left))
	}
}

// safety: a candidate that adds a dependency leaves the scaffolded module
// without the go.sum entry for it, which is why build mode tidies after
// pointing the module at the worktree. This fixture reproduces that failure
// against the real toolchain rather than trusting the shape of the fix.
func TestInstallToGreenTidiesSoACandidateDependencyResolves(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go is not on PATH: %v", err)
	}
	root := installToGreenRoot(t)
	rootMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	pin := regexp.MustCompile(`golang\.org/x/mod (v[^\s]+)`).FindSubmatch(rootMod)
	if pin == nil {
		t.Skip("this module no longer pins golang.org/x/mod, so the fixture has no warm dependency to use")
	}
	dependency := "golang.org/x/mod " + string(pin[1])

	fixture := t.TempDir()
	sdk := filepath.Join(fixture, "sdk")
	scaffold := filepath.Join(fixture, "scaffold")
	write := func(dir, name, content string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(sdk, "go.mod", "module example.invalid/sdk\n\ngo 1.24\n\nrequire "+dependency+"\n")
	write(sdk, "sdk.go", "package sdk\n\nimport \"golang.org/x/mod/semver\"\n\nfunc Canonical(v string) string { return semver.Canonical(v) }\n")
	write(scaffold, "go.mod", "module example.invalid/scaffold\n\ngo 1.24\n\nrequire example.invalid/sdk v0.0.1\n\nreplace example.invalid/sdk => ../sdk\n")
	write(scaffold, "main.go", "package main\n\nimport \"example.invalid/sdk\"\n\nfunc main() { _ = sdk.Canonical(\"v1.0.0\") }\n")

	run := func(dir string, args ...string) (string, error) {
		cmd := exec.Command(goBinary, args...)
		cmd.Dir = dir
		cmd.Env = append(environWithoutGitBindings(), "GOWORK=off", "GOFLAGS=")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	if out, err := run(sdk, "mod", "tidy"); err != nil {
		t.Skipf("the fixture SDK's dependency could not be resolved, so the failure cannot be staged: %v\n%s", err, out)
	}

	out, err := run(scaffold, "build", "-o", os.DevNull, ".")
	if err == nil {
		t.Skip("this toolchain builds a replaced module with no go.sum entry, so the failure build mode tidies against does not occur")
	}
	if !strings.Contains(out, "go.sum") && !strings.Contains(out, "updates to go.mod needed") {
		t.Fatalf("the fixture failed for another reason than an unresolved candidate graph:\n%s", out)
	}

	if out, err := run(scaffold, "mod", "tidy"); err != nil {
		t.Fatalf("go mod tidy could not resolve the candidate graph: %v\n%s", err, out)
	}
	if out, err := run(scaffold, "build", "-o", os.DevNull, "."); err != nil {
		t.Fatalf("a candidate dependency still does not compile after the tidy build mode runs: %v\n%s", err, out)
	}
}
