package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
  "pipeline new"|"pipeline explain") printf 'ok\n' ;;
  "daemon stop") printf 'stopped\n' ;;
  "run demo")
    if [[ "${INSTALL_TO_GREEN_STUB_RUN:-green}" == exit-nonzero ]]; then
      printf 'boom\n' >&2
      exit 1
    fi
    if [[ "${INSTALL_TO_GREEN_STUB_RUN:-green}" == silent ]]; then
      printf '{"event":"node_end"}\n'
      exit 0
    fi
    printf '{"event":"run_finish","attrs":{"status":"success"}}\n'
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
	DominantPhase string `json:"dominant_phase"`
	Mode          string `json:"mode"`
	Template      string `json:"template"`
	Version       string `json:"version"`
	Green         bool   `json:"green"`
	TargetSeconds int    `json:"target_seconds"`
	TargetMet     bool   `json:"target_met"`
}

type installToGreenResult struct {
	stdout string
	stderr string
	err    error
	trace  string
	tmpDir string
}

func runInstallToGreen(t *testing.T, stubRun string, args ...string) installToGreenResult {
	t.Helper()
	for _, tool := range []string{"openssl", "curl", "git", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH, so the harness cannot run: %v", tool, err)
		}
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
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

	cmd := exec.Command("bash", append([]string{filepath.Join(root, "bin", "install-to-green.sh"), "--binary", stub}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"INSTALL_TO_GREEN_TRACE="+trace,
		"INSTALL_TO_GREEN_STUB_RUN="+stubRun,
		"TMPDIR="+scratch,
	)
	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	runErr := cmd.Run()

	recorded, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	return installToGreenResult{stdout: out.String(), stderr: errOut.String(), err: runErr, trace: string(recorded), tmpDir: scratch}
}

func TestInstallToGreenReportsEveryPhaseOfTheDemoPath(t *testing.T) {
	result := runInstallToGreen(t, "green", "--output", "json")
	if result.err != nil {
		t.Fatalf("harness failed on a green demo path: %v\nstdout: %s\nstderr: %s", result.err, result.stdout, result.stderr)
	}

	var report installToGreenReport
	if err := json.Unmarshal([]byte(result.stdout), &report); err != nil {
		t.Fatalf("harness did not print one JSON report: %v\ngot: %s", err, result.stdout)
	}
	if !report.Green {
		t.Error("a successful run_finish record was not reported as green")
	}
	if report.Mode != "binary" || report.Template != "minimal" || report.Version != "v9.9.9" {
		t.Errorf("report does not name what it measured: mode=%q template=%q version=%q", report.Mode, report.Template, report.Version)
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
	if difference := report.TotalSeconds - summed; difference > 0.01 || difference < -0.01 {
		t.Errorf("total %v does not account for the phases summing to %v", report.TotalSeconds, summed)
	}
	if _, ok := phases[report.DominantPhase]; !ok {
		t.Errorf("dominant phase %q is not one of the measured phases", report.DominantPhase)
	}
}

func TestInstallToGreenMeasuresAgainstACleanHomeAndAnEmptyModuleCache(t *testing.T) {
	result := runInstallToGreen(t, "green", "--output", "json")
	if result.err != nil {
		t.Fatalf("harness failed: %v\nstderr: %s", result.err, result.stderr)
	}

	callerHome := os.Getenv("HOME")
	var scaffolds int
	for _, line := range strings.Split(strings.TrimSpace(result.trace), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 5 {
			t.Fatalf("stub recorded an unreadable line: %q", line)
		}
		verb, home, modCache, sparkwingHome, entries := fields[0], fields[1], fields[2], fields[3], fields[4]
		if sparkwingHome == "" {
			// safety: staging reads the version tag before the demo environment
			// exists, so that probe is not part of the measurement.
			if verb != "version" {
				t.Errorf("%s ran outside the demo environment", verb)
			}
			continue
		}
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

func TestInstallToGreenRefusesToCallAnUnprovenRunGreen(t *testing.T) {
	silent := runInstallToGreen(t, "silent", "--output", "json")
	if silent.err == nil {
		t.Errorf("harness called a run green with no success record: %s", silent.stdout)
	}
	if !strings.Contains(silent.stderr, "without recording a green run") {
		t.Errorf("refusal does not say the run was never recorded green: %s", silent.stderr)
	}

	failed := runInstallToGreen(t, "exit-nonzero", "--output", "json")
	if failed.err == nil {
		t.Errorf("harness reported a number for a failing run: %s", failed.stdout)
	}
	if !strings.Contains(failed.stderr, "the run phase failed") {
		t.Errorf("refusal does not name the failing phase: %s", failed.stderr)
	}
}

func TestInstallToGreenFailsAnExplicitTargetAndNamesTheDominantPhase(t *testing.T) {
	result := runInstallToGreen(t, "green", "--output", "json", "--target-seconds", "0")
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

func TestInstallToGreenRemovesItsScratchTree(t *testing.T) {
	result := runInstallToGreen(t, "green", "--output", "json")
	if result.err != nil {
		t.Fatalf("harness failed: %v\nstderr: %s", result.err, result.stderr)
	}
	left, err := os.ReadDir(result.tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("harness left %d entries behind in its temporary directory", len(left))
	}
}
